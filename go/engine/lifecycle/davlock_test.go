//go:build linux

package lifecycle_test

import (
	"context"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// lockBody is the document a client sends to take an exclusive write lock.
const lockBody = `<?xml version="1.0"?>` +
	`<D:lockinfo xmlns:D="DAV:">` +
	`<D:lockscope><D:exclusive/></D:lockscope>` +
	`<D:locktype><D:write/></D:locktype>` +
	`<D:owner>someone@example.test</D:owner>` +
	`</D:lockinfo>`

// lock runs a LOCK and returns the recorder.
func (f *fixture) lock(t *testing.T, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	f.h.Lock(w, request("LOCK", "/files/"+path, body, headers), f.resolve(t, path))
	return w
}

// tokenOf reads the token out of a lock response's header.
func tokenOf(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	raw := strings.TrimSpace(w.Header().Get("Lock-Token"))
	raw = strings.TrimPrefix(raw, "<")
	raw = strings.TrimSuffix(raw, ">")
	if raw == "" {
		t.Fatalf("the response carries no Lock-Token: %v", w.Header())
	}
	return raw
}

// A lock is granted, and the token comes back in both the header and the body.
func TestLockGrantsAndReportsItsToken(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.write(t, "a.txt", "contents")

	w := f.lock(t, "a.txt", lockBody, nil)

	if w.Code != http.StatusOK {
		t.Fatalf("answered %d, want 200", w.Code)
	}
	token := tokenOf(t, w)

	body := w.Body.String()
	if !strings.Contains(body, token) {
		t.Errorf("the body does not carry the token: %s", body)
	}
	if !strings.Contains(body, "<D:exclusive/>") {
		t.Errorf("the body does not report the scope: %s", body)
	}
	if err := xml.Unmarshal([]byte(body), new(struct{})); err != nil {
		t.Errorf("the lock response does not parse: %v\n%s", err, body)
	}
}

// The lock actually holds: a second client's write is refused.
//
// This is the point of the whole method, and it is what a status alone cannot
// show. The stub guard is bypassed here by asking the real table.
func TestAGrantedLockRefusesAnotherWriter(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.write(t, "a.txt", "contents")

	w := f.lock(t, "a.txt", lockBody, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("the lock answered %d", w.Code)
	}
	token := tokenOf(t, w)

	ctx := context.Background()
	res := f.resolve(t, "a.txt")

	// Another account, submitting nothing.
	if err := f.real.Guard(ctx, 1, res.Path().String(), 2, nil); err == nil {
		t.Error("a locked resource admitted another writer")
	}
	// The holder, submitting the token it was given.
	if err := f.real.Guard(ctx, 1, res.Path().String(), 1, []string{token}); err != nil {
		t.Errorf("the holder's own write was refused: %v", err)
	}
}

// A shared lock is reported as shared. A client told "exclusive" for a shared
// lock believes it holds something nobody else can take.
func TestASharedLockIsReportedAsShared(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.write(t, "a.txt", "contents")

	const shared = `<?xml version="1.0"?>` +
		`<D:lockinfo xmlns:D="DAV:">` +
		`<D:lockscope><D:shared/></D:lockscope>` +
		`<D:locktype><D:write/></D:locktype>` +
		`</D:lockinfo>`

	w := f.lock(t, "a.txt", shared, nil)

	if w.Code != http.StatusOK {
		t.Fatalf("answered %d, want 200", w.Code)
	}
	if body := w.Body.String(); !strings.Contains(body, "<D:shared/>") {
		t.Errorf("a shared lock reported another scope: %s", body)
	}
}

// A LOCK on a URL that maps to nothing creates the resource and answers 201.
//
// RFC 4918's own rule, and how a client reserves a name before writing it.
// Answering 404 makes the reservation impossible, so a client that locks
// before every PUT could create nothing at all.
func TestLockingAnAbsentPathCreatesIt(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	w := f.lock(t, "reserved.txt", lockBody, nil)

	if w.Code != http.StatusCreated {
		t.Fatalf("answered %d, want 201", w.Code)
	}
	if !f.exists("reserved.txt") {
		t.Error("the lock-null resource was not created")
	}
	if got := f.read(t, "reserved.txt"); got != "" {
		t.Errorf("the created resource is not empty: %q", got)
	}
}

// The owner a client sent comes back, escaped.
//
// It is arbitrary text this server stored and hands to every later reader, so
// markup in it must not reach another client's parse.
func TestTheLockOwnerIsEchoedEscaped(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.write(t, "a.txt", "contents")

	const body = `<?xml version="1.0"?>` +
		`<D:lockinfo xmlns:D="DAV:">` +
		`<D:lockscope><D:exclusive/></D:lockscope>` +
		`<D:locktype><D:write/></D:locktype>` +
		`<D:owner>&lt;D:href&gt;http://evil/&lt;/D:href&gt;</D:owner>` +
		`</D:lockinfo>`

	w := f.lock(t, "a.txt", body, nil)

	if w.Code != http.StatusOK {
		t.Fatalf("answered %d, want 200", w.Code)
	}
	got := w.Body.String()
	if strings.Contains(got, "<D:href>http://evil/") {
		t.Errorf("a client's markup reached the response: %s", got)
	}
	if !strings.Contains(got, "&lt;D:href&gt;") {
		t.Errorf("the owner is missing or unescaped: %s", got)
	}
	if err := xml.Unmarshal([]byte(got), new(struct{})); err != nil {
		t.Errorf("the response does not parse: %v", err)
	}
}

// UNLOCK releases, and the resource is writable again afterwards.
func TestUnlockReleases(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.write(t, "a.txt", "contents")

	w := f.lock(t, "a.txt", lockBody, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("the lock answered %d", w.Code)
	}
	token := tokenOf(t, w)

	res := f.resolve(t, "a.txt")
	ctx := context.Background()
	if err := f.real.Guard(ctx, 1, res.Path().String(), 2, nil); err == nil {
		t.Fatal("the lock was not held before the unlock")
	}

	u := httptest.NewRecorder()
	f.h.Unlock(u, request("UNLOCK", "/files/a.txt", "", map[string]string{
		"Lock-Token": "<" + token + ">",
	}), res)

	if u.Code != http.StatusNoContent {
		t.Fatalf("the unlock answered %d, want 204", u.Code)
	}
	if err := f.real.Guard(ctx, 1, res.Path().String(), 2, nil); err != nil {
		t.Errorf("the resource stayed locked after the unlock: %v", err)
	}
}

// UNLOCK with no token is refused rather than silently doing nothing.
func TestUnlockWithoutATokenIsRefused(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.write(t, "a.txt", "contents")

	w := httptest.NewRecorder()
	f.h.Unlock(w, request("UNLOCK", "/files/a.txt", "", nil), f.resolve(t, "a.txt"))

	if w.Code == http.StatusNoContent {
		t.Fatal("an UNLOCK with no token reported success")
	}
}

// Only the holder may release. Otherwise one client drops a lock another is
// relying on.
func TestAnotherUserCannotUnlock(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.write(t, "a.txt", "contents")

	w := f.lock(t, "a.txt", lockBody, nil)
	token := tokenOf(t, w)

	// The service refuses a release by anyone but the principal that took it.
	if err := f.real.Release(context.Background(), token, 2); err == nil {
		t.Error("another account released a lock it does not hold")
	}
	if err := f.real.Release(context.Background(), token, 1); err != nil {
		t.Errorf("the holder's own release failed: %v", err)
	}
}

// Sending no body means extend what I hold, with the If header naming which
// lock that is.
func TestAnEmptyBodyRefreshesTheLock(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.write(t, "a.txt", "contents")

	first := f.lock(t, "a.txt", lockBody, nil)
	if first.Code != http.StatusOK {
		t.Fatalf("the lock answered %d", first.Code)
	}
	token := tokenOf(t, first)

	// The If header names the token the resource actually holds, so the
	// condition holds and the token counts as submitted.
	refreshed := f.lock(t, "a.txt", "", map[string]string{
		"If":      "(<" + token + ">)",
		"Timeout": "Second-600",
	})

	if refreshed.Code != http.StatusOK {
		t.Fatalf("the refresh answered %d, want 200", refreshed.Code)
	}
	if got := tokenOf(t, refreshed); got != token {
		t.Errorf("the refresh returned a different token: %q, want %q", got, token)
	}
	if !strings.Contains(refreshed.Body.String(), "Second-600") {
		t.Errorf("the refreshed lease is not reported: %s", refreshed.Body.String())
	}
}

// A refresh with no token is refused: there is nothing naming what to extend.
func TestARefreshWithoutATokenIsRefused(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.write(t, "a.txt", "contents")

	w := f.lock(t, "a.txt", "", nil)

	if w.Code == http.StatusOK {
		t.Fatal("a refresh naming no token reported success")
	}
}

// The requested lease is honoured, and an absurd one is clamped rather than
// stored: an unbounded lease on a durable table is a row a client pins for
// free.
func TestTheLeaseIsReportedAndClamped(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name   string
		header string
		want   string
	}{
		{"a modest lease is honoured", "Second-600", "Second-600"},
		{"an infinite lease is clamped", "Infinite", "Second-3600"},
		{"an absurd lease is clamped", "Second-99999999", "Second-3600"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			f.write(t, "a.txt", "contents")

			w := f.lock(t, "a.txt", lockBody, map[string]string{"Timeout": c.header})

			if w.Code != http.StatusOK {
				t.Fatalf("answered %d, want 200", w.Code)
			}
			if got := w.Body.String(); !strings.Contains(got, c.want) {
				t.Errorf("the lease is not %s: %s", c.want, got)
			}
		})
	}
}

// A malformed lockinfo body is refused rather than read as a default lock.
func TestAMalformedLockBodyIsRefused(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.write(t, "a.txt", "contents")

	for _, body := range []string{
		`<?xml version="1.0"?><D:notlockinfo xmlns:D="DAV:"/>`,
		`<?xml version="1.0"?><D:lockinfo xmlns:D="DAV:">`,
	} {
		w := f.lock(t, "a.txt", body, nil)
		if w.Code == http.StatusOK || w.Code == http.StatusCreated {
			t.Errorf("a malformed body answered %d: %s", w.Code, body)
		}
	}
}

// Depth one is not a lock depth. A lock covers a resource or a whole subtree,
// and there is no way to record anything between.
func TestDepthOneIsNotALockDepth(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.mkdir(t, "Docs")

	w := f.lock(t, "Docs", lockBody, map[string]string{"Depth": "1"})

	if w.Code == http.StatusOK || w.Code == http.StatusCreated {
		t.Errorf("depth one was accepted with %d", w.Code)
	}
}

// A depth-infinity lock over a collection covers what is inside it.
func TestADepthInfinityLockCoversTheSubtree(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.mkdir(t, "Docs")
	f.write(t, "Docs/inside.txt", "contents")

	w := f.lock(t, "Docs", lockBody, map[string]string{"Depth": "infinity"})
	if w.Code != http.StatusOK {
		t.Fatalf("answered %d, want 200", w.Code)
	}

	child := f.resolve(t, "Docs/inside.txt")
	if err := f.real.Guard(context.Background(), 1, child.Path().String(), 2, nil); err == nil {
		t.Error("a file under a depth-infinity lock admitted another writer")
	}
}

// A second exclusive lock is refused while the first is held.
func TestASecondLockIsRefused(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.write(t, "a.txt", "contents")

	if w := f.lock(t, "a.txt", lockBody, nil); w.Code != http.StatusOK {
		t.Fatalf("the first lock answered %d", w.Code)
	}

	second := f.lock(t, "a.txt", lockBody, nil)
	if second.Code == http.StatusOK || second.Code == http.StatusCreated {
		t.Errorf("a second exclusive lock was granted with %d", second.Code)
	}
	if second.Code != http.StatusLocked {
		t.Errorf("answered %d, want 423", second.Code)
	}
}

// A lock is reported by lockdiscovery on the next PROPFIND, which is how a
// client that did not take it learns the resource is held.
func TestALockAppearsInLockdiscovery(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.write(t, "a.txt", "contents")

	w := f.lock(t, "a.txt", lockBody, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("the lock answered %d", w.Code)
	}
	token := tokenOf(t, w)

	const query = `<?xml version="1.0"?>` +
		`<D:propfind xmlns:D="DAV:"><D:prop><D:lockdiscovery/></D:prop></D:propfind>`

	body := f.propfind(t, "a.txt", "0", query).Body.String()

	if !strings.Contains(body, token) {
		t.Errorf("lockdiscovery does not report the held lock: %s", body)
	}
	if !strings.Contains(body, "<D:exclusive/>") {
		t.Errorf("lockdiscovery does not report the scope: %s", body)
	}
}
