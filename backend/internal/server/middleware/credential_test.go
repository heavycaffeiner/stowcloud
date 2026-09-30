// Linux only, matching the package under test.
//go:build linux

package middleware

import (
	"encoding/base64"
	"encoding/hex"
	"testing"
)

func basicHeader(user, pass string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
}

// Basic wins over Bearer. WebDAV and sync libraries routinely leave both
// headers represented, and Basic is the one they actually populate.
func TestBasicPrecedesBearer(t *testing.T) {
	// One header carries one scheme, so the ordering shows in which scheme is
	// recognised when a client sends a header of each kind in turn.
	got := Select(Presented{Authorization: basicHeader("ignored", "app-secret")}, false)
	if got.Kind != CredentialBasicApp {
		t.Fatalf("a Basic header selected %v", got.Kind)
	}
	if string(got.Token) != "app-secret" {
		t.Errorf("the token is %q", got.Token)
	}

	got = Select(Presented{Authorization: "Bearer app-secret"}, false)
	if got.Kind != CredentialBearerApp {
		t.Fatalf("a Bearer header selected %v", got.Kind)
	}
	if string(got.Token) != "app-secret" {
		t.Errorf("the token is %q", got.Token)
	}
}

// A header beats the cookie, so a device credential is not shadowed by a stale
// browser session sharing the connection.
func TestAHeaderPrecedesTheCookie(t *testing.T) {
	cookie := hex.EncodeToString([]byte("session-bytes"))
	for _, header := range []string{basicHeader("u", "app-secret"), "Bearer app-secret"} {
		got := Select(Presented{Authorization: header, Cookie: cookie}, false)
		if got.Kind == CredentialSessionCookie {
			t.Errorf("the cookie won against %q", header)
		}
		if string(got.Token) != "app-secret" {
			t.Errorf("with %q the token is %q", header, got.Token)
		}
	}
}

// The username half of a Basic header is discarded. The token names its own
// account, so honouring the username would let a valid token be aimed at
// another one.
func TestTheBasicUsernameIsIgnored(t *testing.T) {
	a := Select(Presented{Authorization: basicHeader("alice", "app-secret")}, false)
	b := Select(Presented{Authorization: basicHeader("root", "app-secret")}, false)
	if a.Kind != b.Kind || string(a.Token) != string(b.Token) {
		t.Fatalf("the username changed the credential: %v %q against %v %q",
			a.Kind, a.Token, b.Kind, b.Token)
	}
}

// Malformed credentials resolve to none rather than to a token that cannot be
// checked.
func TestMalformedCredentialsResolveToNone(t *testing.T) {
	for _, c := range []struct {
		what string
		p    Presented
	}{
		{"an empty request", Presented{}},
		{"a scheme with no value", Presented{Authorization: "Bearer"}},
		{"a scheme with no space", Presented{Authorization: "Bearerabc"}},
		{"an unknown scheme", Presented{Authorization: "Digest abc"}},
		{"Basic that is not base64", Presented{Authorization: "Basic !!!!"}},
		{"Basic with no colon", Presented{Authorization: "Basic " + base64.StdEncoding.EncodeToString([]byte("nocolon"))}},
		{"Basic with an empty password", Presented{Authorization: basicHeader("alice", "")}},
		{"a cookie that is not hex", Presented{Cookie: "not-hex-zz"}},
		{"an empty cookie", Presented{Cookie: "   "}},
	} {
		if got := Select(c.p, false); got.Kind != CredentialNone {
			t.Errorf("%s selected %v", c.what, got.Kind)
		}
	}
}

// The scheme match is case-insensitive, which is what the HTTP grammar says.
func TestTheSchemeMatchIsCaseInsensitive(t *testing.T) {
	for _, header := range []string{"bearer tok", "BEARER tok", "BeArEr tok"} {
		if got := Select(Presented{Authorization: header}, false); got.Kind != CredentialBearerApp {
			t.Errorf("%q selected %v", header, got.Kind)
		}
	}
}
