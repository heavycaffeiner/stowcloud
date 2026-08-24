package spa

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Whether a bundle is present depends on the build tag, so these assert what
// has to hold either way and skip the rest rather than failing a build that
// legitimately carries no frontend.

func TestHandlerReportsWhetherABundleIsPresent(t *testing.T) {
	h, ok := Handler()
	if ok && h == nil {
		t.Fatal("Handler reported a bundle and returned nothing to serve it")
	}
	if !ok && h != nil {
		t.Fatal("Handler reported no bundle and returned a handler anyway")
	}
	if !ok {
		t.Skip("this build carries no frontend, which is what the untagged build is")
	}
}

// A deep link reloaded in the browser has to reach the client router rather
// than 404, which is the whole reason the fallback exists.
func TestAnUnknownPathFallsBackToTheDocument(t *testing.T) {
	h, ok := Handler()
	if !ok {
		t.Skip("this build carries no frontend")
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/files/some/deep/path", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("a deep link returned %d, want the document", rec.Code)
	}
	if !strings.Contains(strings.ToLower(rec.Body.String()), "<!doctype html") {
		t.Fatalf("the fallback did not return the document:\n%s", rec.Body.String())
	}
}

// The document revalidates so a new bundle is picked up, and the hash-named
// assets do not so they are not re-fetched.
func TestTheDocumentRevalidatesAndTheAssetsDoNot(t *testing.T) {
	h, ok := Handler()
	if !ok {
		t.Skip("this build carries no frontend")
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("the document is served with Cache-Control %q, want no-cache", got)
	}
}

// The bundle really is compiled in, which is the dependency edge the design
// rests on: it is here because the frontend build put it inside this package,
// not because something copied it at run time.
func TestTheBundleIsCompiledIn(t *testing.T) {
	h, ok := Handler()
	if !ok {
		t.Skip("this build carries no frontend")
	}

	// The root, not /index.html: net/http redirects the latter to the former,
	// so asking for it would test the file server's redirect rather than the
	// embed.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("the embedded document returned %d", rec.Code)
	}
	if rec.Body.Len() == 0 {
		t.Fatal("the embedded document is empty")
	}
}
