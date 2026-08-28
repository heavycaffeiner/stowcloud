//go:build linux

package dav

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
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
			int64(testUser),
			int64(acl.Read|acl.Write|acl.Create|acl.Delete|acl.Rename|acl.Move|acl.Download))
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

// Shared locks are taken, and what makes them shared is that a second one is
// admitted where an exclusive one is refused.
//
// This build used to answer 400 to the request outright, which reads to a
// client as the scope not being understood at all.
func TestASharedLockAdmitsASecondHolderAndStillBlocksAnExclusiveOne(t *testing.T) {
	f := newFixture(t)
	f.write(t, "a.txt", "hello")
	sharedBody := `<D:lockinfo xmlns:D="DAV:"><D:lockscope><D:shared/></D:lockscope>` +
		`<D:locktype><D:write/></D:locktype></D:lockinfo>`
	exclusiveBody := `<D:lockinfo xmlns:D="DAV:"><D:lockscope><D:exclusive/></D:lockscope>` +
		`<D:locktype><D:write/></D:locktype></D:lockinfo>`

	rec := f.do(t, "LOCK", "/a.txt", sharedBody, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for a shared lock\n%s", rec.Code, rec.Body)
	}
	// The response says which scope was taken. Reporting "exclusive" here
	// would tell the client it holds something nobody else can take.
	if !strings.Contains(rec.Body.String(), "shared") {
		t.Fatalf("the lock was reported as something other than shared\n%s", rec.Body)
	}

	// A second shared lock is what shared is for.
	if rec := f.do(t, "LOCK", "/a.txt", sharedBody, nil); rec.Code != http.StatusOK {
		t.Fatalf("a second shared lock returned %d, want 200", rec.Code)
	}
	// An exclusive one over it is not.
	if rec := f.do(t, "LOCK", "/a.txt", exclusiveBody, nil); rec.Code != http.StatusLocked {
		t.Fatalf("an exclusive lock over a shared one returned %d, want 423", rec.Code)
	}
}

// And the other order: a shared lock cannot be taken over an exclusive one.
func TestASharedLockIsRefusedOverAnExclusiveOne(t *testing.T) {
	f := newFixture(t)
	f.write(t, "a.txt", "hello")
	exclusiveBody := `<D:lockinfo xmlns:D="DAV:"><D:lockscope><D:exclusive/></D:lockscope>` +
		`<D:locktype><D:write/></D:locktype></D:lockinfo>`
	sharedBody := `<D:lockinfo xmlns:D="DAV:"><D:lockscope><D:shared/></D:lockscope>` +
		`<D:locktype><D:write/></D:locktype></D:lockinfo>`

	if rec := f.do(t, "LOCK", "/a.txt", exclusiveBody, nil); rec.Code != http.StatusOK {
		t.Fatalf("the exclusive lock failed: %d", rec.Code)
	}
	if rec := f.do(t, "LOCK", "/a.txt", sharedBody, nil); rec.Code != http.StatusLocked {
		t.Fatalf("a shared lock over an exclusive one returned %d, want 423", rec.Code)
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

// A PUT guarded by the lock token and the ETag the server itself issued has to
// succeed. This is the conformance suite's cond_put, reproduced end to end.
//
// It failed with 412 on every request. Every file validator on Linux is weak
// here, because statx exposes no inode change version, and the If evaluation
// refused any weak tag outright: a client that echoed back the exact ETag it
// had just been given was told its precondition failed, so guarding a write
// with If was impossible on this build.
func TestAPutGuardedByTheServersOwnETagAndLockSucceeds(t *testing.T) {
	f := newFixture(t)
	f.write(t, "a.txt", "hello")

	// The ETag exactly as a client reads it, weak marker and all, which is
	// what the suite does with a HEAD.
	head := f.do(t, "HEAD", "/a.txt", "", nil)
	etag := head.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag came back, so there is nothing to guard on")
	}

	lockBody := `<D:lockinfo xmlns:D="DAV:"><D:lockscope><D:exclusive/></D:lockscope>` +
		`<D:locktype><D:write/></D:locktype></D:lockinfo>`
	rec := f.do(t, "LOCK", "/a.txt", lockBody, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("the lock failed: %d", rec.Code)
	}
	token := strings.Trim(rec.Header().Get("Lock-Token"), "<>")

	// The suite's own header shape: (<token> [etag]).
	guard := http.Header{"If": {"(<" + token + "> [" + etag + "])"}}
	if rec := f.do(t, "PUT", "/a.txt", "replaced", guard); rec.Code != http.StatusNoContent {
		t.Fatalf("a PUT guarded by the server's own ETag returned %d, want 204\n%s",
			rec.Code, rec.Body)
	}

	// And the guard still guards: a tag that is not the current one fails.
	stale := http.Header{"If": {"(<" + token + `> [W/"0000000000000000"])`}}
	if rec := f.do(t, "PUT", "/a.txt", "again", stale); rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("a PUT guarded by a stale ETag returned %d, want 412", rec.Code)
	}

	// The suite's complex form: two lists, OR-ed, the second asserting the
	// client does not hold a token nobody holds. The ETag moved with the write
	// above, so it is read again.
	head = f.do(t, "HEAD", "/a.txt", "", nil)
	etag = head.Header().Get("ETag")
	complex := http.Header{"If": {
		"(<" + token + "> [" + etag + "]) (Not <DAV:no-lock> [" + etag + "])",
	}}
	if rec := f.do(t, "PUT", "/a.txt", "third", complex); rec.Code != http.StatusNoContent {
		t.Fatalf("a PUT with the complex conditional returned %d, want 204\n%s",
			rec.Code, rec.Body)
	}
}

// A LOCK on a URL that maps to nothing creates an empty resource and locks it.
//
// It is how a client reserves a name before writing it, so answering 404 makes
// the reservation impossible: a client that locks before every PUT could not
// create a file at all. This build answered 404 until the conformance run
// named it.
func TestLockingAnUnmappedURLCreatesAndLocksIt(t *testing.T) {
	f := newFixture(t)
	body := `<D:lockinfo xmlns:D="DAV:"><D:lockscope><D:exclusive/></D:lockscope>` +
		`<D:locktype><D:write/></D:locktype></D:lockinfo>`

	rec := f.do(t, "LOCK", "/reserved.txt", body, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 for a lock on an unmapped URL\n%s", rec.Code, rec.Body)
	}
	if rec.Header().Get("Lock-Token") == "" {
		t.Fatal("no lock token came back, so the caller holds nothing")
	}

	// The resource exists now, and it is empty: a reservation, not a file with
	// content nobody sent.
	got, err := os.ReadFile(filepath.Join(f.host, "reserved.txt"))
	if err != nil {
		t.Fatalf("the locked resource was not created: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("the created resource holds %q, want nothing", got)
	}

	// And it is genuinely locked: a second lock is refused rather than
	// creating it again.
	if rec := f.do(t, "LOCK", "/reserved.txt", body, nil); rec.Code != http.StatusLocked {
		t.Fatalf("a second lock returned %d, want 423", rec.Code)
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

// Depth: infinity over a large collection is refused rather than attempted.
// The bound is lowered for the test because the real one is a hundred thousand
// entries and the point is which check refuses, not how long it takes to hit.
func TestDepthInfinityOverALargeCollectionIsRefused(t *testing.T) {
	f := newFixture(t)
	for i := 0; i < 8; i++ {
		f.write(t, "f"+strconv.Itoa(i)+".txt", "x")
	}

	// A handler whose infinity bound is smaller than the directory.
	f.h = New(Options{
		Core: f.core, State: f.store.State(),
		Locks:           NewLocks(f.store.State(), f.clk),
		InfinityEntries: 4,
	})

	rec := f.do(t, "PROPFIND", "/", "", http.Header{"Depth": {"infinity"}})
	if rec.Code != http.StatusInsufficientStorage {
		t.Fatalf("status = %d, want 507\n%s", rec.Code, rec.Body)
	}

	// Depth 1 over the same directory is unaffected: the refusal is about the
	// recursive walk, not about the directory being large.
	if rec := f.do(t, "PROPFIND", "/", "", http.Header{"Depth": {"1"}}); rec.Code != http.StatusMultiStatus {
		t.Fatalf("depth 1 returned %d, want 207", rec.Code)
	}
}

// A path outside every grant is a 404 here too. WebDAV's status vocabulary
// makes 403 feel natural and it is wrong: a 403 tells a stranger the path
// exists.
func TestAPathOutsideEveryGrantIsNotFound(t *testing.T) {
	f := newFixture(t)
	vp, err := vfs.ParseVpath("nosuchshare/secret.txt")
	if err != nil {
		t.Fatalf("ParseVpath: %v", err)
	}
	if _, err := f.core.Resolve(testUser, vp, acl.Read); err == nil {
		t.Fatal("a path outside every grant resolved")
	} else if code, _ := StatusOf(err); code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for a path outside every grant", code)
	}
}

// The content methods. Each is a shell over the core, so what is proved here
// is the WebDAV framing rather than the filesystem behaviour.

func TestPutCreatesThenUpdates(t *testing.T) {
	f := newFixture(t)

	rec := f.do(t, "PUT", "/new.txt", "hello", nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("a first PUT returned %d, want 201\n%s", rec.Code, rec.Body)
	}
	got, err := os.ReadFile(filepath.Join(f.host, "new.txt"))
	if err != nil || string(got) != "hello" {
		t.Fatalf("the file is %q, %v", got, err)
	}

	// A PUT onto an existing file is 204, not 201: nothing was created.
	rec = f.do(t, "PUT", "/new.txt", "goodbye", nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("a second PUT returned %d, want 204", rec.Code)
	}
	got, err = os.ReadFile(filepath.Join(f.host, "new.txt"))
	if err != nil || string(got) != "goodbye" {
		t.Fatalf("the file is %q, %v", got, err)
	}
}

func TestGetReturnsTheBodyAndHeadReturnsOnlyHeaders(t *testing.T) {
	f := newFixture(t)
	f.write(t, "a.txt", "hello")

	rec := f.do(t, "GET", "/a.txt", "", nil)
	if rec.Code != http.StatusOK || rec.Body.String() != "hello" {
		t.Fatalf("GET = %d %q", rec.Code, rec.Body)
	}
	if rec.Header().Get("ETag") == "" {
		t.Fatal("GET returned no validator, so a client cannot revalidate")
	}

	rec = f.do(t, "HEAD", "/a.txt", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("HEAD = %d", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("HEAD returned a body of %d bytes", rec.Body.Len())
	}
	if rec.Header().Get("Content-Length") != "5" {
		t.Fatalf("HEAD Content-Length = %q, want 5", rec.Header().Get("Content-Length"))
	}
}

// The validator a GET returns is the one a PROPFIND reports. A client compares
// them, so a mismatch is a sync loop.
func TestTheGetAndPropfindValidatorsAgree(t *testing.T) {
	f := newFixture(t)
	f.write(t, "a.txt", "hello")

	fromGet := f.do(t, "GET", "/a.txt", "", nil).Header().Get("ETag")
	doc := f.do(t, "PROPFIND", "/a.txt", "", http.Header{"Depth": {"0"}}).Body.String()

	if fromGet == "" || !strings.Contains(doc, EscapeText(fromGet)) {
		t.Fatalf("GET returned %q which is not in the PROPFIND document\n%s", fromGet, doc)
	}
}

func TestIfNoneMatchAnswers304(t *testing.T) {
	f := newFixture(t)
	f.write(t, "a.txt", "hello")

	tag := f.do(t, "GET", "/a.txt", "", nil).Header().Get("ETag")
	rec := f.do(t, "GET", "/a.txt", "", http.Header{"If-None-Match": {tag}})
	if rec.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want 304", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Fatal("a 304 carried a body")
	}
}

func TestMkcolCreatesACollectionAndRefusesABody(t *testing.T) {
	f := newFixture(t)

	if rec := f.do(t, "MKCOL", "/sub", "", nil); rec.Code != http.StatusCreated {
		t.Fatalf("MKCOL returned %d, want 201", rec.Code)
	}
	if st, err := os.Stat(filepath.Join(f.host, "sub")); err != nil || !st.IsDir() {
		t.Fatalf("the collection was not created: %v", err)
	}

	// No body format is defined for MKCOL, so honouring one would be inventing
	// semantics.
	rec := f.do(t, "MKCOL", "/other", "<x/>", nil)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("MKCOL with a body returned %d, want 415", rec.Code)
	}
}

func TestDeleteRemovesTheResourceAndItsProperties(t *testing.T) {
	f := newFixture(t)
	f.write(t, "a.txt", "hello")

	set := `<D:propertyupdate xmlns:D="DAV:"><D:set><D:prop>` +
		`<colour xmlns="urn:test">red</colour></D:prop></D:set></D:propertyupdate>`
	if rec := f.do(t, "PROPPATCH", "/a.txt", set, nil); rec.Code != http.StatusMultiStatus {
		t.Fatalf("the set failed: %d", rec.Code)
	}

	if rec := f.do(t, "DELETE", "/a.txt", "", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE returned %d, want 204", rec.Code)
	}

	// The properties must not outlive the resource and reattach to whatever
	// next occupies the inode.
	f.write(t, "a.txt", "different")
	ask := `<D:propfind xmlns:D="DAV:"><D:prop><colour xmlns="urn:test"/></D:prop></D:propfind>`
	rec := f.do(t, "PROPFIND", "/a.txt", ask, http.Header{"Depth": {"0"}})
	if strings.Contains(rec.Body.String(), "red") {
		t.Fatalf("a deleted resource's property came back on a new file\n%s", rec.Body)
	}
}

// A locked resource refuses a PUT without the token, which is the point of
// holding one.
func TestAPutIntoALockedResourceIsRefusedWithoutTheToken(t *testing.T) {
	f := newFixture(t)
	f.write(t, "a.txt", "hello")
	lockBody := `<D:lockinfo xmlns:D="DAV:"><D:lockscope><D:exclusive/></D:lockscope>` +
		`<D:locktype><D:write/></D:locktype></D:lockinfo>`
	rec := f.do(t, "LOCK", "/a.txt", lockBody, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("the lock failed: %d", rec.Code)
	}
	token := strings.Trim(rec.Header().Get("Lock-Token"), "<>")

	if rec := f.do(t, "PUT", "/a.txt", "sneaky", nil); rec.Code != http.StatusLocked {
		t.Fatalf("a PUT with no token returned %d, want 423", rec.Code)
	}
	got, rerr := os.ReadFile(filepath.Join(f.host, "a.txt"))
	if rerr != nil || string(got) != "hello" {
		t.Fatalf("the refused PUT still wrote: %q, %v", got, rerr)
	}

	with := http.Header{"If": {"(<" + token + ">)"}}
	if rec := f.do(t, "PUT", "/a.txt", "allowed", with); rec.Code != http.StatusNoContent {
		t.Fatalf("a PUT with the token returned %d, want 204", rec.Code)
	}
}

// A GET of a collection has no defined body, so it is refused rather than
// answered with an invented index.
func TestGetOnACollectionIsNotAllowed(t *testing.T) {
	f := newFixture(t)
	f.write(t, "sub/x.txt", "x")
	if rec := f.do(t, "GET", "/sub", "", nil); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

func TestParseOverwriteDefaultsToTrue(t *testing.T) {
	if !ParseOverwrite("") || !ParseOverwrite("T") || !ParseOverwrite("t") {
		t.Fatal("Overwrite did not default to true")
	}
	if ParseOverwrite("F") || ParseOverwrite("f") || ParseOverwrite(" F ") {
		t.Fatal("Overwrite: F was not honoured")
	}
}

// COPY and MOVE take their destination from a header the router resolves, so
// they are driven directly rather than through ServeMethod.

func (f *fixture) transfer(t *testing.T, method, from, to string, overwrite bool) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, "/dav/docs"+from, nil)
	r.Header.Set("Destination", "/dav/docs"+to)
	rec := httptest.NewRecorder()
	target := MoveTarget{Resolved: f.resolve(t, to), Overwrite: overwrite}
	if method == "COPY" {
		f.h.ServeCopy(rec, r, f.resolve(t, from), target)
	} else {
		f.h.ServeMove(rec, r, f.resolve(t, from), target)
	}
	return rec
}

func TestMoveRelocatesTheFile(t *testing.T) {
	f := newFixture(t)
	f.write(t, "a.txt", "hello")

	if rec := f.transfer(t, "MOVE", "/a.txt", "/b.txt", true); rec.Code != http.StatusCreated {
		t.Fatalf("MOVE returned %d, want 201\n%s", rec.Code, rec.Body)
	}
	if _, err := os.Stat(filepath.Join(f.host, "a.txt")); err == nil {
		t.Fatal("the source survived a MOVE")
	}
	got, err := os.ReadFile(filepath.Join(f.host, "b.txt"))
	if err != nil || string(got) != "hello" {
		t.Fatalf("the destination is %q, %v", got, err)
	}
}

func TestCopyLeavesTheSourceInPlace(t *testing.T) {
	f := newFixture(t)
	f.write(t, "a.txt", "hello")

	if rec := f.transfer(t, "COPY", "/a.txt", "/b.txt", true); rec.Code != http.StatusCreated {
		t.Fatalf("COPY returned %d, want 201\n%s", rec.Code, rec.Body)
	}
	for _, name := range []string{"a.txt", "b.txt"} {
		got, err := os.ReadFile(filepath.Join(f.host, name))
		if err != nil || string(got) != "hello" {
			t.Fatalf("%s is %q, %v", name, got, err)
		}
	}
}

// Overwrite: F is what stops a MOVE from destroying the destination.
func TestOverwriteFalseRefusesAnExistingDestination(t *testing.T) {
	f := newFixture(t)
	f.write(t, "a.txt", "source")
	f.write(t, "b.txt", "destination")

	rec := f.transfer(t, "MOVE", "/a.txt", "/b.txt", false)
	if rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("status = %d, want 412", rec.Code)
	}
	got, rerr := os.ReadFile(filepath.Join(f.host, "b.txt"))
	if rerr != nil || string(got) != "destination" {
		t.Fatalf("the destination was overwritten anyway: %q, %v", got, rerr)
	}
}

// A recursive copy is a background operation, because a large tree cannot be
// copied inside a request.
func TestCopyingACollectionIsAccepted(t *testing.T) {
	f := newFixture(t)
	f.write(t, "sub/x.txt", "x")

	rec := f.transfer(t, "COPY", "/sub", "/sub2", true)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 for a recursive copy\n%s", rec.Code, rec.Body)
	}

	// The 202 says the copy started, not that it finished. Returning here
	// leaves it writing into a directory t.TempDir is about to remove, which
	// fails the cleanup rather than the assertion and does so only when the
	// timing happens to land: this test failed roughly one run in six with
	// "directory not empty" and nothing pointing at the copy.
	waitFor(t, func() bool {
		_, err := os.Stat(filepath.Join(f.host, "sub2", "x.txt"))
		return err == nil
	})
}

// A MKCOL whose parent does not exist is 409, not 404.
//
// The distinction is real to a client: 404 says the target is absent, which it
// is meant to be, and 409 says the parent is. A client that creates parents on
// demand branches on exactly that, and told 404 it has no reason to.
func TestMkcolWithAMissingParentIsAConflict(t *testing.T) {
	f := newFixture(t)

	rec := f.do(t, "MKCOL", "/nothere/child", "", nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 for a missing intermediate collection", rec.Code)
	}

	// With the parent there it succeeds, so the conflict is about the parent
	// rather than about the depth of the path.
	if rec := f.do(t, "MKCOL", "/nothere", "", nil); rec.Code != http.StatusCreated {
		t.Fatalf("creating the parent returned %d, want 201", rec.Code)
	}
	if rec := f.do(t, "MKCOL", "/nothere/child", "", nil); rec.Code != http.StatusCreated {
		t.Fatalf("creating the child returned %d, want 201", rec.Code)
	}
}

// The collection-copy sequence the conformance suite runs: copy a tree, copy
// it again elsewhere, refuse a copy onto an existing collection, then allow it
// with overwrite, and check the members actually arrived.
//
// The members are the point. A recursive COPY answered 202 and copied nothing
// at all, because the operation was started with a zero source stat, which told
// the walker every directory was a file. A test asserting only the status
// passed against that for the whole of the port.
func TestTheCollectionCopySequence(t *testing.T) {
	f := newFixture(t)
	for i := range 3 {
		f.write(t, fmt.Sprintf("ccsrc/foo.%d", i), "x")
	}
	f.write(t, "ccsrc/subcoll/deep.txt", "y")

	if rec := f.transfer(t, "COPY", "/ccsrc", "/ccdest", false); rec.Code != http.StatusAccepted {
		t.Fatalf("the first collection COPY returned %d\n%s", rec.Code, rec.Body)
	}

	// Every member, not just the first, and waited for as a set: the walk
	// visits entries in directory order, so any one of them finishing says
	// nothing about the others. A walk that stops early is the defect this
	// sequence exists to catch, and waiting on a single file would hide it.
	members := []string{filepath.Join("subcoll", "deep.txt")}
	for i := range 3 {
		members = append(members, fmt.Sprintf("foo.%d", i))
	}
	waitFor(t, func() bool {
		for _, m := range members {
			if _, err := os.Stat(filepath.Join(f.host, "ccdest", m)); err != nil {
				return false
			}
		}
		return true
	})

	// A second copy elsewhere, so there are two collections to copy between.
	if rec := f.transfer(t, "COPY", "/ccsrc", "/ccdest2", false); rec.Code != http.StatusAccepted {
		t.Fatalf("the second collection COPY returned %d", rec.Code)
	}
	// Every member again, not just the first. The copy below uses ccdest2 as
	// its source, so waiting on one file starts it against a tree still being
	// written, and what is missing there is missing from the result.
	waitFor(t, func() bool {
		for _, m := range members {
			if _, err := os.Stat(filepath.Join(f.host, "ccdest2", m)); err != nil {
				return false
			}
		}
		return true
	})

	// Onto an existing collection without overwrite: refused.
	if rec := f.transfer(t, "COPY", "/ccdest", "/ccdest2", false); rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("COPY onto an existing collection returned %d, want 412", rec.Code)
	}
	// With overwrite: allowed.
	if rec := f.transfer(t, "COPY", "/ccdest2", "/ccdest", true); rec.Code != http.StatusAccepted {
		t.Fatalf("COPY with overwrite returned %d, want 202", rec.Code)
	}
	// Waited for, like the others, and for every member rather than one: a 202
	// still running when the test returns is a copy writing into a directory
	// t.TempDir is removing, which fails the cleanup of whatever test happens
	// to be running rather than this one.
	waitFor(t, func() bool {
		for _, m := range members {
			if _, err := os.Stat(filepath.Join(f.host, "ccdest", m)); err != nil {
				return false
			}
		}
		return true
	})
}

// waitFor polls until cond holds, or fails the test.
//
// A poll rather than a sleep: a sleep long enough to be reliable on a loaded
// machine is one every run pays for, and a short one is the flake it was
// supposed to fix.
//
// The bound is a number of attempts rather than a wall-clock deadline. Nothing
// outside internal/clock reads the wall clock in this tree, and a test is not
// an exception: the rule exists because a machine whose clock has not been set
// is a machine this product still has to work on.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	const attempts = 5000
	for range attempts {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("the background operation did not finish")
}

// A range request has to be answered as a range.
//
// The header was advertised as supported and ignored, so a client asking for
// the tail of a file was handed the whole file with a success status, which a
// resuming download reads as the server restarting the transfer.
func TestARangeIsParsedOrRefused(t *testing.T) {
	const size = 100

	for _, tc := range []struct {
		raw        string
		wantStart  uint64
		wantEnd    uint64
		wantWhole  bool
		wantRefuse bool
	}{
		{raw: "", wantWhole: true},
		{raw: "bytes=0-9", wantStart: 0, wantEnd: 9},
		{raw: "bytes=10-19", wantStart: 10, wantEnd: 19},
		// An open end runs to the last byte.
		{raw: "bytes=90-", wantStart: 90, wantEnd: 99},
		// A suffix range names no start and asks for the last n bytes.
		{raw: "bytes=-10", wantStart: 90, wantEnd: 99},
		// A suffix longer than the file is the whole file, not a refusal.
		{raw: "bytes=-500", wantStart: 0, wantEnd: 99},
		// An end past the last byte is clamped rather than refused.
		{raw: "bytes=50-999", wantStart: 50, wantEnd: 99},

		// A start at or past the end is what the refusal exists for.
		{raw: "bytes=100-", wantRefuse: true},
		{raw: "bytes=500-600", wantRefuse: true},
		// Backwards.
		{raw: "bytes=50-10", wantRefuse: true},
		// A unit this surface does not serve.
		{raw: "items=0-9", wantRefuse: true},
		// Several ranges are a different response format, and answering one of
		// three is a client silently missing two.
		{raw: "bytes=0-9,20-29", wantRefuse: true},
		{raw: "bytes=", wantRefuse: true},
		{raw: "bytes=-0", wantRefuse: true},
	} {
		got, err := parseByteRange(tc.raw, size)
		switch {
		case tc.wantRefuse:
			if err == nil {
				t.Errorf("%q was accepted as %v, want a refusal", tc.raw, got)
			}
		case tc.wantWhole:
			if err != nil || got != nil {
				t.Errorf("%q gave %v, %v, want the whole file", tc.raw, got, err)
			}
		default:
			if err != nil {
				t.Errorf("%q was refused: %v", tc.raw, err)
				continue
			}
			if got == nil || got[0] != tc.wantStart || got[1] != tc.wantEnd {
				t.Errorf("%q gave %v, want [%d %d]", tc.raw, got, tc.wantStart, tc.wantEnd)
			}
		}
	}
}
