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
	// ErrNoTrustAnchors reports an empty certificate pool at startup.
	//
	// Emptiness rejects rather than falling back to the system pool. A pool
	// verifying nothing effectively verifies everything: all certificates fail,
	// the operator disables verification to make progress, and the back channel
	// carrying the client secret ends up unauthenticated.
	ErrNoTrustAnchors = errors.New("the certificate pool is empty")

	// ErrProvider covers an unreachable provider, an unparseable answer, and a
	// redirect.
	ErrProvider = errors.New("the provider did not answer usably")
)

// ProviderError identifies which of those occurred.
type ProviderError struct{ Reason string }

func (e *ProviderError) Error() string { return "the provider: " + e.Reason }

func (e *ProviderError) Is(target error) bool { return target == ErrProvider }

// Config holds this package's inputs. Parsing and the settings rules belong to
// the layer above, so what arrives here is already validated.
type Config struct {
	Issuer       string
	ClientID     string
	ClientSecret secret.Secret

	// Scopes always ends up carrying the one that makes an identity token be
	// issued at all, because without it the flow produces nothing this server
	// can verify.
	Scopes []string

	// AllowPrivateEndpoints records the operator opting in to a provider sharing
	// this server's network.
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

// New constructs the client or rejects the configuration.
//
// The certificate pool must be supplied explicitly. Adopting the system pool
// unconditionally would let a runtime image without a bundle verify against
// nothing at all, and the single connection made here carries a client
// secret.
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

// Settings returns the client's configuration for the surface that displays it.
// The secret is deliberately excluded.
func (c *Client) Settings() (issuer, clientID string, scopes []string, allowPrivate bool) {
	return c.cfg.Issuer, c.cfg.ClientID,
		append([]string(nil), c.cfg.Scopes...), c.cfg.AllowPrivateEndpoints
}

// trustAnchors assembles the pool and rejects an empty result.
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
	// An empty pool is what a runtime image lacking a bundle yields. Rejecting
	// at startup beats discovering it during the first sign-in attempt.
	if len(pool.Subjects()) == 0 { //nolint:staticcheck // Subjects is the only way to ask whether a system pool is empty; the deprecation is about mutating one.
		return nil, fmt.Errorf(
			"%w: the image ships no certificate bundle, so a certificate file must be configured",
			ErrNoTrustAnchors)
	}
	return pool, nil
}

// get issues a single size-bounded request.
func (c *Client) get(ctx context.Context, target string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, &ProviderError{Reason: "the request could not be built"}
	}
	req.Header.Set("Accept", "application/json")
	return c.do(req)
}

type basicAuth struct{ User, Pass string }

// postForm issues a single size-bounded form post, authenticated when asked.
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
		// Each half is form-encoded before combining rather than afterwards. A
		// client secret containing a plus or a space authenticates only when
		// both ends agree on this ordering.
		req.SetBasicAuth(formEncode(basic.User), formEncode(basic.Pass))
	}
	return c.do(req)
}

// do dispatches a request and reads a size-bounded body.
func (c *Client) do(req *http.Request) (body []byte, err error) {
	res, err := c.http.Do(req)
	if err != nil {
		// A guard rejection propagates as the transport error it already is,
		// surfacing under its own identity instead of a generic wrapper.
		if errors.Is(err, ErrAddressBlocked) || errors.Is(err, ErrProvider) {
			return nil, err
		}
		return nil, &ProviderError{Reason: "the request failed: " + err.Error()}
	}
	defer func() { err = errors.Join(err, res.Body.Close()) }()

	// The limit applies as the body streams in, not once it is buffered. A check
	// performed at the end has already allocated exactly what it intended to
	// reject. Reading one byte beyond the ceiling keeps a body precisely at the
	// limit distinguishable from one exceeding it.
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

// formEncode applies form-body encoding, which differs from path encoding:
// spaces become plus signs and anything outside the unreserved set is
// escaped.
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
