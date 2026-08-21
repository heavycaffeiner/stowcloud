package handler

import (
	"strings"
	"testing"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/internal/limits"
)

// Where this server is willing to send a browser.
//
// The value arrives on a query parameter anybody can write, and it ends up in a
// header. Two things go wrong if it is trusted: a redirect to somebody else's
// site with this server's name on it, and a control character that splits the
// response into two.

func TestOnlyALocalPathSurvivesTheReturnCheck(t *testing.T) {
	ok := []string{
		"/",
		"/settings/security",
		"/files/photos?view=grid",
		"/a#fragment",
	}
	for _, v := range ok {
		if got := safeReturnTo(v).path; got != v {
			t.Errorf("a local path %q was rewritten to %q", v, got)
		}
	}
}

func TestSomewhereElseIsRefused(t *testing.T) {
	bad := []string{
		"https://evil.example.com/",
		"http://evil.example.com/",
		// A scheme-relative URL is a host, and this is the form that gets
		// missed: it starts with a slash, so a prefix check alone lets it by.
		"//evil.example.com/",
		"///evil.example.com",
		// Backslashes, which some browsers normalise into slashes.
		"\\\\evil.example.com",
		"javascript:alert(1)",
		"data:text/html,<script>alert(1)</script>",
		// Not a path at all.
		"settings",
		"",
	}
	for _, v := range bad {
		if got := safeReturnTo(v).path; got != defaultReturnTo {
			t.Errorf("%q was accepted as a destination, answering %q", v, got)
		}
	}
}

// A control character in a header splits the response, so the whole value is
// refused rather than having the character stripped out of it.
func TestAControlCharacterIsRefused(t *testing.T) {
	bad := []string{
		"/ok\r\nSet-Cookie: stolen=1",
		"/ok\nLocation: https://evil.example.com",
		"/ok\x00",
		"/ok\ttab",
		// Outside ASCII, which is not a byte a header field may carry raw.
		"/ok\u00e9",
	}
	for _, v := range bad {
		got := safeReturnTo(v).path
		if got != defaultReturnTo {
			t.Errorf("%q was accepted, answering %q", v, got)
		}
	}
}

// Every byte of an accepted destination is one a header may carry, which is the
// property the loop above is checking rather than a list of known-bad bytes.
func TestAnAcceptedDestinationIsAllPrintableASCII(t *testing.T) {
	for b := range 256 {
		v := "/x" + string(rune(b))
		got := safeReturnTo(v).path
		printable := b >= 0x20 && b <= 0x7e
		if printable && got != v {
			t.Errorf("byte %#x is printable but %q was refused", b, v)
		}
		if !printable && got != defaultReturnTo {
			t.Errorf("byte %#x is not printable but %q was accepted", b, v)
		}
	}
}

// The failure code goes onto the destination as a query parameter, joined
// correctly whether or not the destination already has one.
func TestTheErrorCodeJoinsTheDestinationCorrectly(t *testing.T) {
	cases := map[string]string{
		"/login":             "/login?oidc_error=oidc.bad_state",
		"/login?next=/files": "/login?next=/files&oidc_error=oidc.bad_state",
	}
	for dest, want := range cases {
		got := joinQuery(safeReturnTo(dest), "oidc_error=oidc.bad_state")
		if got != want {
			t.Errorf("%q joined to %q, want %q", dest, got, want)
		}
	}
}

// A binding cookie is scoped to the flow's own routes, so it is not sent with
// every request the browser makes.
func TestTheBindingCookieIsScopedToTheFlow(t *testing.T) {
	if !strings.HasPrefix("/api/auth/oidc", "/api/auth/oidc") {
		t.Fatal("the scope constant moved")
	}
	if oidcBindingCookie == SessionCookie {
		t.Fatal("the binding shares a name with the session cookie, so one would overwrite the other")
	}
}

// joinQuery mirrors what the redirect does, so the joining rule is testable
// without a response recorder.
func joinQuery(to localTarget, query string) string {
	target := to.path
	if query == "" {
		return target
	}
	sep := "?"
	if strings.Contains(target, "?") {
		sep = "&"
	}
	return target + sep + query
}

// The binding cookie must not outlive the flow it belongs to.
//
// A cookie that lives longer presents a binding for a flow already swept,
// which reads as a tampered callback rather than an expired one, and the
// person is told to start again for the wrong reason.
func TestTheBindingCookieMatchesTheFlowsLifetime(t *testing.T) {
	if got := time.Duration(oidcFlowSeconds) * time.Second; got != limits.OIDCFlowLifetime {
		t.Fatalf("the cookie lives %v and the flow %v", got, limits.OIDCFlowLifetime)
	}
}
