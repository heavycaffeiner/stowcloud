// Linux only, because what it tests is.
//go:build linux

package httpapi

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
)

// TestChainOrder asserts the request-phase order against recording stubs.
// Each step is replaced by a stub that records its name and calls on, and the
// composition is the same table Chain composes, so a reorder of steps() is a
// failure here before it can cost an Argon2 invocation in production.
//
// The order is a cost argument, not a taste: RateLimit sits before Auth so a
// flood is refused before it costs a KDF, BodyLimit before the handler reads,
// and AuditSink wraps ErrorMapper so the line it writes carries the status the
// mapper chose.
func TestChainOrder(t *testing.T) {
	want := []string{
		"RequestID", "TrustedProxy", "HostGuard", "SecurityHeaders",
		"RateLimit", "BodyLimit", "Auth", "CSRF", "ACLScope",
		"AuditSink", "ErrorMapper", "Handler",
	}

	var got []string
	var h http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, "Handler")
	})
	st := steps()
	for i := len(st) - 1; i >= 0; i-- {
		name := st[i].Name
		inner := h
		h = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got = append(got, name)
			inner.ServeHTTP(w, r)
		})
	}

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/api/fs/list", nil))

	if !slices.Equal(got, want) {
		t.Fatalf("chain order = %v, want %v", got, want)
	}
}
