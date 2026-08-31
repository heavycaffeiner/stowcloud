// Linux only, matching the package under test.
//go:build linux

package middleware

import (
	"encoding/base64"
	"encoding/hex"
	"strings"
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

// The public-read case attempts only the cookie: a signed-in browser sees
// personalised state, and a stale header does not turn a public page into an
// auth failure.
func TestThePublicReadCaseAttemptsOnlyTheCookie(t *testing.T) {
	cookie := hex.EncodeToString([]byte("session-bytes"))

	got := Select(Presented{Authorization: basicHeader("u", "stale"), Cookie: cookie}, true)
	if got.Kind != CredentialSessionCookie {
		t.Fatalf("a public read selected %v, want the cookie", got.Kind)
	}

	// A stale header with no cookie resolves to nothing rather than to a
	// credential that will fail.
	got = Select(Presented{Authorization: basicHeader("u", "stale")}, true)
	if got.Kind != CredentialNone {
		t.Fatalf("a public read with only a header selected %v", got.Kind)
	}
}

// The cookie is hex, decoded to the bytes the store hashes. Hashing the
// printable form would make the spelling part of the secret.
func TestTheCookieIsDecodedNotHashedAsText(t *testing.T) {
	raw := []byte{0x00, 0xff, 0x10, 0x42}
	got := Select(Presented{Cookie: hex.EncodeToString(raw)}, false)
	if got.Kind != CredentialSessionCookie {
		t.Fatalf("a hex cookie selected %v", got.Kind)
	}
	if string(got.Token) != string(raw) {
		t.Errorf("the token is %x, want %x", got.Token, raw)
	}

	// The same bytes in upper case decode to the same secret.
	up := Select(Presented{Cookie: strings.ToUpper(hex.EncodeToString(raw))}, false)
	if string(up.Token) != string(raw) {
		t.Errorf("an upper-case cookie decoded to %x", up.Token)
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

// A declaration with no problems is accepted.
func TestAWellFormedDeclarationIsAccepted(t *testing.T) {
	if err := ValidateProtocolPaths(ProtocolPaths{
		FilePrefixes:    []string{"/dav", "/remote.php/dav"},
		PublicReads:     []MethodPath{{"GET", "/s/{token}"}, {"OPTIONS", "/status"}},
		CredentialFlows: []MethodPath{{"POST", "/login/v2/poll"}},
	}); err != nil {
		t.Fatalf("a valid declaration: %v", err)
	}
}

// The three sets must not overlap. A path claimed twice has a credential
// requirement that depends on which check runs first.
func TestTheThreeSetsMustBeDisjoint(t *testing.T) {
	for _, c := range []struct {
		what  string
		paths ProtocolPaths
		want  string
	}{
		{
			"a public read that is also a credential flow",
			ProtocolPaths{
				PublicReads:     []MethodPath{{"GET", "/shared"}},
				CredentialFlows: []MethodPath{{"POST", "/shared"}},
			},
			"both a public read and a credential flow",
		},
		{
			"a public read under a file prefix",
			ProtocolPaths{
				FilePrefixes: []string{"/dav"},
				PublicReads:  []MethodPath{{"GET", "/dav/public"}},
			},
			"under the file prefix",
		},
		{
			"a credential flow under a file prefix",
			ProtocolPaths{
				FilePrefixes:    []string{"/dav"},
				CredentialFlows: []MethodPath{{"POST", "/dav/flow"}},
			},
			"under the file prefix",
		},
	} {
		err := ValidateProtocolPaths(c.paths)
		if err == nil {
			t.Errorf("%s was accepted", c.what)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s reported %q, which does not mention %q", c.what, err, c.want)
		}
	}
}

// A public read may not change state, and a credential flow must be a POST.
func TestTheMethodRulesAreEnforced(t *testing.T) {
	err := ValidateProtocolPaths(ProtocolPaths{
		PublicReads: []MethodPath{{"POST", "/anonymous-write"}},
	})
	if err == nil || !strings.Contains(err.Error(), "changes state") {
		t.Errorf("an unauthenticated mutation labelled a public read: %v", err)
	}

	err = ValidateProtocolPaths(ProtocolPaths{
		CredentialFlows: []MethodPath{{"GET", "/login/v2/poll"}},
	})
	if err == nil || !strings.Contains(err.Error(), "not a POST") {
		t.Errorf("a GET credential flow: %v", err)
	}

	// The safe verbs pass, including OPTIONS for protocol discovery.
	for _, m := range []string{"GET", "HEAD", "OPTIONS"} {
		if verr := ValidateProtocolPaths(ProtocolPaths{
			PublicReads: []MethodPath{{m, "/status"}},
		}); verr != nil {
			t.Errorf("%s as a public read: %v", m, verr)
		}
	}
}

// A prefix of "/" would make every path a challenge mount, which is how a
// whole application ends up answering a Basic challenge instead of its own
// sign-in page.
func TestAWholeTreeFilePrefixIsRefused(t *testing.T) {
	err := ValidateProtocolPaths(ProtocolPaths{FilePrefixes: []string{"/"}})
	if err == nil || !strings.Contains(err.Error(), "every path") {
		t.Errorf("the root file prefix: %v", err)
	}
	err = ValidateProtocolPaths(ProtocolPaths{FilePrefixes: []string{"dav"}})
	if err == nil || !strings.Contains(err.Error(), "does not begin with /") {
		t.Errorf("a relative file prefix: %v", err)
	}
}

// Prefix matching is component-wise, so a neighbouring mount is not pulled
// into another protocol's challenge behaviour.
func TestFilePrefixMatchingIsComponentWise(t *testing.T) {
	prefixes := []string{"/dav"}
	for _, c := range []struct {
		path string
		want bool
	}{
		{"/dav", true},
		{"/dav/", true},
		{"/dav/files/alice", true},
		{"/DAV/files", true},
		{"/dav2", false},
		{"/davos/files", false},
		{"/other", false},
		{"/", false},
	} {
		if got := UnderFilePrefix(c.path, prefixes); got != c.want {
			t.Errorf("UnderFilePrefix(%q) = %v", c.path, got)
		}
	}
}

// Every problem at once, since a mount is declared once at startup.
func TestADeclarationReportsEveryProblem(t *testing.T) {
	err := ValidateProtocolPaths(ProtocolPaths{
		FilePrefixes:    []string{"dav", "/x", "/x"},
		PublicReads:     []MethodPath{{"DELETE", "/wipe"}},
		CredentialFlows: []MethodPath{{"GET", "relative"}},
	})
	if err == nil {
		t.Fatal("a declaration with five problems was accepted")
	}
	msg := err.Error()
	for _, want := range []string{
		"does not begin with /", "declared twice", "changes state", "not a POST",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("the report omits %q:\n  %s", want, msg)
		}
	}
}
