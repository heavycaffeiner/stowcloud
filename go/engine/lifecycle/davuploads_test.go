//go:build linux

package lifecycle_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The chunked upload collection, driven through the mount against the real
// upload engine.
//
// A MKCOL opens a session, a PUT of a numerically named member contributes a
// chunk, and a MOVE of the collection publishes it. What matters at this tier
// is that a client's whole transfer works and that the assembled file is what
// the chunks said it should be.

// throughHeaders sends with headers, which the collection reads rather than
// a body.
func (f *fixture) throughHeaders(
	m http.Handler, method, url, body string, headers map[string]string,
) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	m.ServeHTTP(w, asDavUser(request(method, url, body, headers)))
	return w
}

const uploadRoot = "/dav-uploads"

// A whole transfer: two chunks in, one file out, contents in name order.
func TestAChunkedUploadAssemblesInNameOrder(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	m := f.mounted()

	if w := f.throughHeaders(m, "MKCOL", uploadRoot+"/tid-a", "", map[string]string{
		"Destination":     "/dav/files/out.bin",
		"OC-Total-Length": "12",
	}); w.Code != http.StatusCreated {
		t.Fatalf("opening the session answered %d: %s", w.Code, w.Body.String())
	}
	if w := f.throughHeaders(m, http.MethodPut, uploadRoot+"/tid-a/2", "second", nil); w.Code != http.StatusCreated {
		t.Fatalf("chunk 2 answered %d", w.Code)
	}
	if w := f.throughHeaders(m, http.MethodPut, uploadRoot+"/tid-a/1", "first-", nil); w.Code != http.StatusCreated {
		t.Fatalf("chunk 1 answered %d", w.Code)
	}
	if w := f.throughHeaders(m, "MOVE", uploadRoot+"/tid-a", "", map[string]string{
		"Destination":     "/dav/files/out.bin",
		"OC-Total-Length": "12",
	}); w.Code != http.StatusCreated {
		t.Fatalf("assembling answered %d: %s", w.Code, w.Body.String())
	}

	if got := f.read(t, "out.bin"); got != "first-second" {
		t.Errorf("the assembled file holds %q, want %q", got, "first-second")
	}
}

// The assembled response carries a validator. A sync client hard-fails the
// item without one even on a success, so a 201 with no ETag is a transfer the
// client itself reports as failed.
func TestTheAssembledResponseCarriesAnETag(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	m := f.mounted()

	f.throughHeaders(m, "MKCOL", uploadRoot+"/tid-e", "", map[string]string{
		"Destination":     "/dav/files/out.bin",
		"OC-Total-Length": "8",
	})
	f.throughHeaders(m, http.MethodPut, uploadRoot+"/tid-e/1", "contents", nil)
	w := f.throughHeaders(m, "MOVE", uploadRoot+"/tid-e", "", map[string]string{
		"Destination":     "/dav/files/out.bin",
		"OC-Total-Length": "8",
	})

	if w.Code != http.StatusCreated {
		t.Fatalf("assembling answered %d", w.Code)
	}
	if w.Header().Get("ETag") == "" {
		t.Error("assembling answered with no ETag")
	}
}

// The declared length is what assembly publishes. A short transfer must not
// silently become a truncated file.
func TestAssemblingShortOfTheDeclaredLengthIsRefused(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	m := f.mounted()

	f.throughHeaders(m, "MKCOL", uploadRoot+"/tid-s", "", map[string]string{
		"Destination":     "/dav/files/out.bin",
		"OC-Total-Length": "100",
	})
	f.throughHeaders(m, http.MethodPut, uploadRoot+"/tid-s/1", "short", nil)
	w := f.throughHeaders(m, "MOVE", uploadRoot+"/tid-s", "", map[string]string{
		"Destination":     "/dav/files/out.bin",
		"OC-Total-Length": "100",
	})

	if w.Code == http.StatusCreated || w.Code == http.StatusNoContent {
		t.Fatalf("a short transfer published: %d", w.Code)
	}
	if f.exists("out.bin") {
		t.Error("a short transfer still wrote the file")
	}
}

// A member name with a leading zero is refused, so "01" and "1" cannot both
// mean the same chunk and one silently replace the other.
func TestAPaddedChunkNameIsRefused(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	m := f.mounted()

	f.throughHeaders(m, "MKCOL", uploadRoot+"/tid-p", "", map[string]string{
		"Destination":     "/dav/files/out.bin",
		"OC-Total-Length": "1",
	})
	w := f.throughHeaders(m, http.MethodPut, uploadRoot+"/tid-p/01", "x", nil)

	if w.Code != http.StatusBadRequest {
		t.Errorf("a padded name answered %d, want 400", w.Code)
	}
}

// A PROPFIND of the collection reports what the client needs to resume: how
// many bytes are held and the highest member name holding them.
//
// The client sums the lengths it is shown to decide the offset to send from,
// and takes the highest name to decide what to call the next chunk. Members
// already merged into the part file have no boundaries left on disk, so they
// are reported as the one run they now are; what has to be true is the pair
// of figures the client derives, not how many responses carry them.
func TestTheCollectionReportsWhatAResumeNeeds(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	m := f.mounted()

	f.throughHeaders(m, "MKCOL", uploadRoot+"/tid-l", "", map[string]string{
		"Destination":     "/dav/files/out.bin",
		"OC-Total-Length": "6",
	})
	f.throughHeaders(m, http.MethodPut, uploadRoot+"/tid-l/1", "one", nil)
	f.throughHeaders(m, http.MethodPut, uploadRoot+"/tid-l/2", "two", nil)

	w := f.throughHeaders(m, "PROPFIND", uploadRoot+"/tid-l", allprop, map[string]string{
		"Destination": "/dav/files/out.bin",
	})

	if w.Code != http.StatusMultiStatus {
		t.Fatalf("answered %d, want 207: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()

	// Six bytes arrived, so that is what the listing has to account for. A
	// listing that named the members without their lengths summed to zero and
	// had the client resend from the start under a later name, which repeats
	// the opening bytes of the assembled file.
	held := 0
	for _, n := range regexp.MustCompile(
		`<D:getcontentlength>(\d+)</D:getcontentlength>`).FindAllStringSubmatch(body, -1) {
		size, cerr := strconv.Atoi(n[1])
		if cerr != nil {
			t.Fatalf("a member length does not parse: %v", cerr)
		}
		held += size
	}
	if held != 6 {
		t.Errorf("the listing accounts for %d bytes, want 6: %s", held, body)
	}
	if !strings.Contains(body, "<D:href>/dav-uploads/tid-l/2</D:href>") {
		t.Errorf("the listing does not name the highest member held: %s", body)
	}
	if strings.Contains(body, "/dav-uploads/tid-l/3") {
		t.Errorf("a chunk never sent is listed: %s", body)
	}
}

// Discarding abandons the session. Nothing publishes, and the id stops
// resolving.
func TestDiscardingAbandonsTheSession(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	m := f.mounted()

	f.throughHeaders(m, "MKCOL", uploadRoot+"/tid-d", "", map[string]string{
		"Destination":     "/dav/files/out.bin",
		"OC-Total-Length": "6",
	})
	f.through(m, http.MethodPut, uploadRoot+"/tid-d/1", "one")

	if w := f.throughHeaders(m, http.MethodDelete, uploadRoot+"/tid-d", "", map[string]string{
		"Destination": "/dav/files/out.bin",
	}); w.Code != http.StatusNoContent {
		t.Fatalf("discarding answered %d, want 204", w.Code)
	}

	// The session is gone, so a chunk that arrives now has nowhere to go.
	w := f.throughHeaders(m, http.MethodPut, uploadRoot+"/tid-d/2", "two", map[string]string{
		"Destination": "/dav/files/out.bin",
	})
	if w.Code == http.StatusCreated {
		t.Error("a discarded session accepted another chunk")
	}
	if f.exists("out.bin") {
		t.Error("a discarded session published a file")
	}
}

// Reopening a collection whose session is no longer receiving starts a fresh
// one under the same name.
//
// The alias outlives the session it names: aborting and expiring both leave
// the row, and only a discard removes it. Reusing the alias without looking
// at the session answered 201 to the open and then refused every chunk that
// followed, which tells the client the collection is ready while nothing can
// be written into it. The name a client keeps addressing has to go on
// working.
func TestReopeningADeadSessionStartsAFreshOne(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	m := f.mounted()

	const dest = "/dav/files/revived.bin"
	if w := f.throughHeaders(m, "MKCOL", uploadRoot+"/tid-dead", "", map[string]string{
		"Destination": dest,
	}); w.Code != http.StatusCreated {
		t.Fatalf("the first open answered %d", w.Code)
	}

	// Abandon the session behind the mount's back, which is what an expiry
	// sweep leaves: the alias stays, the session stops receiving.
	alias, lerr := f.engine.Upload.LookupAlias(context.Background(), "tid-dead", testUser)
	if lerr != nil {
		t.Fatalf("looking up the alias: %v", lerr)
	}
	if aerr := f.engine.Upload.Abort(context.Background(), alias.Session, testUser); aerr != nil {
		t.Fatalf("aborting: %v", aerr)
	}

	if w := f.throughHeaders(m, "MKCOL", uploadRoot+"/tid-dead", "", map[string]string{
		"Destination": dest,
	}); w.Code != http.StatusCreated {
		t.Fatalf("reopening a dead session answered %d", w.Code)
	}

	// The point of the reopen: the collection actually takes bytes now.
	if w := f.throughHeaders(m, http.MethodPut, uploadRoot+"/tid-dead/1", "revived",
		map[string]string{"Destination": dest}); w.Code != http.StatusCreated {
		t.Fatalf("a chunk after the reopen answered %d, so the open was a lie", w.Code)
	}
	// The collection itself, not the vendor's ".file" member: that spelling is
	// the compatibility layer's vocabulary and this file runs without the tag
	// that supplies it.
	if w := f.throughHeaders(m, "MOVE", uploadRoot+"/tid-dead", "", map[string]string{
		"Destination": dest, "OC-Total-Length": "7",
	}); w.Code != http.StatusCreated {
		t.Fatalf("publishing answered %d", w.Code)
	}
	if got := f.read(t, "revived.bin"); got != "revived" {
		t.Errorf("the revived transfer holds %q", got)
	}
}

// A transfer id the account never opened is not somebody else's session. An
// id owned by a different account resolves exactly as one that never existed.
func TestAnotherAccountsCollectionDoesNotResolve(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	m := f.mounted()

	// This account never opened tid-x. Sending a chunk at it is a guess, and
	// the answer must not differ from a session that never existed at all.
	if w := f.throughHeaders(m, http.MethodPut, uploadRoot+"/tid-x/1", "one", map[string]string{
		"Destination": "/dav/files/out.bin",
	}); w.Code == http.StatusCreated {
		t.Error("a session this account never opened accepted a chunk")
	}
}

// The same transfer id opened against a second destination starts a fresh
// collection rather than adopting the first one's bytes.
//
// The client picks the transfer id, and both reference clients derive it from
// the file rather than the destination: Android from the file's MD5, the
// desktop from a stored transfer number it reuses across retries. Refusing the
// second open answered 405, which the Android operation reads as "the folder
// is already there" and the desktop treats as a hard failure, so a transfer
// that had merely been retargeted could never start. What must not happen is
// the opposite mistake: adopting the session would publish one file's bytes at
// the other's destination, so the old session is abandoned and its members go
// with it.
func TestTheSameIDAgainstAnotherDestinationStartsAFreshCollection(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	m := f.mounted()

	f.throughHeaders(m, "MKCOL", uploadRoot+"/tid-c", "", map[string]string{
		"Destination":     "/dav/files/one.bin",
		"OC-Total-Length": "5",
	})
	f.through(m, http.MethodPut, uploadRoot+"/tid-c/1", "first")

	// The same id, opened against a different destination.
	w := f.throughHeaders(m, "MKCOL", uploadRoot+"/tid-c", "", map[string]string{
		"Destination":     "/dav/files/two.bin",
		"OC-Total-Length": "5",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("retargeting the transfer id answered %d, want 201", w.Code)
	}

	// The bytes written under the first destination are gone with the session
	// they belonged to: a listing of the reopened collection holds no member,
	// so nothing the first transfer sent can be published at the second.
	listed := f.throughHeaders(m, "PROPFIND", uploadRoot+"/tid-c", allprop, map[string]string{
		"Destination": "/dav/files/two.bin",
		"Depth":       "1",
	})
	if listed.Code != http.StatusMultiStatus {
		t.Fatalf("listing the reopened collection answered %d, want 207: %s", listed.Code, listed.Body.String())
	}
	if strings.Contains(listed.Body.String(), "/tid-c/1") {
		t.Errorf("the first destination's chunk survived the retarget: %s", listed.Body.String())
	}
}

// A session opened and assembled against one destination publishes there, not
// wherever a later request's destination says.
func TestAssemblyLandsAtTheDestinationItOpenedWith(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	m := f.mounted()

	f.throughHeaders(m, "MKCOL", uploadRoot+"/tid-o", "", map[string]string{
		"Destination":     "/dav/files/landing.bin",
		"OC-Total-Length": "7",
	})
	f.throughHeaders(m, http.MethodPut, uploadRoot+"/tid-o/1", "payload", map[string]string{
		"Destination": "/dav/files/landing.bin",
	})
	w := f.throughHeaders(m, "MOVE", uploadRoot+"/tid-o", "", map[string]string{
		"Destination":     "/dav/files/landing.bin",
		"OC-Total-Length": "7",
	})

	if w.Code != http.StatusCreated {
		t.Fatalf("assembling answered %d: %s", w.Code, w.Body.String())
	}
	if got := f.read(t, "landing.bin"); got != "payload" {
		t.Errorf("the landing file holds %q", got)
	}
}

// The same transfer id against another share is not the same collection.
//
// The alias is scoped by account, so both shares resolve the same session.
// What stops a chunk meant for one from landing in the other's spool is the
// share check, and what shows it matters is the original session: assembly
// after a stray chunk publishes a file holding whatever arrived. A session
// that received nothing must assemble nothing.
func TestTheSameIDOnAnotherShareIsNotTheCollection(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	m := f.mounted()

	// Opened against the first share.
	if w := f.throughHeaders(m, "MKCOL", uploadRoot+"/tid-b", "", map[string]string{
		"Destination":     "/dav/files/out.bin",
		"OC-Total-Length": "5",
	}); w.Code != http.StatusCreated {
		t.Fatalf("opening answered %d", w.Code)
	}

	// The same id, addressed under the second share. The share scopes the
	// collection, so this resolves as no collection at all.
	w := f.throughHeaders(m, http.MethodPut, uploadRoot+"/tid-b/1", "stray", map[string]string{
		"Destination": "/dav/safe/out.bin",
	})
	if w.Code != http.StatusNotFound {
		t.Fatalf("the stray chunk answered %d, want 404", w.Code)
	}

	// The original session takes no chunks of its own, so assembling it must
	// refuse for incompleteness. Publishing here would mean the stray chunk
	// crossed the share boundary and landed in this session's spool.
	if w := f.throughHeaders(m, "MOVE", uploadRoot+"/tid-b", "", map[string]string{
		"Destination":     "/dav/files/out.bin",
		"OC-Total-Length": "5",
	}); w.Code == http.StatusCreated || w.Code == http.StatusNoContent {
		t.Fatalf("a session that received nothing published %d", w.Code)
	}
	if f.exists("out.bin") {
		t.Error("the stray chunk crossed into the first share's session")
	}
}

// A deployment without an upload engine answers 405 on the collection rather
// than half-serving it.
//
// The header names remain, since the test above is about the engine alone: a
// partly configured surface would be the worse of the two answers.
func TestTheCollectionWithoutAnEngineIsRefused(t *testing.T) {
	t.Parallel()
	f := newFixtureNoUploads(t)

	w := f.throughHeaders(f.mounted(), "MKCOL", uploadRoot+"/tid-n", "", map[string]string{
		"Destination": "/dav/files/out.bin",
	})

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("answered %d, want 405", w.Code)
	}
}
