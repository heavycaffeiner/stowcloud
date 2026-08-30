//go:build linux

package dav_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A GET returns the bytes, with the headers a client needs to cache and to
// resume.
func TestGetReturnsTheFileAndItsValidators(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.write(t, "notes.txt", "hello")

	w := httptest.NewRecorder()
	f.h.Get(w, request("GET", "/files/notes.txt", "", nil), f.resolve(t, "notes.txt"), true)

	if w.Code != http.StatusOK {
		t.Fatalf("the response is %d, want 200", w.Code)
	}
	if got := w.Body.String(); got != "hello" {
		t.Errorf("the body is %q", got)
	}
	if w.Header().Get("ETag") == "" {
		t.Error("no validator was sent, so a client cannot revalidate")
	}
	if w.Header().Get("Accept-Ranges") != "bytes" {
		t.Error("ranges were not advertised, so a resuming client will not try one")
	}
	if got := w.Header().Get("Content-Length"); got != "5" {
		t.Errorf("the length is %q, want 5", got)
	}
}

// HEAD sends what GET would and no body, since that is the whole point: a
// client reads the headers to decide whether to fetch.
func TestHeadMatchesGetWithoutTheBody(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.write(t, "notes.txt", "hello")

	get := httptest.NewRecorder()
	f.h.Get(get, request("GET", "/files/notes.txt", "", nil), f.resolve(t, "notes.txt"), true)

	head := httptest.NewRecorder()
	f.h.Get(head, request("HEAD", "/files/notes.txt", "", nil), f.resolve(t, "notes.txt"), false)

	if head.Code != get.Code {
		t.Errorf("HEAD answered %d and GET %d", head.Code, get.Code)
	}
	if head.Body.Len() != 0 {
		t.Errorf("HEAD returned a body of %d bytes", head.Body.Len())
	}
	for _, h := range []string{"ETag", "Content-Length", "Content-Type", "Last-Modified"} {
		if head.Header().Get(h) != get.Header().Get(h) {
			t.Errorf("%s differs: HEAD %q, GET %q", h, head.Header().Get(h), get.Header().Get(h))
		}
	}
}

// RFC 4918 defines no body for a GET of a collection, so it is refused rather
// than answered with an invented index page.
func TestGetOfACollectionIsRefused(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.mkdir(t, "Docs")

	w := httptest.NewRecorder()
	f.h.Get(w, request("GET", "/files/Docs", "", nil), f.resolve(t, "Docs"), true)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("the response is %d, want 405", w.Code)
	}
	if w.Header().Get("Allow") == "" {
		t.Error("a 405 named no alternative methods")
	}
}

// If-None-Match answers 304 with no body, which is what makes a cache useful.
func TestIfNoneMatchRevalidates(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.write(t, "notes.txt", "hello")

	first := httptest.NewRecorder()
	f.h.Get(first, request("GET", "/files/notes.txt", "", nil), f.resolve(t, "notes.txt"), true)
	etag := first.Header().Get("ETag")

	second := httptest.NewRecorder()
	f.h.Get(second, request("GET", "/files/notes.txt", "", map[string]string{
		"If-None-Match": etag,
	}), f.resolve(t, "notes.txt"), true)

	if second.Code != http.StatusNotModified {
		t.Fatalf("a matching validator answered %d, want 304", second.Code)
	}
	if second.Body.Len() != 0 {
		t.Error("a 304 carried a body")
	}
	// A star matches any existing representation.
	star := httptest.NewRecorder()
	f.h.Get(star, request("GET", "/files/notes.txt", "", map[string]string{
		"If-None-Match": "*",
	}), f.resolve(t, "notes.txt"), true)
	if star.Code != http.StatusNotModified {
		t.Errorf("a star answered %d, want 304", star.Code)
	}
}

// A validator that does not match is not a revalidation, so the body is sent.
func TestAStaleValidatorGetsTheBody(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.write(t, "notes.txt", "hello")

	w := httptest.NewRecorder()
	f.h.Get(w, request("GET", "/files/notes.txt", "", map[string]string{
		"If-None-Match": `"something-else"`,
	}), f.resolve(t, "notes.txt"), true)

	if w.Code != http.StatusOK {
		t.Errorf("a stale validator answered %d, want 200", w.Code)
	}
	if w.Body.String() != "hello" {
		t.Errorf("the body is %q", w.Body.String())
	}
}

// A range request returns that range and says so. Answering the whole file
// with a 200 is what a resuming download reads as the transfer restarting.
func TestARangeReturnsOnlyThatRange(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.write(t, "digits.txt", "0123456789")

	for _, c := range []struct {
		header string
		want   string
		rang   string
	}{
		{"bytes=0-3", "0123", "bytes 0-3/10"},
		{"bytes=4-", "456789", "bytes 4-9/10"},
		{"bytes=-3", "789", "bytes 7-9/10"},
		// Past the end is clamped rather than refused, per RFC 9110.
		{"bytes=8-99", "89", "bytes 8-9/10"},
	} {
		t.Run(c.header, func(t *testing.T) {
			t.Parallel()
			w := httptest.NewRecorder()
			f.h.Get(w, request("GET", "/files/digits.txt", "", map[string]string{
				"Range": c.header,
			}), f.resolve(t, "digits.txt"), true)

			if w.Code != http.StatusPartialContent {
				t.Fatalf("answered %d, want 206", w.Code)
			}
			if got := w.Body.String(); got != c.want {
				t.Errorf("the body is %q, want %q", got, c.want)
			}
			if got := w.Header().Get("Content-Range"); got != c.rang {
				t.Errorf("the range is %q, want %q", got, c.rang)
			}
		})
	}
}

// An unsatisfiable range answers 416 and reports the real size, which is what
// lets a client that guessed wrong correct itself.
func TestAnUnsatisfiableRangeReportsTheSize(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.write(t, "digits.txt", "0123456789")

	for _, header := range []string{"bytes=99-", "bytes=5-2", "bytes=abc", "bytes=0-3,5-7"} {
		w := httptest.NewRecorder()
		f.h.Get(w, request("GET", "/files/digits.txt", "", map[string]string{
			"Range": header,
		}), f.resolve(t, "digits.txt"), true)

		if w.Code != http.StatusRequestedRangeNotSatisfiable {
			t.Errorf("%s answered %d, want 416", header, w.Code)
		}
		if got := w.Header().Get("Content-Range"); got != "bytes */10" {
			t.Errorf("%s reported %q, want bytes */10", header, got)
		}
	}
}

// A PUT of a new file is 201 and a PUT over an existing one is 204. A client
// distinguishes creation from replacement by exactly this.
func TestPutSeparatesCreationFromReplacement(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	created := httptest.NewRecorder()
	f.h.Put(created, request("PUT", "/files/new.txt", "first", nil), f.resolve(t, "new.txt"))
	if created.Code != http.StatusCreated {
		t.Fatalf("creating answered %d, want 201", created.Code)
	}
	if got := f.read(t, "new.txt"); got != "first" {
		t.Errorf("the file holds %q", got)
	}

	replaced := httptest.NewRecorder()
	f.h.Put(replaced, request("PUT", "/files/new.txt", "second", nil), f.resolve(t, "new.txt"))
	if replaced.Code != http.StatusNoContent {
		t.Errorf("replacing answered %d, want 204", replaced.Code)
	}
	if got := f.read(t, "new.txt"); got != "second" {
		t.Errorf("the file holds %q after replacement", got)
	}
	if replaced.Header().Get("ETag") == "" {
		t.Error("a write returned no validator, so a client cannot make its next write conditional")
	}
}

// A PUT onto a collection is refused. Honouring it would mean removing the
// collection and everything in it, which the client did not ask for.
func TestPutOntoACollectionIsRefused(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.mkdir(t, "Docs")

	w := httptest.NewRecorder()
	f.h.Put(w, request("PUT", "/files/Docs", "body", nil), f.resolve(t, "Docs"))

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("answered %d, want 405", w.Code)
	}
	if !f.exists("Docs") {
		t.Error("the collection was removed by a refused PUT")
	}
}

// A weak validator in If-Match is refused rather than passed through: the
// core's rule is that a weak tag never satisfies a precondition, so accepting
// one would be a check that cannot pass dressed as one that might.
func TestAWeakValidatorCannotGuardAWrite(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.write(t, "notes.txt", "hello")

	w := httptest.NewRecorder()
	f.h.Put(w, request("PUT", "/files/notes.txt", "replaced", map[string]string{
		"If-Match": `W/"abc"`,
	}), f.resolve(t, "notes.txt"))

	if w.Code != http.StatusPreconditionFailed {
		t.Errorf("a weak If-Match answered %d, want 412", w.Code)
	}
	if got := f.read(t, "notes.txt"); got != "hello" {
		t.Errorf("the write landed anyway: %q", got)
	}
}

// MKCOL creates a collection.
func TestMkcolCreatesACollection(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	w := httptest.NewRecorder()
	f.h.Mkcol(w, request("MKCOL", "/files/New", "", nil), f.resolve(t, "New"))

	if w.Code != http.StatusCreated {
		t.Fatalf("answered %d, want 201", w.Code)
	}
	if !f.exists("New") {
		t.Error("the collection was not created")
	}
}

// A MKCOL with a body is 415: RFC 4918 defines no body format, so honouring
// one would be inventing a meaning for it.
func TestMkcolWithABodyIsRefused(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	w := httptest.NewRecorder()
	f.h.Mkcol(w, request("MKCOL", "/files/New", "<anything/>", nil), f.resolve(t, "New"))

	if w.Code != http.StatusUnsupportedMediaType {
		t.Errorf("answered %d, want 415", w.Code)
	}
	if f.exists("New") {
		t.Error("a refused MKCOL created the collection anyway")
	}
}

// A missing parent is 409, not 404.
//
// The difference is what a client branches on: 404 says the target is absent,
// which is the point of creating it, and 409 says the parent is. A client that
// creates parents on demand has no reason to act on a 404.
func TestMkcolWithNoParentIsAConflict(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	w := httptest.NewRecorder()
	f.h.Mkcol(w, request("MKCOL", "/files/a/b", "", nil), f.resolve(t, "a/b"))

	if w.Code != http.StatusConflict {
		t.Errorf("answered %d, want 409", w.Code)
	}
}

// DELETE removes the resource and answers 204.
func TestDeleteRemovesTheResource(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.write(t, "gone.txt", "bye")

	w := httptest.NewRecorder()
	f.h.Delete(w, request("DELETE", "/files/gone.txt", "", nil), f.resolve(t, "gone.txt"))

	if w.Code != http.StatusNoContent {
		t.Fatalf("answered %d, want 204", w.Code)
	}
	if f.exists("gone.txt") {
		t.Error("the file survived its own deletion")
	}
}

// Deleting something that is not there is 404 rather than a silent success.
func TestDeletingWhatIsNotThereIsNotFound(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	w := httptest.NewRecorder()
	f.h.Delete(w, request("DELETE", "/files/absent.txt", "", nil), f.resolve(t, "absent.txt"))

	if w.Code != http.StatusNotFound {
		t.Errorf("answered %d, want 404", w.Code)
	}
}

// A locked resource refuses every write with 423, and the refusal carries the
// element a client reads to learn that a token would help.
func TestALockedResourceRefusesEveryWrite(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.write(t, "held.txt", "original")
	f.locks.refuse = errors.New("held by another request")

	w := httptest.NewRecorder()
	f.h.Put(w, request("PUT", "/files/held.txt", "replaced", nil), f.resolve(t, "held.txt"))

	if w.Code != http.StatusLocked {
		t.Fatalf("a locked write answered %d, want 423", w.Code)
	}
	if !strings.Contains(w.Body.String(), "lock-token-submitted") {
		t.Errorf("the refusal names no precondition: %s", w.Body.String())
	}
	if got := f.read(t, "held.txt"); got != "original" {
		t.Errorf("the write landed on a locked resource: %q", got)
	}
}

// The lock guard runs on every mutating method, not only PUT.
func TestTheLockGuardCoversDeleteAndMkcol(t *testing.T) {
	t.Parallel()

	t.Run("DELETE", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		f.write(t, "held.txt", "original")
		f.locks.refuse = errors.New("held")

		w := httptest.NewRecorder()
		f.h.Delete(w, request("DELETE", "/files/held.txt", "", nil), f.resolve(t, "held.txt"))

		if w.Code != http.StatusLocked {
			t.Errorf("answered %d, want 423", w.Code)
		}
		if !f.exists("held.txt") {
			t.Error("a locked resource was deleted")
		}
	})

	t.Run("MKCOL", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		f.locks.refuse = errors.New("held")

		w := httptest.NewRecorder()
		f.h.Mkcol(w, request("MKCOL", "/files/New", "", nil), f.resolve(t, "New"))

		if w.Code != http.StatusLocked {
			t.Errorf("answered %d, want 423", w.Code)
		}
		if f.exists("New") {
			t.Error("a locked path was created under")
		}
	})
}

// The holder of a lock writes through it by naming its token.
//
// The condition only holds when the resource really carries the token, which
// is the point: a client cannot satisfy a lock precondition by naming a token
// nobody granted it. The lookup the evaluation reads is wired here.
func TestAnIfHeaderSubmitsItsTokens(t *testing.T) {
	t.Parallel()
	f := newFixtureHolding(t, "urn:uuid:abc")
	f.write(t, "held.txt", "original")

	w := httptest.NewRecorder()
	f.h.Put(w, request("PUT", "/files/held.txt", "replaced", map[string]string{
		"If": "(<urn:uuid:abc>)",
	}), f.resolve(t, "held.txt"))

	if w.Code != http.StatusNoContent {
		t.Fatalf("answered %d, want 204", w.Code)
	}
	if len(f.locks.sawTokens) != 1 || f.locks.sawTokens[0] != "urn:uuid:abc" {
		t.Errorf("the guard saw %v, want the one submitted token", f.locks.sawTokens)
	}
	if got := f.read(t, "held.txt"); got != "replaced" {
		t.Errorf("the holder's own write did not land: %q", got)
	}
}

// Naming a token the resource does not hold is 412, not a way through.
//
// This is the same shape as the test above with one difference, and it is the
// difference that matters: without the held token the condition is false, so a
// client cannot talk its way past a lock by guessing.
func TestNamingAnUnheldTokenDoesNotSatisfyTheCondition(t *testing.T) {
	t.Parallel()
	f := newFixtureHolding(t, "urn:uuid:the-real-one")
	f.write(t, "held.txt", "original")

	w := httptest.NewRecorder()
	f.h.Put(w, request("PUT", "/files/held.txt", "replaced", map[string]string{
		"If": "(<urn:uuid:a-guess>)",
	}), f.resolve(t, "held.txt"))

	if w.Code != http.StatusPreconditionFailed {
		t.Fatalf("answered %d, want 412", w.Code)
	}
	if got := f.read(t, "held.txt"); got != "original" {
		t.Errorf("a guessed token let the write land: %q", got)
	}
}

// A token named behind a Not is not submitted. It was named to assert the
// lock's absence, and counting it would let a request write through a lock by
// mentioning it.
func TestANegatedTokenIsNotSubmitted(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.write(t, "held.txt", "original")

	w := httptest.NewRecorder()
	f.h.Put(w, request("PUT", "/files/held.txt", "replaced", map[string]string{
		"If": "(Not <urn:uuid:abc>)",
	}), f.resolve(t, "held.txt"))

	// The condition holds, because no lock is held here, so the write proceeds.
	if w.Code != http.StatusNoContent {
		t.Fatalf("answered %d, want 204", w.Code)
	}
	for _, got := range f.locks.sawTokens {
		if got == "urn:uuid:abc" {
			t.Error("a negated token was submitted to the lock guard")
		}
	}
}

// An If header that parsed and did not hold is 412, which is a different
// answer from 423: the state is wrong rather than the caller lacking a key.
func TestAnUnsatisfiedIfHeaderIsAPreconditionFailure(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.write(t, "notes.txt", "hello")

	w := httptest.NewRecorder()
	f.h.Put(w, request("PUT", "/files/notes.txt", "replaced", map[string]string{
		// The resource holds no such token, so the list cannot hold.
		"If": "(<urn:uuid:nobody-holds-this>)",
	}), f.resolve(t, "notes.txt"))

	if w.Code != http.StatusPreconditionFailed {
		t.Fatalf("answered %d, want 412", w.Code)
	}
	if got := f.read(t, "notes.txt"); got != "hello" {
		t.Errorf("the write landed despite a failed precondition: %q", got)
	}
}

// A malformed If header is 400. A header this server silently misreads is a
// precondition the client believes it set and did not.
func TestAMalformedIfHeaderIsRefused(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.write(t, "notes.txt", "hello")

	w := httptest.NewRecorder()
	f.h.Put(w, request("PUT", "/files/notes.txt", "replaced", map[string]string{
		"If": "this is not a condition list",
	}), f.resolve(t, "notes.txt"))

	if w.Code != http.StatusBadRequest {
		t.Errorf("answered %d, want 400", w.Code)
	}
	if got := f.read(t, "notes.txt"); got != "hello" {
		t.Errorf("the write landed on a malformed header: %q", got)
	}
}
