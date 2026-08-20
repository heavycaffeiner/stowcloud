//go:build linux

package dav

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/internal/acl"
	"github.com/heavycaffeiner/stowcloud/go/internal/clock"
	"github.com/heavycaffeiner/stowcloud/go/internal/core"
	"github.com/heavycaffeiner/stowcloud/go/internal/store"
	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
)

const testUser = core.UserID(42)

type fixture struct {
	h     *Handler
	core  *core.Core
	store *store.Store
	host  string
	clk   clock.Clock
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	clk := clock.Fixed(time.Unix(0, 1_700_000_000_000_000_000))
	s, err := store.Open(dir, store.Options{Clock: clk})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() {
		if cerr := s.Close(); cerr != nil {
			t.Errorf("closing the store: %v", cerr)
		}
	})

	ev := acl.NewEvaluator()
	c, err := core.New(s, core.Options{ACL: ev, Clock: clk})
	if err != nil {
		t.Fatalf("core.New: %v", err)
	}

	host := filepath.Join(t.TempDir(), "share")
	if merr := os.MkdirAll(host, 0o775); merr != nil {
		t.Fatalf("creating the share: %v", merr)
	}
	if rerr := c.RegisterShare(context.Background(), core.ShareDef{
		ID: 1, Name: "docs", Host: host, Policy: vfs.DefaultSharePolicy(),
	}); rerr != nil {
		t.Fatalf("RegisterShare: %v", rerr)
	}

	if serr := s.State().Write(context.Background(), func(tx *sql.Tx) error {
		if _, uerr := tx.ExecContext(context.Background(),
			`INSERT INTO user(id, name, pw_hash, created_ns) VALUES (?, 'tester', 'x', 0)`,
			int64(testUser)); uerr != nil {
			return uerr
		}
		_, gerr := tx.ExecContext(context.Background(),
			`INSERT INTO "grant"(user, share, subpath, allow, deny, inherit, label, created_ns)
			 VALUES (?, 1, '', ?, 0, 1, 'docs', 0)`,
			int64(testUser), int64(acl.Read|acl.Write|acl.Create|acl.Delete|acl.Download))
		return gerr
	}); serr != nil {
		t.Fatalf("seeding the account and its grant: %v", serr)
	}
	if lerr := ev.LoadFromState(context.Background(), s.State().SQL()); lerr != nil {
		t.Fatalf("loading grants: %v", lerr)
	}

	h := New(Options{
		Core:  c,
		State: s.State(),
		Locks: NewLocks(s.State(), clk),
	})
	return &fixture{h: h, core: c, store: s, host: host, clk: clk}
}

// resolve turns a share-relative test path into a resolution. A virtual path
// carries no leading slash, and the share label is its first component.
func (f *fixture) resolve(t *testing.T, p string) core.Resolved {
	t.Helper()
	vp, err := vfs.ParseVpath(strings.TrimSuffix("docs/"+strings.TrimPrefix(p, "/"), "/"))
	if err != nil {
		t.Fatalf("ParseVpath(%q): %v", p, err)
	}
	res, err := f.core.Resolve(testUser, vp, acl.Read)
	if err != nil {
		t.Fatalf("Resolve(%q): %v", p, err)
	}
	return res
}

func (f *fixture) write(t *testing.T, rel, body string) {
	t.Helper()
	full := filepath.Join(f.host, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o775); err != nil {
		t.Fatalf("creating the parent of %s: %v", rel, err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatalf("writing %s: %v", rel, err)
	}
}

func (f *fixture) do(t *testing.T, method, path, body string, header http.Header) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, "/dav/docs"+path, strings.NewReader(body))
	for k, vs := range header {
		for _, v := range vs {
			r.Header.Add(k, v)
		}
	}
	rec := httptest.NewRecorder()
	f.h.ServeMethod(rec, r, f.resolve(t, path))
	return rec
}

// PROPFIND.

func TestPropfindDepthZeroReportsOnlyTheResource(t *testing.T) {
	f := newFixture(t)
	f.write(t, "a.txt", "hello")
	f.write(t, "b.txt", "world")

	rec := f.do(t, "PROPFIND", "/", "", http.Header{"Depth": {"0"}})
	if rec.Code != http.StatusMultiStatus {
		t.Fatalf("status = %d, want 207\n%s", rec.Code, rec.Body)
	}
	doc := rec.Body.String()
	if n := strings.Count(doc, "<D:response>"); n != 1 {
		t.Fatalf("got %d responses, want only the collection itself\n%s", n, doc)
	}
}

func TestPropfindDepthOneListsTheChildren(t *testing.T) {
	f := newFixture(t)
	f.write(t, "a.txt", "hello")
	f.write(t, "sub/c.txt", "nested")

	rec := f.do(t, "PROPFIND", "/", "", http.Header{"Depth": {"1"}})
	doc := rec.Body.String()
	// The collection, the file and the subdirectory, but not what is inside it.
	if n := strings.Count(doc, "<D:response>"); n != 3 {
		t.Fatalf("got %d responses, want 3\n%s", n, doc)
	}
	if strings.Contains(doc, "c.txt") {
		t.Fatalf("depth 1 descended into a subdirectory\n%s", doc)
	}
}

func TestPropfindDepthInfinityDescends(t *testing.T) {
	f := newFixture(t)
	f.write(t, "sub/c.txt", "nested")

	rec := f.do(t, "PROPFIND", "/", "", http.Header{"Depth": {"infinity"}})
	doc := rec.Body.String()
	if !strings.Contains(doc, "c.txt") {
		t.Fatalf("depth infinity did not descend\n%s", doc)
	}
}

func TestPropfindReportsLengthForFilesAndNotForCollections(t *testing.T) {
	f := newFixture(t)
	f.write(t, "a.txt", "hello")

	rec := f.do(t, "PROPFIND", "/a.txt", "", http.Header{"Depth": {"0"}})
	doc := rec.Body.String()
	if !strings.Contains(doc, "<D:getcontentlength>5</D:getcontentlength>") {
		t.Fatalf("the file's length is missing or wrong\n%s", doc)
	}

	rec = f.do(t, "PROPFIND", "/", "", http.Header{"Depth": {"0"}})
	doc = rec.Body.String()
	// A collection reporting a length of zero is a lie a sync client acts on.
	if strings.Contains(doc, "getcontentlength") {
		t.Fatalf("a collection reported a content length\n%s", doc)
	}
	if !strings.Contains(doc, "<D:collection/>") {
		t.Fatalf("the collection is not marked as one\n%s", doc)
	}
}

func TestPropfindNamedReturnsWhatWasAskedAnd404sTheRest(t *testing.T) {
	f := newFixture(t)
	f.write(t, "a.txt", "hello")

	body := `<D:propfind xmlns:D="DAV:"><D:prop>` +
		`<D:getcontentlength/><D:getcontentlanguage/>` +
		`</D:prop></D:propfind>`
	rec := f.do(t, "PROPFIND", "/a.txt", body, http.Header{"Depth": {"0"}})
	doc := rec.Body.String()
	if !strings.Contains(doc, "<D:getcontentlength>5</D:getcontentlength>") {
		t.Fatalf("the requested property is missing\n%s", doc)
	}
	if !strings.Contains(doc, "HTTP/1.1 404 Not Found") {
		t.Fatalf("the unavailable property did not become a 404\n%s", doc)
	}
}

func TestPropnameReturnsNamesWithoutValues(t *testing.T) {
	f := newFixture(t)
	f.write(t, "a.txt", "hello")

	body := `<D:propfind xmlns:D="DAV:"><D:propname/></D:propfind>`
	rec := f.do(t, "PROPFIND", "/a.txt", body, http.Header{"Depth": {"0"}})
	doc := rec.Body.String()
	if !strings.Contains(doc, "<D:getcontentlength/>") {
		t.Fatalf("propname did not return the bare name\n%s", doc)
	}
	if strings.Contains(doc, "<D:getcontentlength>5") {
		t.Fatalf("propname leaked a value\n%s", doc)
	}
}

func TestABodyWithADoctypeIsRefusedByTheMethod(t *testing.T) {
	f := newFixture(t)
	f.write(t, "a.txt", "hello")
	body := `<!DOCTYPE x><D:propfind xmlns:D="DAV:"><D:allprop/></D:propfind>`
	rec := f.do(t, "PROPFIND", "/a.txt", body, http.Header{"Depth": {"0"}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a DTD", rec.Code)
	}
}

// PROPPATCH and dead properties.

func TestAPropPatchRoundTripsThroughAPropfind(t *testing.T) {
	f := newFixture(t)
	f.write(t, "a.txt", "hello")

	set := `<D:propertyupdate xmlns:D="DAV:"><D:set><D:prop>` +
		`<colour xmlns="urn:test">red</colour>` +
		`</D:prop></D:set></D:propertyupdate>`
	rec := f.do(t, "PROPPATCH", "/a.txt", set, nil)
	if rec.Code != http.StatusMultiStatus {
		t.Fatalf("status = %d, want 207\n%s", rec.Code, rec.Body)
	}

	ask := `<D:propfind xmlns:D="DAV:"><D:prop>` +
		`<colour xmlns="urn:test"/></D:prop></D:propfind>`
	rec = f.do(t, "PROPFIND", "/a.txt", ask, http.Header{"Depth": {"0"}})
	doc := rec.Body.String()
	if !strings.Contains(doc, "red") {
		t.Fatalf("the stored property did not come back\n%s", doc)
	}
}

func TestARemovedPropertyIsGone(t *testing.T) {
	f := newFixture(t)
	f.write(t, "a.txt", "hello")

	set := `<D:propertyupdate xmlns:D="DAV:"><D:set><D:prop>` +
		`<colour xmlns="urn:test">red</colour></D:prop></D:set></D:propertyupdate>`
	if rec := f.do(t, "PROPPATCH", "/a.txt", set, nil); rec.Code != http.StatusMultiStatus {
		t.Fatalf("the set failed: %d", rec.Code)
	}
	rm := `<D:propertyupdate xmlns:D="DAV:"><D:remove><D:prop>` +
		`<colour xmlns="urn:test"/></D:prop></D:remove></D:propertyupdate>`
	if rec := f.do(t, "PROPPATCH", "/a.txt", rm, nil); rec.Code != http.StatusMultiStatus {
		t.Fatalf("the remove failed: %d", rec.Code)
	}

	ask := `<D:propfind xmlns:D="DAV:"><D:prop><colour xmlns="urn:test"/></D:prop></D:propfind>`
	rec := f.do(t, "PROPFIND", "/a.txt", ask, http.Header{"Depth": {"0"}})
	doc := rec.Body.String()
	if strings.Contains(doc, "red") {
		t.Fatalf("the removed property is still there\n%s", doc)
	}
	if !strings.Contains(doc, "HTTP/1.1 404 Not Found") {
		t.Fatalf("the removed property did not become a 404\n%s", doc)
	}
}

// A live property is computed, not stored. Setting one would give a resource
// two answers to one question.
func TestALivePropertyCannotBeSet(t *testing.T) {
	f := newFixture(t)
	f.write(t, "a.txt", "hello")

	set := `<D:propertyupdate xmlns:D="DAV:"><D:set><D:prop>` +
		`<D:getcontentlength>999</D:getcontentlength>` +
		`</D:prop></D:set></D:propertyupdate>`
	rec := f.do(t, "PROPPATCH", "/a.txt", set, nil)
	if !strings.Contains(rec.Body.String(), "403") {
		t.Fatalf("setting a live property was not refused\n%s", rec.Body)
	}

	// And the real value is untouched.
	rec = f.do(t, "PROPFIND", "/a.txt", "", http.Header{"Depth": {"0"}})
	if !strings.Contains(rec.Body.String(), "<D:getcontentlength>5</D:getcontentlength>") {
		t.Fatalf("the live value changed\n%s", rec.Body)
	}
}

// Dead properties are keyed by identity, so a value stored on one file is not
// visible on another.
func TestADeadPropertyDoesNotLeakBetweenFiles(t *testing.T) {
	f := newFixture(t)
	f.write(t, "a.txt", "hello")
	f.write(t, "b.txt", "world")

	set := `<D:propertyupdate xmlns:D="DAV:"><D:set><D:prop>` +
		`<colour xmlns="urn:test">red</colour></D:prop></D:set></D:propertyupdate>`
	if rec := f.do(t, "PROPPATCH", "/a.txt", set, nil); rec.Code != http.StatusMultiStatus {
		t.Fatalf("the set failed: %d", rec.Code)
	}

	ask := `<D:propfind xmlns:D="DAV:"><D:prop><colour xmlns="urn:test"/></D:prop></D:propfind>`
	rec := f.do(t, "PROPFIND", "/b.txt", ask, http.Header{"Depth": {"0"}})
	if strings.Contains(rec.Body.String(), "red") {
		t.Fatalf("a property leaked onto another file\n%s", rec.Body)
	}
}

// LOCK.

func TestALockIsTakenAndReportedBackWithItsToken(t *testing.T) {
	f := newFixture(t)
	f.write(t, "a.txt", "hello")

	body := `<D:lockinfo xmlns:D="DAV:"><D:lockscope><D:exclusive/></D:lockscope>` +
		`<D:locktype><D:write/></D:locktype><D:owner>tester</D:owner></D:lockinfo>`
	rec := f.do(t, "LOCK", "/a.txt", body, http.Header{"Depth": {"0"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200\n%s", rec.Code, rec.Body)
	}
	tok := rec.Header().Get("Lock-Token")
	if !strings.HasPrefix(tok, "<urn:uuid:") {
		t.Fatalf("Lock-Token = %q, want a urn:uuid form", tok)
	}
	if !strings.Contains(rec.Body.String(), "tester") {
		t.Fatalf("the owner is missing from the response\n%s", rec.Body)
	}
}

func TestASharedLockIsRefused(t *testing.T) {
	f := newFixture(t)
	f.write(t, "a.txt", "hello")
	body := `<D:lockinfo xmlns:D="DAV:"><D:lockscope><D:shared/></D:lockscope>` +
		`<D:locktype><D:write/></D:locktype></D:lockinfo>`
	rec := f.do(t, "LOCK", "/a.txt", body, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: shared locks are not offered", rec.Code)
	}
}

// The point of a lock: a second client cannot take it, and a write without the
// token is refused.
func TestASecondLockOnTheSameResourceIsRefused(t *testing.T) {
	f := newFixture(t)
	f.write(t, "a.txt", "hello")
	body := `<D:lockinfo xmlns:D="DAV:"><D:lockscope><D:exclusive/></D:lockscope>` +
		`<D:locktype><D:write/></D:locktype></D:lockinfo>`

	if rec := f.do(t, "LOCK", "/a.txt", body, nil); rec.Code != http.StatusOK {
		t.Fatalf("the first lock failed: %d", rec.Code)
	}
	rec := f.do(t, "LOCK", "/a.txt", body, nil)
	if rec.Code != http.StatusLocked {
		t.Fatalf("status = %d, want 423 for a second lock", rec.Code)
	}
}

func TestAWriteWithoutTheLockTokenIsRefusedAndWithItIsAllowed(t *testing.T) {
	f := newFixture(t)
	f.write(t, "a.txt", "hello")
	lockBody := `<D:lockinfo xmlns:D="DAV:"><D:lockscope><D:exclusive/></D:lockscope>` +
		`<D:locktype><D:write/></D:locktype></D:lockinfo>`
	rec := f.do(t, "LOCK", "/a.txt", lockBody, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("the lock failed: %d", rec.Code)
	}
	token := strings.Trim(rec.Header().Get("Lock-Token"), "<>")

	patch := `<D:propertyupdate xmlns:D="DAV:"><D:set><D:prop>` +
		`<colour xmlns="urn:test">red</colour></D:prop></D:set></D:propertyupdate>`

	// No token: refused.
	if rec := f.do(t, "PROPPATCH", "/a.txt", patch, nil); rec.Code != http.StatusLocked {
		t.Fatalf("a write with no token returned %d, want 423", rec.Code)
	}
	// The right token: allowed.
	withToken := http.Header{"If": {"(<" + token + ">)"}}
	if rec := f.do(t, "PROPPATCH", "/a.txt", patch, withToken); rec.Code != http.StatusMultiStatus {
		t.Fatalf("a write with the token returned %d, want 207", rec.Code)
	}
}

func TestUnlockReleasesTheResource(t *testing.T) {
	f := newFixture(t)
	f.write(t, "a.txt", "hello")
	lockBody := `<D:lockinfo xmlns:D="DAV:"><D:lockscope><D:exclusive/></D:lockscope>` +
		`<D:locktype><D:write/></D:locktype></D:lockinfo>`
	rec := f.do(t, "LOCK", "/a.txt", lockBody, nil)
	token := strings.Trim(rec.Header().Get("Lock-Token"), "<>")

	rec = f.do(t, "UNLOCK", "/a.txt", "", http.Header{"Lock-Token": {"<" + token + ">"}})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("UNLOCK returned %d, want 204\n%s", rec.Code, rec.Body)
	}
	// The resource takes a lock again, which it could not have done if the
	// first were still held.
	if rec := f.do(t, "LOCK", "/a.txt", lockBody, nil); rec.Code != http.StatusOK {
		t.Fatalf("relocking after UNLOCK returned %d", rec.Code)
	}
}

// A lock survives a restart, because a client that believes it holds one must
// not be quietly contradicted.
func TestALockSurvivesAReopen(t *testing.T) {
	f := newFixture(t)
	f.write(t, "a.txt", "hello")
	lockBody := `<D:lockinfo xmlns:D="DAV:"><D:lockscope><D:exclusive/></D:lockscope>` +
		`<D:locktype><D:write/></D:locktype></D:lockinfo>`
	if rec := f.do(t, "LOCK", "/a.txt", lockBody, nil); rec.Code != http.StatusOK {
		t.Fatalf("the lock failed: %d", rec.Code)
	}

	// A second handler over the same store is what a restart looks like.
	restarted := New(Options{
		Core: f.core, State: f.store.State(),
		Locks: NewLocks(f.store.State(), f.clk),
	})
	r := httptest.NewRequest("LOCK", "/dav/docs/a.txt", strings.NewReader(lockBody))
	rec := httptest.NewRecorder()
	restarted.ServeMethod(rec, r, f.resolve(t, "/a.txt"))
	if rec.Code != http.StatusLocked {
		t.Fatalf("after a restart the lock was forgotten: %d", rec.Code)
	}
}

// An expired lock is not honoured, whether or not anything swept the row.
func TestAnExpiredLockIsNotHonoured(t *testing.T) {
	f := newFixture(t)
	f.write(t, "a.txt", "hello")
	lockBody := `<D:lockinfo xmlns:D="DAV:"><D:lockscope><D:exclusive/></D:lockscope>` +
		`<D:locktype><D:write/></D:locktype></D:lockinfo>`
	rec := f.do(t, "LOCK", "/a.txt", lockBody, http.Header{"Timeout": {"Second-60"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("the lock failed: %d", rec.Code)
	}

	// Move past the deadline. The row is untouched; only the clock moved.
	later := clock.Fixed(f.clk.Now().Add(2 * time.Hour))
	restarted := New(Options{
		Core: f.core, State: f.store.State(),
		Locks: NewLocks(f.store.State(), later),
	})
	r := httptest.NewRequest("LOCK", "/dav/docs/a.txt", strings.NewReader(lockBody))
	rec = httptest.NewRecorder()
	restarted.ServeMethod(rec, r, f.resolve(t, "/a.txt"))
	if rec.Code != http.StatusOK {
		t.Fatalf("an expired lock still blocked a new one: %d", rec.Code)
	}
}

// A depth-infinity lock covers descendants, and the separator check is what
// stops it covering a sibling whose name starts with the same bytes.
func TestADepthInfinityLockCoversDescendantsAndNotSiblings(t *testing.T) {
	f := newFixture(t)
	f.write(t, "a/x.txt", "in")
	f.write(t, "ab/y.txt", "out")

	lockBody := `<D:lockinfo xmlns:D="DAV:"><D:lockscope><D:exclusive/></D:lockscope>` +
		`<D:locktype><D:write/></D:locktype></D:lockinfo>`
	if rec := f.do(t, "LOCK", "/a", lockBody, http.Header{"Depth": {"infinity"}}); rec.Code != http.StatusOK {
		t.Fatalf("the lock failed: %d", rec.Code)
	}

	patch := `<D:propertyupdate xmlns:D="DAV:"><D:set><D:prop>` +
		`<c xmlns="urn:test">1</c></D:prop></D:set></D:propertyupdate>`

	if rec := f.do(t, "PROPPATCH", "/a/x.txt", patch, nil); rec.Code != http.StatusLocked {
		t.Fatalf("a descendant was writable under a depth-infinity lock: %d", rec.Code)
	}
	if rec := f.do(t, "PROPPATCH", "/ab/y.txt", patch, nil); rec.Code != http.StatusMultiStatus {
		t.Fatalf("a sibling was locked by a prefix match: %d", rec.Code)
	}
}

// OPTIONS.

func TestOptionsAdvertisesClassTwo(t *testing.T) {
	f := newFixture(t)
	f.write(t, "a.txt", "hello")
	rec := f.do(t, "OPTIONS", "/a.txt", "", nil)
	if got := rec.Header().Get("DAV"); !strings.Contains(got, "2") {
		t.Fatalf("DAV = %q, want class 2", got)
	}
	allow := rec.Header().Get("Allow")
	for _, m := range []string{"PROPFIND", "PROPPATCH", "LOCK", "UNLOCK"} {
		if !strings.Contains(allow, m) {
			t.Fatalf("Allow = %q, missing %s", allow, m)
		}
	}
}

// Registering a source is what puts SEARCH and REPORT into Allow, so a build
// with no source advertises neither. That is the isolation rule holding at the
// protocol surface rather than only in the import graph.
func TestSearchAndReportAreNotAdvertisedWithoutASource(t *testing.T) {
	f := newFixture(t)
	f.write(t, "a.txt", "hello")
	rec := f.do(t, "OPTIONS", "/a.txt", "", nil)
	allow := rec.Header().Get("Allow")
	for _, m := range []string{"SEARCH", "REPORT"} {
		if strings.Contains(allow, m) {
			t.Fatalf("Allow = %q, which advertises %s with no source registered", allow, m)
		}
	}
	if rec := f.do(t, "REPORT", "/a.txt", `<x/>`, nil); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("REPORT returned %d with no source, want 405", rec.Code)
	}
}
