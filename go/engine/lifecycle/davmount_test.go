//go:build linux

package lifecycle_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/middleware"
	"github.com/heavycaffeiner/stowcloud/go/engine/lifecycle"
)

// mounted builds the mount, optionally with aliases.
func (f *fixture) mounted(aliases ...lifecycle.DavAlias) http.Handler {
	return f.engine.DavHandler(f.h, aliases)
}

// asDavUser attaches the principal the chain would have put there.
func asDavUser(r *http.Request) *http.Request {
	return r.WithContext(context.WithValue(r.Context(),
		middleware.KeyCredential, middleware.Principal{UserID: int64(testUser)}))
}

// through sends one request at the mount as an authenticated caller.
func (f *fixture) through(m http.Handler, method, url, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	m.ServeHTTP(w, asDavUser(request(method, url, body, nil)))
	return w
}

// A request with no principal is challenged rather than refused silently. A
// WebDAV client does not send a credential until it is asked.
func TestTheMountChallengesAnAnonymousRequest(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	m := f.mounted()

	w := httptest.NewRecorder()
	m.ServeHTTP(w, request("PROPFIND", "/dav/", allprop, nil))

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("answered %d, want 401", w.Code)
	}
	if got := w.Header().Get("WWW-Authenticate"); !strings.Contains(got, "Basic") {
		t.Errorf("no Basic challenge: %q", got)
	}
}

// Discovery answers before a credential, because it is how a client learns the
// server speaks the protocol at all.
func TestDiscoveryAnswersWithoutACredential(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	w := httptest.NewRecorder()
	f.mounted().ServeHTTP(w, request(http.MethodOptions, "/dav/", "", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("answered %d, want 200", w.Code)
	}
	if got := w.Header().Get("DAV"); got != "1, 2" {
		t.Errorf("the DAV header is %q", got)
	}
}

// An OPTIONS naming a path is not discovery. Answering it unauthenticated
// would report whether a file exists to anyone who asks.
func TestOptionsOnAPathStillNeedsACredential(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.write(t, "secret.txt", "contents")

	w := httptest.NewRecorder()
	f.mounted().ServeHTTP(w, request(http.MethodOptions, "/dav/secret.txt", "", nil))

	if w.Code != http.StatusUnauthorized {
		t.Errorf("an OPTIONS on a path answered %d without a credential, want 401", w.Code)
	}
}

// The virtual root lists the caller's shares. It has no directory behind it,
// so a resolution would refuse it, and a client that cannot list the root
// reports the server unreachable right after signing in.
func TestTheRootListsTheCallersShares(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	w := f.through(f.mounted(), "PROPFIND", "/dav/", allprop)

	if w.Code != http.StatusMultiStatus {
		t.Fatalf("answered %d, want 207: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "files") {
		t.Errorf("the share is not listed: %s", w.Body.String())
	}
}

// Depth: 0 on the root describes the root and nothing under it.
//
// A client sends it to ask what this collection is without paying for a
// listing. Answering with the shares anyway means every such probe drags the
// whole set back, and a client that asked for one response gets many.
func TestTheRootAtDepthZeroListsNoShares(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	w := httptest.NewRecorder()
	f.mounted().ServeHTTP(w, asDavUser(request("PROPFIND", "/dav/", allprop,
		map[string]string{"Depth": "0"})))

	if w.Code != http.StatusMultiStatus {
		t.Fatalf("answered %d, want 207", w.Code)
	}
	if n := strings.Count(w.Body.String(), "<D:response>"); n != 1 {
		t.Errorf("depth zero returned %d responses, want 1: %s", n, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "files") {
		t.Errorf("depth zero listed a share: %s", w.Body.String())
	}
}

// The root and every share in it are reported as collections.
//
// A client decides whether to descend from resourcetype. Reported as files
// they are never entered, so the account appears to hold nothing: the listing
// is correct and unusable at the same time.
func TestTheRootAndItsSharesAreCollections(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	body := f.through(f.mounted(), "PROPFIND", "/dav/", allprop).Body.String()

	// One for the root, one for the share.
	if n := strings.Count(body, "<D:collection"); n != 2 {
		t.Errorf("%d collections reported, want 2: %s", n, body)
	}
}

// Depth: 1 is what a client sends to list. The shares come back.
func TestTheRootAtDepthOneListsTheShares(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	w := httptest.NewRecorder()
	f.mounted().ServeHTTP(w, asDavUser(request("PROPFIND", "/dav/", allprop,
		map[string]string{"Depth": "1"})))

	if w.Code != http.StatusMultiStatus {
		t.Fatalf("answered %d, want 207", w.Code)
	}
	if !strings.Contains(w.Body.String(), "files") {
		t.Errorf("depth one did not list the share: %s", w.Body.String())
	}
}

// Both spellings of the root name the same thing. A client may or may not send
// the trailing slash.
func TestTheRootIsTheSameWithoutItsSlash(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	if got := f.through(f.mounted(), "PROPFIND", "/dav", allprop); got.Code != http.StatusMultiStatus {
		t.Errorf("the root without a slash answered %d, want 207", got.Code)
	}
}

// A path under the mount reaches the file it names.
func TestTheMountResolvesAPath(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.write(t, "a.txt", "contents")

	w := f.through(f.mounted(), http.MethodGet, "/dav/files/a.txt", "")

	if w.Code != http.StatusOK {
		t.Fatalf("answered %d, want 200: %s", w.Code, w.Body.String())
	}
	if w.Body.String() != "contents" {
		t.Errorf("served %q", w.Body.String())
	}
}

// A name holding a percent survives the mount.
//
// The URL's path arrives already decoded, and the splitter decodes each
// segment itself. Reading the decoded form here decodes twice, and the second
// pass reads the file's own percent as a malformed escape: the file becomes
// unreachable rather than merely awkward.
func TestAPercentInANameIsNotDecodedTwice(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.write(t, "100%.txt", "contents")

	w := f.through(f.mounted(), http.MethodGet, "/dav/files/100%25.txt", "")

	if w.Code != http.StatusOK {
		t.Fatalf("answered %d, want 200: %s", w.Code, w.Body.String())
	}
	if w.Body.String() != "contents" {
		t.Errorf("served %q", w.Body.String())
	}
}

// An encoded separator does not become a path boundary. This is the classic
// way a path-mapping layer is walked out of its root.
func TestAnEncodedSeparatorIsRefused(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	w := f.through(f.mounted(), http.MethodGet, "/dav/files/a%2f..%2f..%2fetc", "")

	if w.Code == http.StatusOK {
		t.Errorf("an encoded separator was accepted: %s", w.Body.String())
	}
}

// A COPY names its destination in a header, and the mount resolves it.
func TestTheMountResolvesADestination(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.write(t, "a.txt", "contents")

	w := httptest.NewRecorder()
	r := asDavUser(request("COPY", "/dav/files/a.txt", "", map[string]string{
		"Destination": "/dav/files/b.txt",
	}))
	f.mounted().ServeHTTP(w, r)

	if w.Code != http.StatusCreated {
		t.Fatalf("answered %d, want 201: %s", w.Code, w.Body.String())
	}
	if got := f.read(t, "b.txt"); got != "contents" {
		t.Errorf("the copy holds %q", got)
	}
}

// A destination naming another host is refused rather than having its host
// quietly dropped, which would copy to the same path on this server.
func TestAForeignDestinationIsRefused(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.write(t, "a.txt", "contents")

	w := httptest.NewRecorder()
	r := asDavUser(request("COPY", "/dav/files/a.txt", "", map[string]string{
		"Destination": "https://elsewhere.example/dav/files/b.txt",
	}))
	f.mounted().ServeHTTP(w, r)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("answered %d, want 502", w.Code)
	}
	if f.exists("b.txt") {
		t.Error("a foreign destination still wrote a file here")
	}
}

// An absolute destination naming this server is accepted. Clients send one.
func TestAnAbsoluteDestinationOnThisHostWorks(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.write(t, "a.txt", "contents")

	w := httptest.NewRecorder()
	r := asDavUser(request("COPY", "/dav/files/a.txt", "", map[string]string{
		"Destination": "http://example.test/dav/files/b.txt",
	}))
	r.Host = "example.test"
	f.mounted().ServeHTTP(w, r)

	if w.Code != http.StatusCreated {
		t.Fatalf("answered %d, want 201: %s", w.Code, w.Body.String())
	}
}

// An alias addresses the same tree under another prefix, dropping the segments
// that name something other than a file.
func TestAnAliasReachesTheSameTree(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.write(t, "a.txt", "contents")
	m := f.mounted(lifecycle.DavAlias{Prefix: "/remote.php/dav/files", DropSegments: 1})

	w := f.through(m, http.MethodGet, "/remote.php/dav/files/someaccount/files/a.txt", "")

	if w.Code != http.StatusOK {
		t.Fatalf("answered %d, want 200: %s", w.Code, w.Body.String())
	}
	if w.Body.String() != "contents" {
		t.Errorf("served %q", w.Body.String())
	}
}

// The account segment in an alias is dropped rather than trusted. Resolution
// runs against the caller's own roots, so naming another account reaches that
// caller's tree and not the named one.
func TestAnAliasIgnoresTheAccountItNames(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.write(t, "a.txt", "contents")
	m := f.mounted(lifecycle.DavAlias{Prefix: "/remote.php/dav/files", DropSegments: 1})

	mine := f.through(m, http.MethodGet, "/remote.php/dav/files/me/files/a.txt", "")
	theirs := f.through(m, http.MethodGet, "/remote.php/dav/files/somebodyelse/files/a.txt", "")

	if mine.Code != http.StatusOK || theirs.Code != http.StatusOK {
		t.Fatalf("the two answered %d and %d, want both 200", mine.Code, theirs.Code)
	}
	if mine.Body.String() != theirs.Body.String() {
		t.Error("the account segment changed which tree was served")
	}
}

// A destination sent through an alias is rewritten the same way the request
// path is. Otherwise a COPY through an alias resolves nowhere.
func TestADestinationThroughAnAliasIsRewritten(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.write(t, "a.txt", "contents")
	m := f.mounted(lifecycle.DavAlias{Prefix: "/remote.php/dav/files", DropSegments: 1})

	w := httptest.NewRecorder()
	r := asDavUser(request("COPY", "/remote.php/dav/files/me/files/a.txt", "", map[string]string{
		"Destination": "/remote.php/dav/files/me/files/b.txt",
	}))
	m.ServeHTTP(w, r)

	if w.Code != http.StatusCreated {
		t.Fatalf("answered %d, want 201: %s", w.Code, w.Body.String())
	}
	if got := f.read(t, "b.txt"); got != "contents" {
		t.Errorf("the copy holds %q", got)
	}
}

// A write method is checked as a write at resolution, before the operation
// starts, so a reader is refused rather than stopped part way through.
func TestAWriteMethodIsCheckedAsAWrite(t *testing.T) {
	t.Parallel()
	f := newFixtureReadOnly(t)
	f.write(t, "a.txt", "contents")

	w := f.through(f.mounted(), http.MethodPut, "/dav/files/a.txt", "replacement")

	if w.Code == http.StatusCreated || w.Code == http.StatusNoContent {
		t.Fatalf("a read-only caller wrote: %d", w.Code)
	}
	if got := f.read(t, "a.txt"); got != "contents" {
		t.Errorf("the file changed to %q", got)
	}
}

// A caller denied a write on a file they can read is told they were denied,
// rather than that the file is missing.
//
// The refusal comes from the operation, which checks its own requirement
// against the resolution it was handed. Answering 404 for a file the same
// caller can list and read would be untrue, and a client acts on it by
// retrying as though the parent were gone.
func TestADeniedWriteIsNotReportedAsAbsence(t *testing.T) {
	t.Parallel()
	f := newFixtureReadOnly(t)
	f.write(t, "a.txt", "contents")
	m := f.mounted()

	// The premise: this caller can read it, so it is not hidden from them.
	if got := f.through(m, http.MethodGet, "/dav/files/a.txt", ""); got.Code != http.StatusOK {
		t.Fatalf("the reader cannot read the file: %d", got.Code)
	}

	for _, c := range []struct{ method, body string }{
		{http.MethodPut, "replacement"},
		{"PROPPATCH", setOne},
		{"LOCK", lockBody},
		{http.MethodDelete, ""},
	} {
		if got := f.through(m, c.method, "/dav/files/a.txt", c.body); got.Code != http.StatusForbidden {
			t.Errorf("%s answered %d, want 403", c.method, got.Code)
		}
	}
}

// A path the caller genuinely cannot see stays hidden. The rule above admits
// existence only to a caller who already learned it by reading.
func TestAnUnreadablePathIsStillHidden(t *testing.T) {
	t.Parallel()
	f := newFixtureReadOnly(t)

	w := f.through(f.mounted(), http.MethodPut, "/dav/nosuchshare/a.txt", "x")

	if w.Code != http.StatusNotFound {
		t.Errorf("answered %d, want 404", w.Code)
	}
}
