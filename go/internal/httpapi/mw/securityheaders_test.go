package mw

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The application origin's policy.
//
// It has to be strict enough to be worth having and permissive enough to run
// the interface this server ships. Getting the second part wrong produced a
// blank page whose only symptom was a console line: every request succeeded,
// the document arrived whole, and nothing rendered.

func policyFor(t *testing.T) string {
	t.Helper()
	rec := httptest.NewRecorder()
	SecurityHeaders(AppPolicy("'sha256-test'"))(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).
		ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	return rec.Header().Get("Content-Security-Policy")
}

// The bundle's hashes reach the header. Building the policy correctly and
// then not using it is the same blank page.
func TestTheBundlesHashesReachTheHeader(t *testing.T) {
	if got := policyFor(t); !strings.Contains(got, "'sha256-test'") {
		t.Fatalf("the inline-script hash is not in the policy:\n%s", got)
	}
}

// A build with no frontend still gets a policy, and it is the strict one
// rather than an empty header. Absent must never mean "admit everything".
func TestNoFrontendStillGetsAStrictPolicy(t *testing.T) {
	rec := httptest.NewRecorder()
	SecurityHeaders("")(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).
		ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	got := rec.Header().Get("Content-Security-Policy")
	if got == "" {
		t.Fatal("a build with no frontend was served with no policy at all")
	}
	if !strings.Contains(got, "script-src 'self';") {
		t.Fatalf("the policy is not the strict one:\n%s", got)
	}
}

// The properties that make the policy worth having. Each is what stops a
// stored file, or a page that reflected one, from becoming an execution
// context with a session attached.
func TestTheApplicationPolicyKeepsItsRestrictions(t *testing.T) {
	got := policyFor(t)

	for _, want := range []string{
		"default-src 'self'",
		"object-src 'none'",
		"base-uri 'none'",
		"frame-ancestors 'none'",
		"form-action 'self'",
		"connect-src 'self'",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the policy lost %q:\n%s", want, got)
		}
	}

	// The one that matters most, and the one an inline script is a temptation
	// to add: a hash admits the bytes that were built, and this admits every
	// inline script on every page.
	if strings.Contains(got, "'unsafe-inline'") {
		script := got[strings.Index(got, "script-src"):]
		if end := strings.Index(script, ";"); end >= 0 {
			script = script[:end]
		}
		if strings.Contains(script, "'unsafe-inline'") {
			t.Errorf("script-src admits every inline script:\n%s", script)
		}
	}
	if strings.Contains(got, "'unsafe-eval'") {
		t.Errorf("the policy admits eval:\n%s", got)
	}
}

// The interface inlines its own font, so the face is a data: URL. Without this
// the fallback to default-src refuses it and the page renders in a substitute.
func TestTheApplicationPolicyAdmitsTheInlinedFont(t *testing.T) {
	if got := policyFor(t); !strings.Contains(got, "font-src 'self' data:") {
		t.Errorf("the policy has no font-src admitting data:, so the bundled face is refused:\n%s", got)
	}
}

// The uploader runs in a Worker, and the policy has to admit one.
//
// A worker script is checked against worker-src, and against script-src only
// when worker-src is absent. script-src carries the bundle's inline hash, and
// a hash does not admit a separate script file: the browser refused to start
// the worker, so every upload created its session and then stopped, with the
// refusal visible only in the console.
func TestThePolicyAdmitsTheUploadWorker(t *testing.T) {
	got := AppPolicy("'sha256-abc'")
	if !strings.Contains(got, "worker-src") {
		t.Fatalf("no worker-src, so a worker falls back to script-src and its hash: %s", got)
	}

	// And what it admits is same-origin, not everything.
	for _, forbidden := range []string{"worker-src *", "worker-src 'unsafe-inline'"} {
		if strings.Contains(got, forbidden) {
			t.Errorf("worker-src is too wide: %s", got)
		}
	}
}
