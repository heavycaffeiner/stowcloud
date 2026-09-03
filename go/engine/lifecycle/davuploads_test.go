//go:build linux

package lifecycle_test

import (
	"net/http"
	"net/http/httptest"
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

// A PROPFIND of the collection reports the members held, which is what a
// resuming client reads to learn what is left to send.
func TestTheCollectionListsTheChunksItHolds(t *testing.T) {
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
	for _, name := range []string{"/dav-uploads/tid-l/1", "/dav-uploads/tid-l/2"} {
		if !strings.Contains(w.Body.String(), "<D:href>"+name+"</D:href>") {
			t.Errorf("chunk %s is not listed: %s", name, w.Body.String())
		}
	}
	if strings.Contains(w.Body.String(), "/dav-uploads/tid-l/3") {
		t.Errorf("a chunk never sent is listed: %s", w.Body.String())
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

// The same transfer id from two callers must not reach one session. The alias
// is scoped by account, but the collection is also addressed against a
// destination, and the pair is what identifies the session.
func TestTheSameIDAgainstAnotherDestinationIsASeparateCollection(t *testing.T) {
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
	if w.Code == http.StatusCreated {
		t.Error("the same transfer id rebound to a second destination")
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
