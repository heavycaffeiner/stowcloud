package oidc

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// clientAuthMethod picks authNone only for a client the operator declared
// public, and only when the document actually advertises it. Every other
// combination keeps the existing basic/post ladder, so no confidential
// deployment changes behaviour just because a provider happens to list
// "none" among several methods.
func TestClientAuthMethodPicksNoneOnlyForAPublicClientAgainstAnAdvertisingDocument(t *testing.T) {
	advertisingNone := &Discovery{TokenEndpointAuthMethodsSupported: []string{"client_secret_basic", "none"}}
	noneOnly := &Discovery{TokenEndpointAuthMethodsSupported: []string{"none"}}
	basicOnly := &Discovery{TokenEndpointAuthMethodsSupported: []string{"client_secret_basic"}}
	empty := &Discovery{}

	cases := []struct {
		name   string
		doc    *Discovery
		public bool
		want   clientAuth
	}{
		{"public client, document advertises none", advertisingNone, true, authNone},
		{"public client, document advertises only none", noneOnly, true, authNone},
		{"confidential client, document advertises none: none is not chosen", advertisingNone, false, authBasic},
		{"public client, document never advertises none: falls back to basic", basicOnly, true, authBasic},
		{"confidential client, ordinary document", basicOnly, false, authBasic},
		{"public client, empty list: falls back to the default", empty, true, authBasic},
	}
	for _, tc := range cases {
		got, err := clientAuthMethod(tc.doc, tc.public)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A document naming no method this build implements is still refused, public
// client or not: "none" being possible for a public client does not widen
// what an unlisted method means.
func TestClientAuthMethodStillRefusesAnUnusableDocumentForAPublicClient(t *testing.T) {
	doc := &Discovery{TokenEndpointAuthMethodsSupported: []string{"private_key_jwt"}}
	if _, err := clientAuthMethod(doc, true); err == nil {
		t.Fatal("a document naming no implemented method was accepted for a public client")
	}
}

// Exchange under authNone sends client_id in the form body and neither an
// Authorization header nor a client_secret, per RFC 6749 §3.2.1: a client
// that does not authenticate still identifies itself in the body.
func TestExchangeUnderAuthNoneSendsClientIDInBodyAndNoAuthHeader(t *testing.T) {
	ctx := context.Background()
	f, err := NewFlowSecrets()
	if err != nil {
		t.Fatalf("NewFlowSecrets: %v", err)
	}

	var sawAuthHeader bool
	var form url.Values
	c, _ := stubbed(t, fixedClock(), func(req *http.Request) (*http.Response, error) {
		if strings.HasSuffix(req.URL.Path, "/token") {
			if _, _, ok := req.BasicAuth(); ok {
				sawAuthHeader = true
			}
			if req.Header.Get("Authorization") != "" {
				sawAuthHeader = true
			}
			body, rerr := io.ReadAll(req.Body)
			if rerr != nil {
				return nil, rerr
			}
			var perr error
			if form, perr = url.ParseQuery(string(body)); perr != nil {
				return nil, perr
			}
			return response(200, `{"id_token":"a.b.c"}`), nil
		}
		return response(200, discoveryJSON(map[string]any{
			"token_endpoint_auth_methods_supported": []string{"none"},
		})), nil
	})
	c.cfg.PublicClient = true

	token, xerr := c.Exchange(ctx, "the-code", "https://cloud.example.test/callback", f)
	if xerr != nil {
		t.Fatalf("Exchange: %v", xerr)
	}
	if token != "a.b.c" {
		t.Fatalf("the token is %q", token)
	}
	if sawAuthHeader {
		t.Error("authNone sent an Authorization header")
	}
	if form.Get("client_secret") != "" {
		t.Error("authNone sent a client_secret")
	}
	if form.Get("client_id") != testClientID {
		t.Errorf("the form's client_id is %q, want %q", form.Get("client_id"), testClientID)
	}
}
