package watch

import (
	"fmt"
	"slices"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
)

func k(dir string) key { return key{share: vfs.ShareID(1), dir: dir} }

// register is what the watcher does around the hot set: evict for room, then
// mark the new key registered.
func register(h *hotSet, target key) []key {
	evicted := h.evictFor(target)
	h.markRegistered(target)
	return evicted
}

// The failure this test exists for is silent. The directory a user is looking
// at is exactly the one an LRU evicts under load, so invalidations stop
// arriving for the folder in front of them while everything else keeps working.
func TestTheStickyHalfIsNeverEvicted(t *testing.T) {
	h := newHotSet(3)

	pinned := k("watched")
	if !h.addSticky(pinned) {
		t.Fatal("the first pin should ask for a registration")
	}
	register(h, pinned)

	for i := range 10 {
		recent := k(fmt.Sprintf("recent%02d", i))
		if h.touch(recent) {
			register(h, recent)
		}
	}

	if !h.isRegistered(pinned) {
		t.Fatal("the pinned directory was evicted")
	}
	if got := h.registeredCount(); got > 3 {
		t.Fatalf("the registered set grew to %d past a cap of 3", got)
	}
}

// The evictable half evicts oldest first, which is the part that sounds like
// the whole feature.
func TestTheRecentHalfEvictsOldestFirst(t *testing.T) {
	h := newHotSet(2)
	a, b, c := k("a"), k("b"), k("c")

	for _, key := range []key{a, b} {
		h.touch(key)
		register(h, key)
	}
	// Reading a again makes b the oldest.
	h.touch(a)

	h.touch(c)
	evicted := register(h, c)
	if !slices.Equal(evicted, []key{b}) {
		t.Fatalf("evicted %v, want b", evicted)
	}
	if !h.isRegistered(a) || !h.isRegistered(c) {
		t.Fatal("the wrong keys survived")
	}
}

// A pin is refcounted, so two subscribers on one directory do not tear each
// other's watch down.
func TestPinsAreRefcounted(t *testing.T) {
	h := newHotSet(4)
	dir := k("shared")

	if !h.addSticky(dir) {
		t.Fatal("the first pin should ask for a registration")
	}
	register(h, dir)
	if h.addSticky(dir) {
		t.Fatal("the second pin asked for a second registration")
	}

	h.removeSticky(dir)
	if !h.isRegistered(dir) {
		t.Fatal("releasing one of two subscribers dropped the watch")
	}
	// At zero it falls back into the evictable half rather than being torn down
	// while a second reader may be a moment behind.
	h.removeSticky(dir)
	if !h.isRegistered(dir) {
		t.Fatal("the last release tore the watch down instead of making it evictable")
	}

	filler := k("filler")
	h.touch(filler)
	if evicted := register(h, filler); len(evicted) != 0 {
		t.Fatalf("evicted %v with room to spare", evicted)
	}
}

// When everything left is pinned there is nothing to evict, and exceeding the
// cap slightly beats dropping the watch a user is actively reading.
func TestAFullyPinnedSetExceedsTheCapRatherThanDroppingAPin(t *testing.T) {
	h := newHotSet(2)
	for i := range 4 {
		pinned := k(fmt.Sprintf("pinned%d", i))
		h.addSticky(pinned)
		register(h, pinned)
	}
	if h.registeredCount() != 4 {
		t.Fatalf("registered %d pinned directories, want all 4", h.registeredCount())
	}
}

func TestTouchingAPinnedKeyChangesNothing(t *testing.T) {
	h := newHotSet(4)
	dir := k("pinned")
	h.addSticky(dir)
	register(h, dir)
	if h.touch(dir) {
		t.Fatal("touching a pinned directory asked for a second registration")
	}
}

func TestRegisteredKeysIsTheRescanSnapshot(t *testing.T) {
	h := newHotSet(4)
	for _, dir := range []string{"a", "b"} {
		h.touch(k(dir))
		register(h, k(dir))
	}
	got := h.registeredKeys()
	slices.SortFunc(got, func(x, y key) int {
		if x.dir < y.dir {
			return -1
		}
		return 1
	})
	if len(got) != 2 || got[0].dir != "a" || got[1].dir != "b" {
		t.Fatalf("registeredKeys = %v", got)
	}
}

func TestUnregisteringLeavesTheSetConsistent(t *testing.T) {
	h := newHotSet(2)
	dir := k("a")
	h.touch(dir)
	register(h, dir)
	h.markUnregistered(dir)
	if h.isRegistered(dir) {
		t.Fatal("the key is still registered")
	}
	if h.registeredCount() != 0 {
		t.Fatalf("count = %d", h.registeredCount())
	}
}
