package oidc

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"

	"github.com/heavycaffeiner/stowcloud/go/internal/clock"
	"github.com/heavycaffeiner/stowcloud/go/internal/limits"
	"github.com/heavycaffeiner/stowcloud/go/internal/secret"
)

// ErrNoTrustAnchors is an empty certificate pool at startup.
//
// An empty pool is a refusal rather than a fallback to the system's own,
// because a pool that verifies nothing verifies everything: every certificate
// fails, the operator turns verification off to get past it, and the back
// channel that carries the client secret is then unauthenticated.
var ErrNoTrustAnchors = errors.New("oidc: the certificate pool is empty")

// ErrProvider is the provider being unreachable, answering something
// unparseable, or redirecting.
var ErrProvider = errors.New("oidc: the provider did not answer usably")

// ProviderError says which of those it was.
type ProviderError struct{ Reason string }

func (e *ProviderError) Error() string { return "oidc: " + e.Reason }

func (e *ProviderError) Is(target error) bool { return target == ErrProvider }

// Config is what this package is told. The server owns parsing and the
// settings rules; what arrives here is already the validated form.
type Config struct {
	Issuer       string
	ClientID     string
	ClientSecret secret.Secret

	// Scopes always ends up containing the one that makes an identity token be
	// issued at all, because without it the whole flow produces nothing.
	Scopes []string

	// AllowPrivateEndpoints is the operator's opt-in for a provider on the
	// same network as this server.
	AllowPrivateEndpoints bool

	// CACertFile overrides the trust anchors with a file of them. Empty means
	// the system's pool.
	CACertFile string
}

// Client is the relying party.
type Client struct {
	cfg   Config
	guard Guard
	http  *http.Client
	clk   clock.Clock

	discovery discoveryCache
	jwks      jwksCache
}

// New builds the client, or refuses.
//
// The certificate pool is explicit. Taking the system's unconditionally would
// mean a runtime image that ships no bundle silently verifies against nothing,
// and the one connection this makes carries a client secret.
func New(cfg Config, clk clock.Clock) (*Client, error) {
	pool, err := trustAnchors(cfg.CACertFile)
	if err != nil {
		return nil, err
	}

	guard := Guard{AllowPrivate: cfg.AllowPrivateEndpoints}
	c := &Client{
		cfg:   cfg,
		guard: guard,
		clk:   clk,
		http: &http.Client{
			Timeout: limits.OIDCRequestTimeout,
			// A redirect is a refusal. The token endpoint is where discovery
			// said it is, and a redirect is a final URL nobody checked: the
			// address guard ran against the address that was named, and a
			// followed redirect names another.
			CheckRedirect: func(req *http.Request, _ []*http.Request) error {
				return &ProviderError{
					Reason: "the provider answered with a redirect to " + req.URL.Redacted() +
						", and the back channel does not follow one",
				}
			},
			Transport: &http.Transport{
				DialContext: guard.Dialer().DialContext,
				TLSClientConfig: &tls.Config{
					RootCAs:    pool,
					MinVersion: tls.VersionTLS12,
				},
				// The pooled connection is dialled through the guard, so a
				// connection reused after the guard's answer would have
				// changed is bounded rather than kept.
				IdleConnTimeout:   limits.OIDCRequestTimeout,
				DisableKeepAlives: false,
				ForceAttemptHTTP2: true,
			},
		},
	}
	return c, nil
}

// trustAnchors builds the pool, refusing an empty one.
func trustAnchors(caCertFile string) (*x509.CertPool, error) {
	if caCertFile != "" {
		pem, err := os.ReadFile(caCertFile) //nolint:gosec // G304 reads the variable: the path is the operator's own configuration, never request input.
		if err != nil {
			return nil, fmt.Errorf("oidc: reading the certificate pool: %w", err)
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
	// produces, and it is a startup refusal rather than a discovery that
	// arrives at the first login attempt.
	if len(pool.Subjects()) == 0 { //nolint:staticcheck // Subjects is the only way to ask whether a system pool is empty; the deprecation is about mutating one.
		return nil, fmt.Errorf("%w: the image ships no certificate bundle, so configure oidc.ca_cert_file",
			ErrNoTrustAnchors)
	}
	return pool, nil
}

// AllowHost applies the address rule to a host that may be a literal address.
//
// A hostname passes here and is judged at dial time instead, because it is not
// decided until it is resolved. This catches the literal, which never reaches
// a resolver at all.
func (g Guard) AllowHost(host string) error {
	ip, err := netip.ParseAddr(strings.Trim(host, "[]"))
	if err != nil {
		return nil
	}
	if g.AllowPrivate {
		return nil
	}
	if IsBlocked(ip) {
		return &BlockedAddressError{Addr: host}
	}
	return nil
}

// get performs one bounded request.
func (c *Client) get(ctx context.Context, target string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, &ProviderError{Reason: "could not build the request"}
	}
	req.Header.Set("Accept", "application/json")
	return c.do(req)
}

// postForm performs one bounded form post, optionally authenticated.
func (c *Client) postForm(ctx context.Context, target string, form url.Values, basic *basicAuth) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, &ProviderError{Reason: "could not build the request"}
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if basic != nil {
		// The two halves are form-encoded before they are combined, not after.
		// A client secret carrying a plus or a space authenticates only if
		// both ends agree on that.
		req.SetBasicAuth(formEncode(basic.User), formEncode(basic.Pass))
	}
	return c.do(req)
}

type basicAuth struct{ User, Pass string }

// do sends a request and reads a bounded body.
func (c *Client) do(req *http.Request) (b []byte, err error) {
	res, err := c.http.Do(req)
	if err != nil {
		// A refusal from the guard travels out as the transport error it is,
		// and is reported as itself rather than folded into a generic one.
		if errors.Is(err, ErrAddressBlocked) {
			return nil, err
		}
		if errors.Is(err, ErrProvider) {
			return nil, err
		}
		return nil, &ProviderError{Reason: "the request failed: " + err.Error()}
	}
	defer func() { err = errors.Join(err, res.Body.Close()) }()

	// Bounded while the body arrives rather than after it is buffered. A bound
	// checked at the end has already allocated what it was meant to refuse.
	// One byte past the ceiling is read so that a body exactly at it is
	// distinguishable from one over.
	body, rerr := io.ReadAll(io.LimitReader(res.Body, limits.OIDCResponseBytes+1))
	if rerr != nil {
		return nil, &ProviderError{Reason: "the response body did not finish arriving"}
	}
	if len(body) > limits.OIDCResponseBytes {
		return nil, limits.Exceed("oidc response body", limits.OIDCResponseBytes, int64(len(body)))
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return nil, &ProviderError{Reason: fmt.Sprintf("the provider answered with status %d", res.StatusCode)}
	}
	return body, nil
}

// formEncode is the encoding a form body uses, which is not the encoding a
// path uses: a space becomes a plus, and everything outside the unreserved set
// is escaped.
func formEncode(s string) string {
	const unreserved = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_.~"
	out := make([]byte, 0, len(s))
	for i := range len(s) {
		ch := s[i]
		switch {
		case strings.IndexByte(unreserved, ch) >= 0:
			out = append(out, ch)
		case ch == ' ':
			out = append(out, '+')
		default:
			out = append(out, fmt.Sprintf("%%%02X", ch)...)
		}
	}
	return string(out)
}
