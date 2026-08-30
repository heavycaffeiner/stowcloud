//go:build linux

package lifecycle_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// serve dispatches one method the way a mount would.
func (f *fixture) serve(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	f.h.ServeMethod(w, request(method, "/files/"+path, body, nil), f.resolve(t, path))
	return w
}

// OPTIONS reports what this deployment implements.
func TestOptionsReportsCompliance(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.write(t, "a.txt", "contents")

	w := f.serve(t, "OPTIONS", "a.txt", "")

	if w.Code != http.StatusOK {
		t.Fatalf("answered %d, want 200", w.Code)
	}
	// Class 2 is locking, and this fixture has a lock table.
	if got := w.Header().Get("DAV"); got != "1, 2" {
		t.Errorf("the DAV header is %q, want 1, 2", got)
	}
	if got := w.Header().Get("MS-Author-Via"); got != "DAV" {
		t.Errorf("MS-Author-Via is %q", got)
	}
	if w.Header().Get("Allow") == "" {
		t.Error("OPTIONS named no methods")
	}
}

// The Allow header describes the resource in front of it, not a fixed list.
//
// A client reads it to decide what to send, so offering PUT on a collection
// invites a request that has to be refused.
func TestAllowDescribesTheResource(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.write(t, "a.txt", "contents")
	f.mkdir(t, "Docs")

	file := f.serve(t, "OPTIONS", "a.txt", "").Header().Get("Allow")
	if !strings.Contains(file, "PUT") {
		t.Errorf("a file does not offer PUT: %q", file)
	}
	if strings.Contains(file, "MKCOL") {
		t.Errorf("a file offers MKCOL: %q", file)
	}

	dir := f.serve(t, "OPTIONS", "Docs", "").Header().Get("Allow")
	if !strings.Contains(dir, "MKCOL") {
		t.Errorf("a collection does not offer MKCOL: %q", dir)
	}
	if strings.Contains(dir, "PUT") {
		t.Errorf("a collection offers PUT: %q", dir)
	}
}

// Every method the dispatcher routes reaches its handler.
func TestServeMethodRoutesEachMethod(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		method string
		path   string
		body   string
		want   int
	}{
		{"GET", "a.txt", "", http.StatusOK},
		{"HEAD", "a.txt", "", http.StatusOK},
		{"OPTIONS", "a.txt", "", http.StatusOK},
		{"PUT", "new.txt", "written", http.StatusCreated},
		{"MKCOL", "New", "", http.StatusCreated},
		{"DELETE", "doomed.txt", "", http.StatusNoContent},
		{"PROPFIND", "a.txt", allprop, http.StatusMultiStatus},
		{"PROPPATCH", "a.txt", setOne, http.StatusMultiStatus},
		{"LOCK", "a.txt", lockBody, http.StatusOK},
	} {
		t.Run(c.method, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			f.write(t, "a.txt", "contents")
			f.write(t, "doomed.txt", "gone soon")

			if got := f.serve(t, c.method, c.path, c.body); got.Code != c.want {
				t.Errorf("%s answered %d, want %d: %s",
					c.method, got.Code, c.want, got.Body.String())
			}
		})
	}
}

// A method this server does not implement is refused with a header naming what
// would work.
func TestAnUnknownMethodIsRefusedWithAnAllowHeader(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.write(t, "a.txt", "contents")

	w := f.serve(t, "BREW", "a.txt", "")

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("answered %d, want 405", w.Code)
	}
	if got := w.Header().Get("Allow"); !strings.Contains(got, "GET") {
		t.Errorf("the refusal names no usable method: %q", got)
	}
}

// A GET of a collection is refused, and the header beside the refusal
// describes that collection rather than a generic resource.
func TestARefusalNamesWhatTheResourceAccepts(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.mkdir(t, "Docs")

	w := f.serve(t, "GET", "Docs", "")

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("answered %d, want 405", w.Code)
	}
	got := w.Header().Get("Allow")
	if !strings.Contains(got, "PROPFIND") {
		t.Errorf("the refusal does not offer PROPFIND: %q", got)
	}
	if strings.Contains(got, "PUT") {
		t.Errorf("the refusal offers PUT on a collection: %q", got)
	}
}
