//go:build linux

package dav

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/internal/core"
	"github.com/heavycaffeiner/stowcloud/go/internal/limits"
)

// The upload collection. The engine behind it is proved in its own package, so
// what is proved here is the protocol shape: which method means what, and that
// a member name that could reorder the assembly is refused.

type fakeUploads struct {
	opened    []string
	chunks    map[uint32]string
	assembled bool
	total     uint64
	discarded bool
	err       error
}

func newFakeUploads() *fakeUploads {
	return &fakeUploads{chunks: map[uint32]string{}}
}

func (f *fakeUploads) Open(_ context.Context, _ core.Resolved, name string, _ *uint64) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	f.opened = append(f.opened, name)
	return name, nil
}

func (f *fakeUploads) PutChunk(_ context.Context, _ core.Resolved, _ string, _ core.UserID, name uint32, body io.Reader) error {
	if f.err != nil {
		return f.err
	}
	b, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	f.chunks[name] = string(b)
	return nil
}

func (f *fakeUploads) Assemble(_ context.Context, _ core.Resolved, _ string, total uint64, _ *int64) (core.Entry, error) {
	if f.err != nil {
		return core.Entry{}, f.err
	}
	f.assembled, f.total = true, total
	return core.Entry{Name: "out.bin", ETag: "abc"}, nil
}

func (f *fakeUploads) Discard(context.Context, string, core.UserID) error {
	f.discarded = true
	return f.err
}

func (f *fakeUploads) Chunks(context.Context, string, core.UserID) ([]uint32, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := make([]uint32, 0, len(f.chunks))
	for n := range f.chunks {
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

func withUploads(t *testing.T, up UploadCollection) *fixture {
	t.Helper()
	f := newFixture(t)
	f.h = New(Options{
		Core: f.core, State: f.store.State(),
		Locks: NewLocks(f.store.State(), f.clk), Uploads: up,
	})
	return f
}

func (f *fixture) upload(t *testing.T, method, sub, body string, header http.Header) *httptest.ResponseRecorder {
	t.Helper()
	up, err := ParseUploadPath(sub)
	if err != nil {
		t.Fatalf("ParseUploadPath(%q): %v", sub, err)
	}
	r := httptest.NewRequest(method, "/dav-uploads/"+strings.TrimPrefix(sub, "/"),
		strings.NewReader(body))
	for k, vs := range header {
		for _, v := range vs {
			r.Header.Add(k, v)
		}
	}
	rec := httptest.NewRecorder()
	f.h.ServeUpload(rec, r, f.resolve(t, "/out.bin"), up)
	return rec
}

// The member name is what decides assembly order, so it has to be a plain
// decimal. Anything else could assemble in an order the client did not intend.
func TestAnUploadMemberNameMustBeAPlainDecimal(t *testing.T) {
	for _, good := range []string{"s/0", "s/1", "s/42", "s/4294967295"} {
		if _, err := ParseUploadPath(good); err != nil {
			t.Errorf("ParseUploadPath(%q) = %v, want it accepted", good, err)
		}
	}
	for _, bad := range []string{
		"s/-1", "s/+1", "s/1.0", "s/0x10", "s/ 1", "s/1 ",
		"s/01a", "s/abc", "s/", "s/4294967296",
		"s/a/b", // too deep to address anything
	} {
		if _, err := ParseUploadPath(bad); err == nil {
			t.Errorf("ParseUploadPath(%q) was accepted", bad)
		}
	}
}

func TestALeadingZeroNameDoesNotAliasAnother(t *testing.T) {
	// "007" and "7" would be the same chunk if the name were parsed loosely,
	// and the second one written would silently replace the first.
	a, err := ParseUploadPath("s/7")
	if err != nil {
		t.Fatalf("ParseUploadPath: %v", err)
	}
	b, err := ParseUploadPath("s/007")
	if err != nil {
		// Refusing it outright is also correct, and is what this build does.
		return
	}
	if a.Member == b.Member {
		t.Fatal("a zero-padded name aliased a plain one, so one chunk would overwrite the other")
	}
}

func TestMkcolOpensASessionAndPutContributesChunks(t *testing.T) {
	up := newFakeUploads()
	f := withUploads(t, up)

	if rec := f.upload(t, "MKCOL", "sess", "", nil); rec.Code != http.StatusCreated {
		t.Fatalf("MKCOL returned %d, want 201", rec.Code)
	}
	if len(up.opened) != 1 || up.opened[0] != "sess" {
		t.Fatalf("the session was not opened: %v", up.opened)
	}

	for _, c := range []struct {
		name, body string
	}{{"sess/1", "abc"}, {"sess/2", "def"}} {
		if rec := f.upload(t, "PUT", c.name, c.body, nil); rec.Code != http.StatusCreated {
			t.Fatalf("PUT %s returned %d, want 201", c.name, rec.Code)
		}
	}
	if up.chunks[1] != "abc" || up.chunks[2] != "def" {
		t.Fatalf("the chunks did not arrive: %v", up.chunks)
	}
}

func TestMoveAssemblesWithTheDeclaredLength(t *testing.T) {
	up := newFakeUploads()
	f := withUploads(t, up)

	rec := f.upload(t, "MOVE", "sess", "", http.Header{"OC-Total-Length": {"6"}})
	if rec.Code != http.StatusCreated {
		t.Fatalf("MOVE returned %d, want 201\n%s", rec.Code, rec.Body)
	}
	if !up.assembled || up.total != 6 {
		t.Fatalf("assembled = %v, total = %d, want the declared length", up.assembled, up.total)
	}
	if rec.Header().Get("ETag") == "" {
		t.Fatal("the assembled entry's validator was not returned")
	}
}

// Without a declared length there is nothing to check the assembly against, so
// the request is refused rather than assembled to whatever arrived.
func TestMoveWithoutADeclaredLengthIsRefused(t *testing.T) {
	up := newFakeUploads()
	f := withUploads(t, up)

	rec := f.upload(t, "MOVE", "sess", "", nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("MOVE with no length returned %d, want 400", rec.Code)
	}
	if up.assembled {
		t.Fatal("the collection was assembled without a declared length")
	}
}

func TestPropfindReportsTheMembersHeld(t *testing.T) {
	up := newFakeUploads()
	up.chunks[1] = "a"
	up.chunks[5] = "b"
	f := withUploads(t, up)

	rec := f.upload(t, "PROPFIND", "sess", "", nil)
	if rec.Code != http.StatusMultiStatus {
		t.Fatalf("PROPFIND returned %d, want 207\n%s", rec.Code, rec.Body)
	}
	doc := rec.Body.String()
	for _, want := range []string{"/1", "/5"} {
		if !strings.Contains(doc, want) {
			t.Fatalf("the held member %s is not reported\n%s", want, doc)
		}
	}
}

func TestDeleteDiscardsTheSession(t *testing.T) {
	up := newFakeUploads()
	f := withUploads(t, up)
	if rec := f.upload(t, "DELETE", "sess", "", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE returned %d, want 204", rec.Code)
	}
	if !up.discarded {
		t.Fatal("the session was not discarded")
	}
}

// A single member is not individually removable: the assembly cursor has
// already consumed the ones before it.
func TestDeletingOneMemberIsRefused(t *testing.T) {
	up := newFakeUploads()
	f := withUploads(t, up)
	if rec := f.upload(t, "DELETE", "sess/1", "", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("deleting one member returned %d, want 400", rec.Code)
	}
	if up.discarded {
		t.Fatal("deleting one member discarded the whole session")
	}
}

func TestTheCollectionIsAbsentWithoutAnEngine(t *testing.T) {
	f := newFixture(t)
	up, err := ParseUploadPath("sess")
	if err != nil {
		t.Fatalf("ParseUploadPath: %v", err)
	}
	r := httptest.NewRequest("MKCOL", "/dav-uploads/sess", nil)
	rec := httptest.NewRecorder()
	f.h.ServeUpload(rec, r, f.resolve(t, "/out.bin"), up)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405 with no engine wired up", rec.Code)
	}
}

// An engine refusal reaches the client as a status rather than as a success.
func TestAnEngineRefusalIsReportedNotSwallowed(t *testing.T) {
	up := newFakeUploads()
	up.err = limits.Exceed("upload sessions per user", limits.UploadSessionsPerUser,
		limits.UploadSessionsPerUser+1)
	f := withUploads(t, up)

	rec := f.upload(t, "MKCOL", "sess", "", nil)
	if rec.Code != http.StatusInsufficientStorage {
		t.Fatalf("status = %d, want 507 for an exhausted bound", rec.Code)
	}
}

func TestStatusOfMapsTheErrorsThatReachAClient(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want int
	}{
		{"a DTD", ErrDTDForbidden, http.StatusBadRequest},
		{"a processing instruction", ErrPIForbidden, http.StatusBadRequest},
		{"malformed xml", ErrBadXML, http.StatusBadRequest},
		{"a malformed header", ErrBadRequest, http.StatusBadRequest},
		{"a locked resource", ErrLocked, http.StatusLocked},
		{"a failed precondition", ErrPreconditionFailed, http.StatusPreconditionFailed},
		{"a missing resource", ErrNotFound, http.StatusNotFound},
		{"a bound", limits.Exceed("x", 1, 2), http.StatusInsufficientStorage},
		// The existence rule: outside every grant is a 404, never a 403.
		{"outside every grant", core.ErrNotFound, http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := StatusOf(tc.err)
			if got != tc.want {
				t.Fatalf("StatusOf(%v) = %d, want %d", tc.err, got, tc.want)
			}
		})
	}
	// A wrapped error still maps, because everything above goes through
	// errors.Is rather than an equality test.
	if got, _ := StatusOf(errors.New("something else")); got != http.StatusInternalServerError {
		t.Fatalf("an unknown error = %d, want 500", got)
	}
}
