//go:build linux

package lifecycle_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/dav"
)

// target builds the destination a mount would hand a transfer method.
func (f *fixture) target(t *testing.T, path string, overwrite bool) dav.Target {
	t.Helper()
	return dav.Target{Resolved: f.resolve(t, path), Overwrite: overwrite}
}

// A copy leaves both copies in place.
func TestCopyDuplicatesAFile(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.write(t, "source.txt", "contents")

	w := httptest.NewRecorder()
	f.h.Copy(w, request("COPY", "/files/source.txt", "", nil),
		f.resolve(t, "source.txt"), f.target(t, "dest.txt", true))

	if w.Code != http.StatusCreated {
		t.Fatalf("answered %d, want 201", w.Code)
	}
	if got := f.read(t, "dest.txt"); got != "contents" {
		t.Errorf("the copy holds %q", got)
	}
	if got := f.read(t, "source.txt"); got != "contents" {
		t.Errorf("the source was altered: %q", got)
	}
}

// A move relocates, leaving nothing behind.
func TestMoveRelocatesAFile(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.write(t, "source.txt", "contents")

	w := httptest.NewRecorder()
	f.h.Move(w, request("MOVE", "/files/source.txt", "", nil),
		f.resolve(t, "source.txt"), f.target(t, "dest.txt", true))

	if w.Code != http.StatusCreated {
		t.Fatalf("answered %d, want 201", w.Code)
	}
	if got := f.read(t, "dest.txt"); got != "contents" {
		t.Errorf("the destination holds %q", got)
	}
	if f.exists("source.txt") {
		t.Error("the source survived a move")
	}
}

// A move that replaced something answers 204, and one that did not answers
// 201. A client reads the difference to learn whether it destroyed anything.
func TestMoveSeparatesReplacementFromCreation(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.write(t, "a.txt", "first")
	f.write(t, "b.txt", "second")
	f.write(t, "c.txt", "third")

	replaced := httptest.NewRecorder()
	f.h.Move(replaced, request("MOVE", "/files/a.txt", "", nil),
		f.resolve(t, "a.txt"), f.target(t, "b.txt", true))
	if replaced.Code != http.StatusNoContent {
		t.Errorf("replacing answered %d, want 204", replaced.Code)
	}
	if got := f.read(t, "b.txt"); got != "first" {
		t.Errorf("the destination holds %q after replacement", got)
	}

	created := httptest.NewRecorder()
	f.h.Move(created, request("MOVE", "/files/c.txt", "", nil),
		f.resolve(t, "c.txt"), f.target(t, "fresh.txt", true))
	if created.Code != http.StatusCreated {
		t.Errorf("creating answered %d, want 201", created.Code)
	}
}

// Overwrite: F refuses rather than replacing, and the destination survives.
func TestOverwriteFalseRefusesATakenDestination(t *testing.T) {
	t.Parallel()

	t.Run("COPY", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		f.write(t, "a.txt", "source")
		f.write(t, "b.txt", "destination")

		w := httptest.NewRecorder()
		f.h.Copy(w, request("COPY", "/files/a.txt", "", nil),
			f.resolve(t, "a.txt"), f.target(t, "b.txt", false))

		if w.Code != http.StatusPreconditionFailed {
			t.Errorf("answered %d, want 412", w.Code)
		}
		if got := f.read(t, "b.txt"); got != "destination" {
			t.Errorf("the destination was overwritten anyway: %q", got)
		}
	})

	t.Run("MOVE", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		f.write(t, "a.txt", "source")
		f.write(t, "b.txt", "destination")

		w := httptest.NewRecorder()
		f.h.Move(w, request("MOVE", "/files/a.txt", "", nil),
			f.resolve(t, "a.txt"), f.target(t, "b.txt", false))

		if w.Code != http.StatusPreconditionFailed {
			t.Errorf("answered %d, want 412", w.Code)
		}
		if got := f.read(t, "b.txt"); got != "destination" {
			t.Errorf("the destination was overwritten anyway: %q", got)
		}
		if !f.exists("a.txt") {
			t.Error("a refused move removed the source")
		}
	})
}

// A destination inside its own source is refused.
//
// Each pass would copy what the previous one wrote, so the walk does not
// terminate. This is the case RFC 4918 calls out and it is worth its own test:
// admitted, it fills the disk.
func TestADestinationInsideTheSourceIsRefused(t *testing.T) {
	t.Parallel()

	t.Run("COPY", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		f.mkdir(t, "tree")
		f.write(t, "tree/file.txt", "x")

		w := httptest.NewRecorder()
		f.h.Copy(w, request("COPY", "/files/tree", "", nil),
			f.resolve(t, "tree"), f.target(t, "tree/inside", true))

		if w.Code == http.StatusCreated || w.Code == http.StatusAccepted {
			t.Fatalf("a self-descendant copy was accepted with %d", w.Code)
		}
		if w.Code != http.StatusForbidden {
			t.Errorf("answered %d, want 403", w.Code)
		}
	})

	t.Run("MOVE", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		f.mkdir(t, "tree")

		w := httptest.NewRecorder()
		f.h.Move(w, request("MOVE", "/files/tree", "", nil),
			f.resolve(t, "tree"), f.target(t, "tree/inside", true))

		if w.Code != http.StatusForbidden {
			t.Errorf("answered %d, want 403", w.Code)
		}
		if !f.exists("tree") {
			t.Error("a refused move removed the source")
		}
	})
}

// A recursive copy is a background job answered 202, since a tree of any size
// cannot be copied inside one request.
func TestARecursiveCopyBecomesAJob(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.mkdir(t, "tree/deep")
	f.write(t, "tree/deep/file.txt", "x")

	w := httptest.NewRecorder()
	f.h.Copy(w, request("COPY", "/files/tree", "", nil),
		f.resolve(t, "tree"), f.target(t, "copy", true))

	if w.Code != http.StatusAccepted {
		t.Errorf("answered %d, want 202", w.Code)
	}
}

// An overwriting copy onto a collection replaces it rather than merging.
//
// Merging leaves a member the destination had and the source does not, which
// then survives a copy that was supposed to have replaced the whole thing.
//
// The replacement belongs to the core, inside the conflict decision that picks
// the destination, so this pins the behaviour a client sees rather than which
// layer produces it. The handler carried a second delete of the same directory
// until this test showed removing it changed nothing.
func TestCopyOntoACollectionReplacesRatherThanMerges(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.mkdir(t, "src")
	f.write(t, "src/kept.txt", "from the source")
	f.mkdir(t, "dst")
	f.write(t, "dst/stale.txt", "only in the destination")

	if !f.exists("dst/stale.txt") {
		t.Fatal("the fixture did not create the stale member")
	}

	w := httptest.NewRecorder()
	f.h.Copy(w, request("COPY", "/files/src", "", nil),
		f.resolve(t, "src"), f.target(t, "dst", true))

	if w.Code != http.StatusAccepted {
		t.Fatalf("answered %d, want 202", w.Code)
	}
	if f.exists("dst/stale.txt") {
		t.Error("a member the source does not have survived the replacement")
	}
	if f.exists("dst") {
		// The whole destination goes, not just its differing members. A
		// handler that emptied it member by member would leave the directory
		// and pass the check above.
		t.Error("the destination collection itself was not removed before the copy")
	}
}

// A move writes at both ends, so a lock over either one refuses it.
//
// Each end is locked separately. A stub refusing every path cannot tell a
// handler that guards both from one that guards only the source, because both
// answer 423 either way.
func TestMoveIsGuardedAtBothEnds(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name   string
		locked string
	}{
		{"the source is locked", "a.txt"},
		{"the destination is locked", "b.txt"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			f.write(t, "a.txt", "source")
			f.locks.refuseAt = map[string]error{c.locked: errors.New("held")}

			w := httptest.NewRecorder()
			f.h.Move(w, request("MOVE", "/files/a.txt", "", nil),
				f.resolve(t, "a.txt"), f.target(t, "b.txt", true))

			if w.Code != http.StatusLocked {
				t.Errorf("answered %d, want 423", w.Code)
			}
			if !f.exists("a.txt") {
				t.Error("a locked move removed the source")
			}
			if f.exists("b.txt") {
				t.Error("a locked move created the destination")
			}
		})
	}
}

// A copy writes only at the destination, so that is what is guarded.
func TestCopyIsGuardedAtTheDestination(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.write(t, "a.txt", "source")
	f.locks.refuse = errors.New("held")

	w := httptest.NewRecorder()
	f.h.Copy(w, request("COPY", "/files/a.txt", "", nil),
		f.resolve(t, "a.txt"), f.target(t, "b.txt", true))

	if w.Code != http.StatusLocked {
		t.Errorf("answered %d, want 423", w.Code)
	}
	if f.exists("b.txt") {
		t.Error("a locked copy created the destination")
	}
}

// Copying something that is not there is 404.
func TestCopyingWhatIsNotThereIsNotFound(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	w := httptest.NewRecorder()
	f.h.Copy(w, request("COPY", "/files/absent.txt", "", nil),
		f.resolve(t, "absent.txt"), f.target(t, "dest.txt", true))

	if w.Code != http.StatusNotFound {
		t.Errorf("answered %d, want 404", w.Code)
	}
}
