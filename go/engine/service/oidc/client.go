package oidc

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/clock"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/limits"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/secret"
)

// The refusals this package answers with. None of them chooses a wire status:
// the protocol layer maps them once.
var (
	// ErrNoTrustAnchors is an empty certificate pool at startup.
	//
	// An empty pool is a refusal rather than a fallback to the system's own,
	// because a pool that verifies nothing verifies everything: every
	// certificate fails, the operator turns verification off to get past it,
	// and the back channel that carries the client secret is then
	// unauthenticated.
	ErrNoTrustAnchors = errors.New("the certificate pool is empty")

	// ErrProvider is the provider being unreachable, answering something
	// unparseable, or redirecting.
	ErrProvider = errors.New("the provider did not answer usably")
)

// ProviderError says which of those it was.
type ProviderError struct{ Reason string }

func (e *ProviderError) Error() string { return "the provider: " + e.Reason }

func (e *ProviderError) Is(target error) bool { return target == ErrProvider }

// Config is what this package is told. The layer above owns parsing and the
// settings rules; what arrives here is already the validated form.
type Config struct {
	Issuer       string
	ClientID     string
	ClientSecret secret.Secret

	// Scopes always ends up carrying the one that makes an identity token be
	// issued at all, because without it the flow produces nothing this server
	// can verify.
	Scopes []string

	// AllowPrivateEndpoints is the operator's opt-in for a provider on the
	// same network as this server.
	AllowPrivateEndpoints bool

	// CACertFile replaces the trust anchors with a file of them. Empty takes
	// the system's pool.
	CACertFile string
}

// Client is the relying party. It is safe for concurrent use: the two caches
// carry their own mutexes and nothing else is mutable.
type Client struct {
	cfg   Config
	guard guard
	http  *http.Client
	clk   clock.Clock

	discovery *ttlCache[*Discovery]
	jwks      *ttlCache[[]jwk]
}

// New builds the client, or refuses.
//
// The certificate pool is explicit. Taking the system's unconditionally would
// mean a runtime image shipping no bundle silently verifies against nothing,
// and the one connection this makes carries a client secret.
func New(cfg Config, clk clock.Clock) (*Client, error) {
	pool, err := trustAnchors(cfg.CACertFile)
	if err != nil {
		return nil, err
	}
	if clk == nil {
		clk = clock.System()
	}

	g := guard{allowPrivate: cfg.AllowPrivateEndpoints}
	return &Client{
		cfg:       cfg,
		guard:     g,
		clk:       clk,
		discovery: newTTLCache[*Discovery](limits.OIDCDiscoveryTTL),
		jwks:      newTTLCache[[]jwk](limits.OIDCJWKSTTL),
		http: &http.Client{
			Timeout: limits.OIDCRequestTimeout,
			// A redirect is a refusal. The endpoint is where discovery said it
			// is, and a redirect names a final URL nobody checked: the address
			// guard ran against the address that was named.
			CheckRedirect: func(req *http.Request, _ []*http.Request) error {
				return &ProviderError{
					Reason: "answered with a redirect to " + req.URL.Redacted() +
						", and the back channel does not follow one",
				}
			},
			Transport: &http.Transport{
				DialContext: g.dialer().DialContext,
				TLSClientConfig: &tls.Config{
					RootCAs:    pool,
					MinVersion: tls.VersionTLS12,
				},
				// A pooled connection was dialled through the guard, so one
				// reused after the guard's answer would have changed is bounded
				// rather than kept indefinitely.
				IdleConnTimeout:   limits.OIDCRequestTimeout,
				ForceAttemptHTTP2: true,
			},
		},
	}, nil
}

// Settings is what the client was configured with, for the surface that
// reports configuration. The secret is deliberately not among them.
func (c *Client) Settings() (issuer, clientID string, scopes []string, allowPrivate bool) {
	return c.cfg.Issuer, c.cfg.ClientID,
		append([]string(nil), c.cfg.Scopes...), c.cfg.AllowPrivateEndpoints
}

// trustAnchors builds the pool, refusing an empty one.
func trustAnchors(caCertFile string) (*x509.CertPool, error) {
	if caCertFile != "" {
		// Cleaned before the read: the path is the operator's own
		// configuration, and a lexically odd spelling should name the same
		// file.
		pem, err := os.ReadFile(filepath.Clean(caCertFile))
		if err != nil {
			return nil, fmt.Errorf("reading the certificate pool: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("%w: %s carries no certificate this build could parse",
				ErrNoTrustAnchors, caCertFile)
		}
		return pool, nil
	}

	pool, err := x509.SystemCertPool()
	if err != nil {
		return nil, fmt.Errorf("%w: the system pool could not be read: %w", ErrNoTrustAnchors, err)
	}
	// A pool with nothing in it is what a runtime image carrying no bundle
	// produces, and it is a startup refusal rather than a discovery arriving
	// at the first sign-in attempt.
	if len(pool.Subjects()) == 0 { //nolint:staticcheck // Subjects is the only way to ask whether a system pool is empty; the deprecation is about mutating one.
		return nil, fmt.Errorf(
			"%w: the image ships no certificate bundle, so a certificate file must be configured",
			ErrNoTrustAnchors)
	}
	return pool, nil
}

// get performs one bounded request.
func (c *Client) get(ctx context.Context, target string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, &ProviderError{Reason: "the request could not be built"}
	}
	req.Header.Set("Accept", "application/json")
	return c.do(req)
}

type basicAuth struct{ User, Pass string }

// postForm performs one bounded form post, optionally authenticated.
func (c *Client) postForm(
	ctx context.Context, target string, form url.Values, basic *basicAuth,
) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, &ProviderError{Reason: "the request could not be built"}
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if basic != nil {
		// The two halves are form-encoded before they are combined, not after.
		// A client secret carrying a plus or a space authenticates only if both
		// ends agree on that.
		req.SetBasicAuth(formEncode(basic.User), formEncode(basic.Pass))
	}
	return c.do(req)
}

// do sends a request and reads a bounded body.
func (c *Client) do(req *http.Request) (body []byte, err error) {
	res, err := c.http.Do(req)
	if err != nil {
		// A refusal from the guard travels out as the transport error it is,
		// and is reported as itself rather than folded into a generic one.
		if errors.Is(err, ErrAddressBlocked) || errors.Is(err, ErrProvider) {
			return nil, err
		}
		return nil, &ProviderError{Reason: "the request failed: " + err.Error()}
	}
	defer func() { err = errors.Join(err, res.Body.Close()) }()

	// Bounded while the body arrives rather than after it is buffered: a bound
	// checked at the end has already allocated what it was meant to refuse.
	// One byte past the ceiling is read, so a body exactly at it stays
	// distinguishable from one over.
	read, rerr := io.ReadAll(io.LimitReader(res.Body, limits.OIDCResponseBytes+1))
	if rerr != nil {
		return nil, &ProviderError{Reason: "the response body did not finish arriving"}
	}
	if len(read) > limits.OIDCResponseBytes {
		return nil, limits.Exceed("oidc response body", limits.OIDCResponseBytes, int64(len(read)))
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return nil, &ProviderError{
			Reason: fmt.Sprintf("answered with status %d", res.StatusCode),
		}
	}
	return read, nil
}

// formEncode is the encoding a form body uses, which is not the encoding a
// path uses: a space becomes a plus, and everything outside the unreserved
// set is escaped.
func formEncode(s string) string {
	const unreserved = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_.~"
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case strings.IndexByte(unreserved, ch) >= 0:
			out = append(out, ch)
		case ch == ' ':
			out = append(out, '+')
		default:
			out = fmt.Appendf(out, "%%%02X", ch)
		}
	}
	return string(out)
}
