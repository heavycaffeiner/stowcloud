//go:build linux

package dav

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// awaitCopy waits for a recursive COPY to land.
//
// A collection COPY is answered 202 and finished on a detached goroutine,
// which is the right shape for a tree that takes minutes. A test asserting on
// the result has to wait for it rather than for the status code, and that is
// the whole point: the status said "started" for the entire life of the port
// while nothing was copied.
func awaitCopy(t *testing.T, f *fixture, path string) {
	t.Helper()
	// A bounded number of sleeps rather than a wall-clock deadline: reading
	// the clock outside internal/clock is what D8 forbids, and a count is all
	// this needs. Five hundred passes at 10ms is five seconds, which is orders
	// of magnitude more than a copy of three small files takes.
	for range 500 {
		if f.do(t, "GET", path, "", nil).Code == http.StatusOK {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s never appeared: the copy answered 202 and produced nothing", path)
}

// RFC 4918 conformance, in this repository rather than in an external suite.
//
// The baseline was litmus, which is a 2000s-era autotools program with no TLS
// support: it needed a plaintext proxy in front of a server that deliberately
// has no plaintext listener, and it cannot be built on a host without neon and
// autotools. A conformance table nobody can reproduce is a table that stops
// being true without anybody noticing, and this one had already gone stale
// against a build that no longer exists.
//
// These reproduce the assertions rather than the program: each test is named
// after the litmus case it stands in for, and asserts what RFC 4918 requires
// rather than what any implementation happens to do. They run in the ordinary
// `go test` gate, on every push, with no proxy and no external dependency.
//
// The five below are the cluster that had no test at all: collection COPY and
// MOVE onto an existing destination. Two of them answered 500, which is the
// wrong answer whatever the right one is.

// copy_coll: COPY a collection onto an existing collection with Overwrite: T.
//
// RFC 4918 9.8.4: the destination is deleted and replaced. It answered 404,
// because the recursive walk started from a zero stat and took the single-file
// path.
func TestConformanceCopyCollectionOverExistingCollection(t *testing.T) {
	f := newFixture(t)
	f.write(t, "src/a.txt", "one")
	f.write(t, "src/deep/b.txt", "two")
	f.write(t, "dst/old.txt", "gone")

	// 202: a collection copy is a background operation, which is the right
	// shape for a tree that takes minutes.
	res := f.transfer(t, "COPY", "/src", "/dst", true)
	if res.Code != http.StatusAccepted {
		t.Fatalf("COPY over an existing collection = %d, want 202\n%s", res.Code, res.Body)
	}

	// The members have to be there, which is the part a status code cannot
	// say. Every collection COPY on this build produced nothing for the whole
	// of the port while answering 202.
	for _, want := range []string{"/dst/a.txt", "/dst/deep/b.txt"} {
		awaitCopy(t, f, want)
	}
	// And the destination's own former member is gone: this is a replace.
	if got := f.do(t, "GET", "/dst/old.txt", "", nil); got.Code != http.StatusNotFound {
		t.Errorf("the replaced collection's old member = %d, want 404", got.Code)
	}
}

// copy_overwrite: COPY onto an existing destination with Overwrite: F.
//
// RFC 4918 9.8.4: 412. It answered 500.
func TestConformanceCopyRefusesWithoutOverwrite(t *testing.T) {
	f := newFixture(t)
	f.write(t, "src/a.txt", "one")
	f.write(t, "dst/keep.txt", "kept")

	res := f.transfer(t, "COPY", "/src", "/dst", false)
	if res.Code != http.StatusPreconditionFailed {
		t.Fatalf("COPY onto an existing destination without overwrite = %d, want 412\n%s",
			res.Code, res.Body)
	}
	// A refusal leaves the destination alone.
	if got := f.do(t, "GET", "/dst/keep.txt", "", nil); got.Code != http.StatusOK {
		t.Errorf("the refused COPY disturbed the destination: %d", got.Code)
	}
}

// move: MOVE a collection onto an existing collection with Overwrite: T.
//
// RFC 4918 9.9.4. It answered 500.
func TestConformanceMoveCollectionOverExistingCollection(t *testing.T) {
	f := newFixture(t)
	f.write(t, "src/a.txt", "one")
	f.write(t, "dst/old.txt", "gone")

	res := f.transfer(t, "MOVE", "/src", "/dst", true)
	if res.Code != http.StatusNoContent && res.Code != http.StatusCreated {
		t.Fatalf("MOVE over an existing collection = %d, want 204 or 201\n%s", res.Code, res.Body)
	}
	if got := f.do(t, "GET", "/dst/a.txt", "", nil); got.Code != http.StatusOK {
		t.Errorf("the moved member = %d, want 200", got.Code)
	}
	// A move leaves nothing behind.
	if got := f.do(t, "GET", "/src/a.txt", "", nil); got.Code != http.StatusNotFound {
		t.Errorf("the source survived the move: %d", got.Code)
	}
}

// move_coll: MOVE onto an existing destination with Overwrite: F.
//
// RFC 4918 9.9.4: 412. It succeeded, which loses the destination.
func TestConformanceMoveRefusesWithoutOverwrite(t *testing.T) {
	f := newFixture(t)
	f.write(t, "src/a.txt", "one")
	f.write(t, "dst/keep.txt", "kept")

	res := f.transfer(t, "MOVE", "/src", "/dst", false)
	if res.Code != http.StatusPreconditionFailed {
		t.Fatalf("MOVE onto an existing destination without overwrite = %d, want 412\n%s",
			res.Code, res.Body)
	}
	// Both ends survive a refusal: the destination is untouched and the source
	// was not moved out from under the caller.
	if got := f.do(t, "GET", "/dst/keep.txt", "", nil); got.Code != http.StatusOK {
		t.Errorf("the refused MOVE disturbed the destination: %d", got.Code)
	}
	if got := f.do(t, "GET", "/src/a.txt", "", nil); got.Code != http.StatusOK {
		t.Errorf("the refused MOVE consumed the source: %d", got.Code)
	}
}

// copy_shallow: MKCOL on a path that already exists.
//
// RFC 4918 9.3.1: 405. It answered 405 already; this pins it, because the case
// arrives through the copy suite and had no test of its own.
func TestConformanceMkcolOnAnExistingPathIsRefused(t *testing.T) {
	f := newFixture(t)
	f.write(t, "coll/a.txt", "one")

	res := f.do(t, "MKCOL", "/coll", "", nil)
	if res.Code != http.StatusMethodNotAllowed {
		t.Fatalf("MKCOL on an existing collection = %d, want 405\n%s", res.Code, res.Body)
	}
	// And on an existing file, which is the same rule.
	if got := f.do(t, "MKCOL", "/coll/a.txt", "", nil); got.Code != http.StatusMethodNotAllowed {
		t.Errorf("MKCOL on an existing file = %d, want 405", got.Code)
	}
}

// MKCOL with a missing intermediate collection.
//
// RFC 4918 9.3.1: 409, not 404. A client branches on this to decide whether to
// create the parent, so the distinction is the whole value of the answer.
func TestConformanceMkcolWithAMissingParentIsAConflict(t *testing.T) {
	f := newFixture(t)
	res := f.do(t, "MKCOL", "/nope/deeper", "", nil)
	if res.Code != http.StatusConflict {
		t.Fatalf("MKCOL under a missing parent = %d, want 409\n%s", res.Code, res.Body)
	}
}

// A copy of a collection is deep by default.
//
// RFC 4918 9.8.3: absent a Depth header, COPY on a collection is Depth
// infinity. This is what the recursive-copy defect broke, and the status code
// alone could not see it.
func TestConformanceCopyIsDeepByDefault(t *testing.T) {
	f := newFixture(t)
	f.write(t, "tree/one.txt", "1")
	f.write(t, "tree/mid/two.txt", "2")
	f.write(t, "tree/mid/deep/three.txt", "3")

	res := f.transfer(t, "COPY", "/tree", "/copy", false)
	if res.Code != http.StatusAccepted {
		t.Fatalf("COPY of a tree = %d, want 202\n%s", res.Code, res.Body)
	}
	for _, want := range []string{
		"/copy/one.txt",
		"/copy/mid/two.txt",
		"/copy/mid/deep/three.txt",
	} {
		awaitCopy(t, f, want)
	}
}

// COPY and MOVE onto themselves.
//
// RFC 4918 9.8.4 and 9.9.4: 403. A server that copies a collection into itself
// walks forever.
func TestConformanceCopyOntoItselfIsRefused(t *testing.T) {
	f := newFixture(t)
	f.write(t, "self/a.txt", "one")

	for _, method := range []string{"COPY", "MOVE"} {
		res := f.transfer(t, method, "/self", "/self", true)
		if res.Code != http.StatusForbidden {
			t.Errorf("%s onto itself = %d, want 403\n%s", method, res.Code, res.Body)
		}
	}
}

// COPY into a descendant of the source.
//
// The same non-termination, one level further out: RFC 4918 9.8.4 refuses it
// with 403.
func TestConformanceCopyIntoItsOwnDescendantIsRefused(t *testing.T) {
	f := newFixture(t)
	f.write(t, "outer/inner/a.txt", "one")

	res := f.transfer(t, "COPY", "/outer", "/outer/inner/child", true)
	if res.Code != http.StatusForbidden {
		t.Errorf("COPY into its own descendant = %d, want 403\n%s", res.Code, res.Body)
	}
}

// PROPFIND with an undeclared namespace prefix.
//
// litmus propfind_invalid2. encoding/xml does not report this: an undeclared
// prefix arrives with Space set to the prefix itself, indistinguishable from a
// properly declared namespace.
func TestConformancePropfindRefusesAnUndeclaredPrefix(t *testing.T) {
	f := newFixture(t)
	f.write(t, "a.txt", "one")

	h := http.Header{}
	h.Set("Depth", "0")
	h.Set("Content-Type", "application/xml")
	body := `<?xml version="1.0"?>` +
		`<D:propfind xmlns:D="DAV:"><D:prop><bar:foo/></D:prop></D:propfind>`
	res := f.do(t, "PROPFIND", "/a.txt", body, h)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("PROPFIND with an undeclared prefix = %d, want 400\n%s", res.Code, res.Body)
	}
}

// A prefix declared on an ancestor is bound, and the two reserved prefixes
// need no declaration. The refusal above must not reject a valid document.
func TestConformancePropfindAcceptsAnInheritedPrefix(t *testing.T) {
	f := newFixture(t)
	f.write(t, "a.txt", "one")

	h := http.Header{}
	h.Set("Depth", "0")
	h.Set("Content-Type", "application/xml")
	body := `<?xml version="1.0"?>` +
		`<D:propfind xmlns:D="DAV:" xmlns:x="http://example.com/ns">` +
		`<D:prop><x:custom/><D:displayname/></D:prop></D:propfind>`
	res := f.do(t, "PROPFIND", "/a.txt", body, h)
	if res.Code != http.StatusMultiStatus {
		t.Fatalf("PROPFIND with a declared prefix = %d, want 207\n%s", res.Code, res.Body)
	}
	if !strings.Contains(res.Body.String(), "custom") {
		t.Error("the requested property is missing from the multistatus")
	}
}
