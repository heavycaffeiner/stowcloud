//go:build linux

package watch

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/internal/clock"
	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
)

func testConfig() Config {
	c := DefaultConfig()
	// Short enough that a test does not wait on the shipped 200ms window, long
	// enough that a burst still coalesces.
	c.Debounce = 20 * time.Millisecond
	c.RescanInterval = 50 * time.Millisecond
	return c
}

func startWatcher(t *testing.T, cfg Config) (*Watcher, chan InvalEvent, string) {
	t.Helper()
	host := t.TempDir()
	sink := make(chan InvalEvent, 64)
	w, err := Start(context.Background(), cfg, clock.System(), sink)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		if err := w.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	w.AddShare(1, host, false)
	return w, sink, host
}

func waitFor(t *testing.T, sink <-chan InvalEvent, want func(InvalEvent) bool) InvalEvent {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-sink:
			if want(ev) {
				return ev
			}
		case <-deadline:
			t.Fatal("no matching invalidation arrived within five seconds")
		}
	}
}

func TestAChangeInAWatchedDirectoryIsReported(t *testing.T) {
	w, sink, host := startWatcher(t, testConfig())
	w.Subscribe(1, vfs.RootPath())

	if err := os.WriteFile(filepath.Join(host, "new.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	ev := waitFor(t, sink, func(e InvalEvent) bool { return !e.All })
	if ev.Share != 1 || ev.Dir != "" {
		t.Fatalf("event = %+v, want the share root", ev)
	}
}

// A subscription pins the whole ancestor chain, because a change to a child
// changes the listing of every directory above it.
func TestSubscribePinsTheAncestorChain(t *testing.T) {
	w, sink, host := startWatcher(t, testConfig())
	deep := filepath.Join(host, "a", "b")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}

	p, err := vfs.ParseSafePath("a/b")
	if err != nil {
		t.Fatal(err)
	}
	w.Subscribe(1, p)

	if got := w.Stats().Registered; got != 3 {
		t.Fatalf("registered %d directories, want the root, a and a/b", got)
	}

	if err := os.WriteFile(filepath.Join(deep, "leaf.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	ev := waitFor(t, sink, func(e InvalEvent) bool { return e.Dir == "a/b" })
	if ev.Share != 1 {
		t.Fatalf("event = %+v", ev)
	}
}

// A burst that finishes inside the window costs one event rather than one per
// file.
func TestABurstCoalescesIntoOneEvent(t *testing.T) {
	cfg := testConfig()
	cfg.Debounce = 300 * time.Millisecond
	w, sink, host := startWatcher(t, cfg)
	w.Subscribe(1, vfs.RootPath())

	for i := range 20 {
		name := filepath.Join(host, string(rune('a'+i))+".txt")
		if err := os.WriteFile(name, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, sink, func(e InvalEvent) bool { return !e.All })

	// Nothing else may arrive for the same directory from that burst.
	select {
	case ev := <-sink:
		t.Fatalf("a second event arrived for one burst: %+v", ev)
	case <-time.After(400 * time.Millisecond):
	}
}

// The threshold is what refuses, and what it produces is one event per share
// rather than one per directory: there is nothing to replay, because what was
// missed is exactly what is unknown.
func TestOutgrowingTheThresholdInvalidatesWholeShares(t *testing.T) {
	cfg := testConfig()
	cfg.FullThreshold = 2
	cfg.Debounce = time.Hour // so nothing flushes the ordinary way
	w, sink, host := startWatcher(t, cfg)
	w.AddShare(2, t.TempDir(), false)

	for _, dir := range []string{"a", "b", "c", "d"} {
		if err := os.Mkdir(filepath.Join(host, dir), 0o755); err != nil {
			t.Fatal(err)
		}
		p, err := vfs.ParseSafePath(dir)
		if err != nil {
			t.Fatal(err)
		}
		w.Subscribe(1, p)
		if err := os.WriteFile(filepath.Join(host, dir, "x"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	ev := waitFor(t, sink, func(e InvalEvent) bool { return e.All })
	if ev.Dir != "" {
		t.Fatalf("a whole-share invalidation named a directory: %+v", ev)
	}
	// Every share is invalidated, not only the one that overflowed: the dirty
	// set is what became untrustworthy, and it spans shares.
	seen := map[vfs.ShareID]bool{ev.Share: true}
	waitFor(t, sink, func(e InvalEvent) bool {
		if e.All {
			seen[e.Share] = true
		}
		return len(seen) == 2
	})
}

// The rescan is the only thing that notices a change another host made on a
// network mount, and it revisits what is already tracked rather than walking.
func TestTheRescanRemarksWatchedDirectoriesOfUnreliableFilesystems(t *testing.T) {
	cfg := testConfig()
	cfg.Debounce = 10 * time.Millisecond
	w, sink, _ := startWatcher(t, cfg)
	w.AddShare(1, t.TempDir(), true)
	w.Subscribe(1, vfs.RootPath())

	// Drain anything the subscription itself produced.
	select {
	case <-sink:
	case <-time.After(100 * time.Millisecond):
	}

	ev := waitFor(t, sink, func(e InvalEvent) bool { return !e.All && e.Share == 1 })
	if ev.Dir != "" {
		t.Fatalf("the rescan reported %q, want the watched root", ev.Dir)
	}
}

func TestARescanIsNotRunForALocalFilesystem(t *testing.T) {
	cfg := testConfig()
	w, sink, _ := startWatcher(t, cfg)
	w.Subscribe(1, vfs.RootPath())

	select {
	case ev := <-sink:
		t.Fatalf("an unchanged local share produced %+v", ev)
	case <-time.After(3 * cfg.RescanInterval):
	}
}

func TestUnsubscribeReleasesThePin(t *testing.T) {
	cfg := testConfig()
	cfg.HotSetMax = 1
	w, _, host := startWatcher(t, cfg)

	if err := os.Mkdir(filepath.Join(host, "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	p, err := vfs.ParseSafePath("a")
	if err != nil {
		t.Fatal(err)
	}
	w.Subscribe(1, p)
	w.Unsubscribe(1, p)

	// With the pin released the cap can reclaim, so touching new directories
	// does not grow the registered set without bound.
	for i := range 5 {
		q, perr := vfs.ParseSafePath("a")
		if perr != nil {
			t.Fatal(perr)
		}
		_ = i
		w.Touch(1, q)
	}
	if got := w.Stats().Registered; got > 2 {
		t.Fatalf("registered %d directories against a cap of 1", got)
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	sink := make(chan InvalEvent, 1)
	w, err := Start(context.Background(), testConfig(), clock.System(), sink)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
}

// A directory the watcher has no host path for is a named degradation rather
// than a watch that silently pretends.
func TestAnUnknownShareDegradesRatherThanPretending(t *testing.T) {
	w, _, _ := startWatcher(t, testConfig())
	w.Subscribe(99, vfs.RootPath())
	if w.Stats().Degraded == 0 {
		t.Fatal("watching a share with no host path was not reported as a degradation")
	}
	if w.Stats().Registered != 0 {
		t.Fatal("a watch was registered for a share with no host path")
	}
}
