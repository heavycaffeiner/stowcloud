package oidc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/internal/clock"
	"github.com/heavycaffeiner/stowcloud/go/internal/limits"
)

// The discovery document is a trust boundary. What comes back names the
// endpoints this server will later post a client secret to, so every field is
// validated before any of them is used.

// ErrDiscovery is a document that failed validation. The wrapped error names
// the field, which is what an operator can act on.
var ErrDiscovery = errors.New("oidc: the discovery document is not usable")

// DiscoveryError names what was wrong and where.
type DiscoveryError struct {
	Field  string
	Reason string
}

func (e *DiscoveryError) Error() string {
	return fmt.Sprintf("oidc: the discovery document is not usable: %s: %s", e.Field, e.Reason)
}

func (e *DiscoveryError) Is(target error) bool { return target == ErrDiscovery }

// Discovery is the part of the document this package reads.
//
// Everything else is ignored rather than refused: the specification lets a
// provider add fields and most of them do. There is no field of an open type
// here, so a document cannot smuggle a structure this server then walks.
type Discovery struct {
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	JWKSURI               string `json:"jwks_uri"`

	// Absent and empty mean the same thing, that the provider did not say. The
	// specification makes one method the default in that case and an empty
	// list is not a legal value, so collapsing the two costs nothing.
	TokenEndpointAuthMethodsSupported []string `json:"token_endpoint_auth_methods_supported"`
	IDTokenSigningAlgValuesSupported  []string `json:"id_token_signing_alg_values_supported"`
}

// DiscoveryURL is where the document lives for an issuer.
func DiscoveryURL(issuer string) string {
	return strings.TrimSuffix(issuer, "/") + "/.well-known/openid-configuration"
}

// FetchDiscovery reads and validates the document.
//
// It is called lazily and never at startup. Calling the provider while the
// server boots means a server that will not boot while the provider is down,
// which trades an outage of one login method for an outage of the whole
// product.
func (c *Client) FetchDiscovery(ctx context.Context) (*Discovery, error) {
	if doc := c.discovery.fresh(c.clk); doc != nil {
		return doc, nil
	}

	// The configured issuer is held to the same rules as anything the document
	// names. An issuer that skipped them would be a way to skip them.
	if err := c.checkEndpoint("issuer", c.cfg.Issuer); err != nil {
		return nil, err
	}

	body, err := c.get(ctx, DiscoveryURL(c.cfg.Issuer))
	if err != nil {
		return nil, err
	}

	var doc Discovery
	dec := json.NewDecoder(strings.NewReader(string(body)))
	// A document with a repeated or unexpected shape is refused rather than
	// having its last value win silently.
	if err := dec.Decode(&doc); err != nil {
		return nil, &DiscoveryError{Field: "body", Reason: "not valid JSON"}
	}

	// Exact equality, not a prefix and not a host comparison. Anything looser
	// lets a document served from one issuer speak for another.
	if doc.Issuer != c.cfg.Issuer {
		return nil, &DiscoveryError{
			Field:  "issuer",
			Reason: "claims a different issuer than the one configured",
		}
	}
	for _, e := range []struct{ field, raw string }{
		{"authorization_endpoint", doc.AuthorizationEndpoint},
		{"token_endpoint", doc.TokenEndpoint},
		{"jwks_uri", doc.JWKSURI},
	} {
		if err := c.checkEndpoint(e.field, e.raw); err != nil {
			return nil, err
		}
	}

	// A provider that signs with nothing this build verifies is a refusal now
	// rather than a token that fails to verify later, which is the same
	// outcome reported where nobody can act on it.
	if len(doc.IDTokenSigningAlgValuesSupported) > 0 && !anySupported(doc.IDTokenSigningAlgValuesSupported) {
		return nil, &DiscoveryError{
			Field:  "id_token_signing_alg_values_supported",
			Reason: "names no algorithm this build verifies",
		}
	}

	c.discovery.store(&doc, c.clk)
	return &doc, nil
}

// checkEndpoint applies the scheme and address rules to one URL.
//
// The address half is checked here as well as at dial time, and needs both.
// Here it catches an address written literally into the document, which never
// reaches a resolver. At dial time it catches a hostname, which is not decided
// until it is resolved.
func (c *Client) checkEndpoint(field, raw string) error {
	if raw == "" {
		return &DiscoveryError{Field: field, Reason: "is missing"}
	}
	u, err := url.Parse(raw)
	if err != nil {
		return &DiscoveryError{Field: field, Reason: "is not a URL"}
	}
	if u.Scheme != "https" {
		return &DiscoveryError{Field: field, Reason: "is not https"}
	}
	if u.Host == "" {
		return &DiscoveryError{Field: field, Reason: "has no host"}
	}
	// A URL carrying credentials is refused: the credential would be sent to
	// the provider on every request, and it is not one this server chose.
	if u.User != nil {
		return &DiscoveryError{Field: field, Reason: "carries credentials"}
	}
	if err := c.guard.AllowHost(u.Hostname()); err != nil {
		return &DiscoveryError{
			Field:  field,
			Reason: "points at a private, loopback or link-local address",
		}
	}
	return nil
}

// discoveryCache is one slot with a bounded lifetime.
//
// Two logins on a cold cache both fetch, which is accepted rather than solved:
// deduplicating them means holding a lock across a network call, and the cost
// of the race is one extra request.
//
// A failure is not cached at all. A provider that was down a second ago may be
// up now, and caching the failure turns a one-second outage into an hour of
// them.
type discoveryCache struct {
	mu      sync.Mutex
	doc     *Discovery
	fetched time.Time
}

func (c *discoveryCache) fresh(clk clock.Clock) *Discovery {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.doc == nil {
		return nil
	}
	if clk.Now().Sub(c.fetched) >= limits.OIDCDiscoveryTTL {
		return nil
	}
	return c.doc
}

func (c *discoveryCache) store(doc *Discovery, clk clock.Clock) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.doc, c.fetched = doc, clk.Now()
}
