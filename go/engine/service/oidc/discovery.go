package oidc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// The discovery document is a trust boundary. What comes back names the
// endpoints this server will post a client secret to, so every field is
// validated before any of them is used.

// ErrDiscovery is a document that failed validation.
var ErrDiscovery = errors.New("the discovery document is not usable")

// DiscoveryError names what was wrong and where, which is what an operator
// can act on.
type DiscoveryError struct {
	Field  string
	Reason string
}

func (e *DiscoveryError) Error() string {
	return fmt.Sprintf("the discovery document is not usable: %s: %s", e.Field, e.Reason)
}

func (e *DiscoveryError) Is(target error) bool { return target == ErrDiscovery }

// Discovery is the part of the document this package reads.
//
// Everything else is ignored rather than refused: the specification lets a
// provider add fields and most do. No field here has an open type, so a
// document cannot smuggle in a structure this server then walks.
type Discovery struct {
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	JWKSURI               string `json:"jwks_uri"`

	// Absent and empty mean the same thing, that the provider did not say.
	// The specification makes one method the default in that case and an empty
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
// which trades an outage of one sign-in method for an outage of the product.
func (c *Client) FetchDiscovery(ctx context.Context) (*Discovery, error) {
	if doc, ok := c.discovery.fresh(c.clk); ok {
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
	// A repeated key is refused rather than having the last value win
	// silently: two answers to one question is not a document to act on.
	if derr := decodeStrict(body, &doc); derr != nil {
		return nil, &DiscoveryError{Field: "body", Reason: derr.Error()}
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
		if cerr := c.checkEndpoint(e.field, e.raw); cerr != nil {
			return nil, cerr
		}
	}

	// A provider that signs with nothing this build verifies is refused now
	// rather than through a token that fails to verify later, which is the
	// same outcome reported where nobody can act on it.
	if len(doc.IDTokenSigningAlgValuesSupported) > 0 &&
		!anySupported(doc.IDTokenSigningAlgValuesSupported) {
		return nil, &DiscoveryError{
			Field:  "id_token_signing_alg_values_supported",
			Reason: "names no algorithm this build verifies",
		}
	}
	// The client-authentication method is settled here too, so a provider this
	// deployment cannot talk to is a refusal at discovery rather than at the
	// exchange, where a person is already waiting on a redirect.
	if _, aerr := clientAuthMethod(&doc); aerr != nil {
		return nil, aerr
	}

	c.discovery.store(&doc, c.clk)
	return &doc, nil
}

// checkEndpoint applies the scheme and address rules to one URL.
//
// The address half is checked here as well as at dial time, and needs both.
// Here it catches an address written literally into the document, which never
// reaches a resolver. At dial time it catches a hostname, which is not
// decided until it is resolved.
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
	if gerr := c.guard.allowHost(u.Hostname()); gerr != nil {
		return &DiscoveryError{
			Field:  field,
			Reason: "points at a private, loopback or link-local address",
		}
	}
	return nil
}

// decodeStrict decodes one JSON document and refuses a repeated key or
// trailing content.
//
// The standard decoder takes the last value for a repeated key, which means a
// document can name one issuer for a reader that stops at the first and
// another for one that does not. A document this server acts on has to have
// one answer per question.
func decodeStrict(body []byte, into any) error {
	if err := checkNoDuplicateKeys(json.NewDecoder(bytes.NewReader(body))); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	if err := dec.Decode(into); err != nil {
		return errors.New("is not valid JSON")
	}
	if dec.More() {
		return errors.New("carries more than one document")
	}
	return nil
}

// checkNoDuplicateKeys walks the token stream and refuses an object that
// names one key twice, at any depth.
func checkNoDuplicateKeys(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return errors.New("is not valid JSON")
	}
	return walkJSON(dec, tok)
}

func walkJSON(dec *json.Decoder, tok json.Token) error {
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for {
			keyTok, err := dec.Token()
			if err != nil {
				return errors.New("is not valid JSON")
			}
			if d, isDelim := keyTok.(json.Delim); isDelim && d == '}' {
				return nil
			}
			key, isString := keyTok.(string)
			if !isString {
				return errors.New("is not valid JSON")
			}
			if seen[key] {
				return fmt.Errorf("names %q twice", key)
			}
			seen[key] = true

			valTok, err := dec.Token()
			if err != nil {
				return errors.New("is not valid JSON")
			}
			if werr := walkJSON(dec, valTok); werr != nil {
				return werr
			}
		}
	case '[':
		for {
			elemTok, err := dec.Token()
			if err != nil {
				return errors.New("is not valid JSON")
			}
			if d, isDelim := elemTok.(json.Delim); isDelim && d == ']' {
				return nil
			}
			if werr := walkJSON(dec, elemTok); werr != nil {
				return werr
			}
		}
	default:
		return nil
	}
}
