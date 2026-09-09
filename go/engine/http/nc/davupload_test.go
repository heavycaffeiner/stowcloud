//go:build linux && compat_nc

package nc

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// A deployment without a resumable upload engine has no chunked collection.
// The refusal has to name that rather than blame the method: one client
// ignores the capability that says so and tries anyway, and the person
// holding it reads whatever came back.
func TestTheUploadCollectionSaysWhenItIsAbsent(t *testing.T) {
	t.Parallel()
	s := New(Deps{Features: func() Features { return Features{} }})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("MKCOL", "/remote.php/dav/uploads/alice/session", nil)
	s.davUpload(rec, req, Principal{UserID: 1}, Target{Kind: KindUploads, Session: "session"})

	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("answered %d, want 501", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/xml; charset=utf-8" {
		t.Errorf("the content type is %q", got)
	}
}
