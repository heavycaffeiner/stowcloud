package watch

import (
	"slices"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
)

func k(share uint32, dir string) key { return key{share: vfs.ShareID(share), dir: dir} }

func TestParseBackendTakesInotifyAndRefusesTheRest(t *testing.T) {
	got, err := ParseBackend("inotify")
	if err != nil {
		t.Fatalf("ParseBackend(inotify): %v", err)
	}
	if got != BackendInotify || got.String() != "inotify" {
		t.Errorf("got %v (%q)", got, got.String())
	}

	// The regression this rule exists for: a previous configuration accepted
	// "fanotify", warned, and did something else. An operator who configures a
	// transport believes they are running it.
	for _, bad := range []string{
		"fanotify", "", "Inotify", "INOTIFY", "inotify ", "poll", "none", "auto",
	} {
		if _, err := ParseBackend(bad); err == nil {
			t.Errorf("ParseBackend(%q) was accepted", bad)
		}
	}
}

// A partially configured watcher cannot spin at zero intervals.
func TestConfigDefaultsFillEveryZeroField(t *testing.T) {
	got := Config{}.withDefaults()
	want := DefaultConfig()
	if got.HotSetMax != want.HotSetMax || got.FullThreshold != want.FullThreshold {
		t.Errorf("caps came through as %d and %d", got.HotSetMax, got.FullThreshold)
	}
	if got.Debounce != want.Debounce || got.RescanInterval != want.RescanInterval {
		t.Errorf("intervals came through as %v and %v", got.Debounce, got.RescanInterval)
	}

	// A configured value survives.
	mine := Config{HotSetMax: 7, FullThreshold: 9}.withDefaults()
	if mine.HotSetMax != 7 || mine.FullThreshold != 9 {
		t.Errorf("a configured cap was overwritten: %+v", mine)
	}
}

// The sticky half never auto-evicts. This is the whole reason the set has two
// halves rather than being an LRU.
func TestTheStickyHalfNeverEvicts(t *testing.T) {
	h := newHotSet(2)
	pinned := k(1, "watched")

	if !h.addSticky(pinned) {
		t.Fatal("pinning a fresh key did not ask for a registration")
	}
	h.markRegistered(pinned)

	// Fill and overfill the recent half.
	for _, dir := range []string{"a", "b", "c", "d"} {
		arriving := k(1, dir)
		for _, e := range h.evictFor(arriving) {
			h.markUnregistered(e)
		}
		h.touch(arriving)
		h.markRegistered(arriving)
	}

	if !h.isRegistered(pinned) {
		t.Error("the pinned directory was evicted, which is the silent failure mode")
	}
}

// Pins are refcounted: two subscribers to one directory means it survives one
// of them leaving.
func TestPinsAreRefcounted(t *testing.T) {
	h := newHotSet(4)
	pinned := k(1, "shared")

	if !h.addSticky(pinned) {
		t.Fatal("the first pin did not ask for a registration")
	}
	h.markRegistered(pinned)
	if h.addSticky(pinned) {
		t.Error("a second pin asked for a second registration")
	}

	h.removeSticky(pinned)
	if _, still := h.sticky[pinned]; !still {
		t.Error("one release dropped a pin two subscribers held")
	}

	// The last release drops the pin and returns the key to the evictable half,
	// still registered: tearing it down while a second reader may be a moment
	// behind is worse than letting it age out.
	h.removeSticky(pinned)
	if _, still := h.sticky[pinned]; still {
		t.Error("the last release left the pin")
	}
	if !h.isRegistered(pinned) {
		t.Error("releasing a pin unregistered the watch")
	}
	if _, evictable := h.recent[pinned]; !evictable {
		t.Error("the released key did not return to the evictable half")
	}

	// Releasing what was never pinned is a no-op rather than an underflow.
	h.removeSticky(k(1, "never-pinned"))
}

// Touching a pinned key changes nothing: it is already in the half that never
// evicts, and moving it would put it back among the evictable.
func TestTouchingAPinnedKeyChangesNothing(t *testing.T) {
	h := newHotSet(4)
	pinned := k(1, "pinned")
	h.addSticky(pinned)
	h.markRegistered(pinned)

	if h.touch(pinned) {
		t.Error("touching a pinned key asked for a registration it already has")
	}
	if _, evictable := h.recent[pinned]; evictable {
		t.Error("touching a pinned key moved it into the evictable half")
	}
}

// A fully pinned set exceeds the cap rather than dropping a pin.
func TestAFullyPinnedSetExceedsTheCap(t *testing.T) {
	h := newHotSet(2)
	for _, dir := range []string{"a", "b", "c"} {
		pinned := k(1, dir)
		h.addSticky(pinned)
		h.markRegistered(pinned)
	}

	fresh := k(1, "d")
	if evicted := h.evictFor(fresh); len(evicted) != 0 {
		t.Errorf("a fully pinned set evicted %v", evicted)
	}
	if h.registeredCount() != 3 {
		t.Errorf("the pinned set holds %d, want all 3 kept", h.registeredCount())
	}
}

// Eviction is oldest first, and only through the path that also removes the
// kernel watch.
func TestEvictionIsOldestFirst(t *testing.T) {
	h := newHotSet(2)
	for _, dir := range []string{"oldest", "newest"} {
		existing := k(1, dir)
		h.touch(existing)
		h.markRegistered(existing)
	}

	evicted := h.evictFor(k(1, "arriving"))
	if len(evicted) != 1 || evicted[0].dir != "oldest" {
		t.Fatalf("evicted %v, want the oldest", evicted)
	}
	// evictFor does not remove the watch: the caller does that and then marks
	// it, so a failed removal cannot leave the two disagreeing.
	h.markUnregistered(evicted[0])
	if h.isRegistered(evicted[0]) {
		t.Error("the evicted key is still registered")
	}

	// A key already registered needs no eviction to make room for itself.
	if got := h.evictFor(k(1, "newest")); got != nil {
		t.Errorf("making room for an already-registered key evicted %v", got)
	}
}

// Re-touching moves a key to the front, so it is no longer the eviction
// candidate.
func TestTouchingBumpsRecency(t *testing.T) {
	h := newHotSet(2)
	first, second := k(1, "first"), k(1, "second")
	for _, existing := range []key{first, second} {
		h.touch(existing)
		h.markRegistered(existing)
	}

	// first is now the oldest; touching it makes second the oldest instead.
	h.touch(first)
	evicted := h.evictFor(k(1, "third"))
	if len(evicted) != 1 || evicted[0] != second {
		t.Errorf("evicted %v, want the re-touched key spared", evicted)
	}
}

// The registered set is what the rescan snapshots, and it holds both halves.
func TestRegisteredKeysCoversBothHalves(t *testing.T) {
	h := newHotSet(8)
	pinned, recent := k(1, "pinned"), k(2, "recent")

	h.addSticky(pinned)
	h.markRegistered(pinned)
	h.touch(recent)
	h.markRegistered(recent)

	got := h.registeredKeys()
	if len(got) != 2 {
		t.Fatalf("the snapshot holds %d keys: %v", len(got), got)
	}
	if !slices.Contains(got, pinned) || !slices.Contains(got, recent) {
		t.Errorf("the snapshot is missing a half: %v", got)
	}
}

// Unregistering leaves the set consistent: the key is gone from the count and
// from what a later rescan would revisit.
func TestUnregisteringLeavesTheSetConsistent(t *testing.T) {
	h := newHotSet(4)
	watched := k(1, "dir")
	h.touch(watched)
	h.markRegistered(watched)
	if h.registeredCount() != 1 {
		t.Fatalf("the set holds %d", h.registeredCount())
	}

	h.markUnregistered(watched)
	if h.registeredCount() != 0 || h.isRegistered(watched) {
		t.Error("the key survived being unregistered")
	}
	if slices.Contains(h.registeredKeys(), watched) {
		t.Error("an unregistered key is still in the rescan snapshot")
	}
}

// A cap below one is raised rather than producing a set that evicts everything
// it is given.
func TestACapBelowOneIsRaised(t *testing.T) {
	for _, capacity := range []int{0, -1, -100} {
		h := newHotSet(capacity)
		if h.cap < 1 {
			t.Errorf("newHotSet(%d) kept a cap of %d", capacity, h.cap)
		}
	}
}
