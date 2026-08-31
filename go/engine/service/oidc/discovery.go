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

// The discovery document arrives from outside and is untrusted. Its contents
// name the endpoints this server will send a client secret to, so every field
// is validated before any of it is used.

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

// Discovery covers the portion of the document this package consumes.
//
// Unrecognized content is ignored rather than rejected, since the specification
// permits providers to add fields and most do. None of these fields has an open
// type, so a document cannot introduce a structure this server would then
// traverse.
type Discovery struct {
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	JWKSURI               string `json:"jwks_uri"`

	// Missing and empty carry identical meaning: the provider stated nothing.
	// The specification designates a default method for that case, and an empty
	// list is not valid anyway, so treating them alike loses nothing.
	TokenEndpointAuthMethodsSupported []string `json:"token_endpoint_auth_methods_supported"`
	IDTokenSigningAlgValuesSupported  []string `json:"id_token_signing_alg_values_supported"`
}

// DiscoveryURL gives the document's location for an issuer.
func DiscoveryURL(issuer string) string {
	return strings.TrimSuffix(issuer, "/") + "/.well-known/openid-configuration"
}

// FetchDiscovery retrieves and validates the document.
//
// Invocation is lazy and never occurs at startup. Contacting the provider
// during boot would make the server fail to start whenever the provider is
// down, exchanging the loss of one sign-in method for the loss of the whole
// product.
func (c *Client) FetchDiscovery(ctx context.Context) (*Discovery, error) {
	if doc, ok := c.discovery.fresh(c.clk); ok {
		return doc, nil
	}

	// The configured issuer faces the same rules as any value the document
	// supplies. Exempting it would simply provide a route around those rules.
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

	// Compared for exact equality, not by prefix and not by host. Any weaker
	// test would let a document served by one issuer speak on another's
	// behalf.
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

// checkEndpoint enforces the scheme and address rules on a single URL.
//
// The address portion is verified both here and at dial time, and both are
// required. This point catches a literal address embedded in the document,
// which never passes through a resolver. Dial time catches a hostname, whose
// destination is unknown until resolution.
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
	// URLs bearing credentials are rejected. Such a credential would accompany
	// every request to the provider, and this server never selected it.
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
