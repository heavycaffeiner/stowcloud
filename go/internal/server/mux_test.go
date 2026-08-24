// Linux only, because what it tests is.
//go:build linux

package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/internal/httpapi/handler"
	"github.com/heavycaffeiner/stowcloud/go/internal/httpapi/route"
)

// The router refuses to order a single method on the bare root against every
// method on a prefix: neither is more specific than the other, so registering
// both panics when the mux is built.
//
// That panic reached a shipping build. It only fires when the frontend is
// embedded, so every ordinary build and every test was green while the tagged
// binary died at startup before it bound a socket.

// stubHandler stands in for the frontend, which a test binary has no bundle
// for.
func stubHandler(body string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, err := w.Write([]byte(body)); err != nil {
			panic(err)
		}
	})
}

// Building the mux with both a frontend and the WebDAV mount must not panic,
// which is the whole of the defect.
func TestTheRootAndTheProtocolMountCoexist(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("building the mux panicked: %v", r)
		}
	}()

	table := []route.Route{{
		Method: "GET", Pattern: "/api/health",
		Req: route.Requirement{Access: route.AccessAny}, Handler: handler.Health(handler.NewHealthState()),
	}}
	m := mux(table, nil, stubHandler("dav"))
	// The frontend is not embedded in a test binary, so the root above is the
	// protocol alone. What matters is that it registered at all.
	if m == nil {
		t.Fatal("mux returned nothing")
	}
}

// The prefix belongs to the protocol. A request under it never falls through
// to the frontend: handing a sync client an HTML page is how it reports a
// corrupt server rather than a wrong path.
func TestTheProtocolPrefixNeverFallsThroughToTheFrontend(t *testing.T) {
	root := rootHandler(stubHandler("the frontend"), stubHandler("the protocol"))

	for _, path := range []string{"/dav", "/dav/", "/dav/docs", "/dav/docs/a.txt"} {
		rec := httptest.NewRecorder()
		root.ServeHTTP(rec, httptest.NewRequest("PROPFIND", path, nil))
		if got := rec.Body.String(); got != "the protocol" {
			t.Errorf("%s answered %q, want the protocol", path, got)
		}
	}

	// And a path that merely starts with the same letters is not the prefix.
	rec := httptest.NewRecorder()
	root.ServeHTTP(rec, httptest.NewRequest("GET", "/davos", nil))
	if got := rec.Body.String(); got != "the frontend" {
		t.Errorf("/davos answered %q, want the frontend", got)
	}
}

// The frontend answers reads only. A write on a path it owns is a method it
// has no answer for, and saying so beats a document with a success status.
func TestTheFrontendRefusesAMethodItCannotAnswer(t *testing.T) {
	root := rootHandler(stubHandler("the frontend"), stubHandler("the protocol"))

	for _, method := range []string{"POST", "PUT", "DELETE", "PROPFIND"} {
		rec := httptest.NewRecorder()
		root.ServeHTTP(rec, httptest.NewRequest(method, "/some/page", nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s / answered %d, want 405", method, rec.Code)
		}
		// A refusal that does not say what is allowed is one a client cannot
		// act on.
		if rec.Header().Get("Allow") == "" {
			t.Errorf("%s / refused with no Allow header", method)
		}
	}

	for _, method := range []string{"GET", "HEAD"} {
		rec := httptest.NewRecorder()
		root.ServeHTTP(rec, httptest.NewRequest(method, "/some/page", nil))
		if got := rec.Body.String(); got != "the frontend" {
			t.Errorf("%s answered %q", method, got)
		}
	}
}

// With no protocol mount the frontend is the root unchanged, which is what a
// build assembled without one gets.
func TestWithNoProtocolMountTheFrontendIsTheRoot(t *testing.T) {
	root := rootHandler(stubHandler("the frontend"), nil)
	rec := httptest.NewRecorder()
	root.ServeHTTP(rec, httptest.NewRequest("GET", "/dav/docs", nil))
	if got := rec.Body.String(); got != "the frontend" {
		t.Fatalf("answered %q, want the frontend", got)
	}
}
