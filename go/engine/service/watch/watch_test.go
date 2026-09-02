//go:build linux

package watch

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/clock"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
	"golang.org/x/sys/unix"
)

const testShare = vfs.ShareID(1)

// start brings up a watcher over one share rooted at a temporary directory.
func start(t *testing.T, cfg Config) (*Watcher, chan InvalEvent, string) {
	t.Helper()
	root := t.TempDir()
	sink := make(chan InvalEvent, 64)

	w, err := Start(t.Context(), cfg, clock.System(), sink)
	if err != nil {
		t.Skipf("this host refused an inotify descriptor: %v", err)
	}
	t.Cleanup(func() {
		if cerr := w.Close(); cerr != nil {
			t.Errorf("closing the watcher: %v", cerr)
		}
	})
	w.AddShare(testShare, root, false)
	return w, sink, root
}

// waitFor reads events until one matches or the deadline passes.
func waitFor(t *testing.T, sink <-chan InvalEvent, want func(InvalEvent) bool) InvalEvent {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case ev := <-sink:
			if want(ev) {
				return ev
			}
		case <-deadline:
			t.Fatal("no matching event arrived")
		}
	}
}

func safePath(t *testing.T, s string) vfs.SafePath {
	t.Helper()
	p := vfs.RootPath()
	if s == "" {
		return p
	}
	for _, comp := range splitSlash(s) {
		next, err := p.JoinExisting(comp)
		if err != nil {
			t.Fatalf("building the path %q: %v", s, err)
		}
		p = next
	}
	return p
}

func splitSlash(s string) []string {
	var out []string
	cur := ""
	for i := range len(s) {
		if s[i] == '/' {
			if cur != "" {
				out = append(out, cur)
			}
			cur = ""
			continue
		}
		cur += string(s[i])
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

// A change in a watched directory reports.
func TestAChangeInAWatchedDirectoryReports(t *testing.T) {
	w, sink, root := start(t, Config{Debounce: 10 * time.Millisecond})

	sub := filepath.Join(root, "docs")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	w.Touch(testShare, safePath(t, "docs"))

	if err := os.WriteFile(filepath.Join(sub, "note.txt"), []byte("hi"), 0o600); err != nil {
		t.Fatalf("writing: %v", err)
	}

	ev := waitFor(t, sink, func(e InvalEvent) bool { return !e.All })
	if ev.Share != testShare || ev.Dir != "docs" {
		t.Errorf("got share %d dir %q, want share %d dir %q", ev.Share, ev.Dir, testShare, "docs")
	}
}

// A subscription pins the ancestor chain, so a change anywhere along the path
// that renders a deep directory is reported.
func TestASubscriptionPinsTheAncestorChain(t *testing.T) {
	w, sink, root := start(t, Config{Debounce: 10 * time.Millisecond})

	deep := filepath.Join(root, "a", "b", "c")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	w.Subscribe(testShare, safePath(t, "a/b/c"))

	// Every level of the chain carries a watch: the root, a, a/b and a/b/c.
	if got := w.Stats().Registered; got != 4 {
		t.Errorf("the chain registered %d directories, want 4", got)
	}

	// A change partway up the chain still reports, which is the point of
	// pinning it rather than only the leaf.
	if err := os.WriteFile(filepath.Join(root, "a", "b", "sibling.txt"), []byte("x"), 0o600); err != nil {
		t.Fatalf("writing: %v", err)
	}
	ev := waitFor(t, sink, func(e InvalEvent) bool { return !e.All && e.Dir == "a/b" })
	if ev.Share != testShare {
		t.Errorf("the event names share %d", ev.Share)
	}

	// Unsubscribing releases the pins without unregistering the watches, so a
	// reader a moment behind still gets its update.
	w.Unsubscribe(testShare, safePath(t, "a/b/c"))
	if got := w.Stats().Registered; got != 4 {
		t.Errorf("unsubscribing unregistered watches: %d remain", got)
	}
}

// A synthesized IN_Q_OVERFLOW yields a whole-share invalidation: what was
// missed is exactly what is unknown, so there is nothing to replay.
func TestAQueueOverflowYieldsWholeShareInvalidation(t *testing.T) {
	w, sink, _ := start(t, Config{Debounce: time.Millisecond})
	w.AddShare(vfs.ShareID(2), t.TempDir(), false)

	w.consume(overflowRecord())

	ev := waitFor(t, sink, func(e InvalEvent) bool { return e.All })
	if ev.Dir != "" {
		t.Errorf("a whole-share event names the directory %q", ev.Dir)
	}

	// Every share is invalidated, not only one: the dirty set as a whole is
	// what became untrustworthy.
	second := waitFor(t, sink, func(e InvalEvent) bool { return e.All && e.Share != ev.Share })
	if second.Share == ev.Share {
		t.Error("only one share was invalidated")
	}
}

// The parser is fail-closed: a record that does not parse whole stops the batch
// and escalates. A parser that resynchronizes by guessing silently skips
// events, and a skipped event is a stale answer nothing corrects.
func TestATruncatedRecordEscalatesToWholeShare(t *testing.T) {
	cases := map[string][]byte{
		// A trailing fragment shorter than a header.
		"a short header": make([]byte, inotifyEventHeader-1),
		// A header whose declared name runs past the buffer.
		"a name past the buffer": truncatedNameRecord(),
	}

	for name, buf := range cases {
		t.Run(name, func(t *testing.T) {
			w, sink, _ := start(t, Config{Debounce: time.Millisecond})
			w.consume(buf)

			ev := waitFor(t, sink, func(e InvalEvent) bool { return e.All })
			if ev.Share != testShare {
				t.Errorf("the escalation named share %d", ev.Share)
			}
		})
	}
}

// A well-formed batch parses whole and marks exactly its own directory.
func TestAWellFormedBatchMarksItsDirectory(t *testing.T) {
	w, sink, root := start(t, Config{Debounce: time.Millisecond})

	sub := filepath.Join(root, "d")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	w.Touch(testShare, safePath(t, "d"))

	w.mu.Lock()
	wd, ok := w.keyToWd[key{share: testShare, dir: "d"}]
	w.mu.Unlock()
	if !ok {
		t.Skip("the directory did not register, so there is no descriptor to synthesize for")
	}

	// Two records in one buffer, which is what a busy tree produces per read.
	buf := append(namedRecord(t, wd, unix.IN_CREATE, "one.txt"),
		namedRecord(t, wd, unix.IN_MODIFY, "two.txt")...)
	w.consume(buf)

	ev := waitFor(t, sink, func(e InvalEvent) bool { return !e.All })
	if ev.Dir != "d" {
		t.Errorf("got %q, want the directory the records named", ev.Dir)
	}
}

// A registration the kernel refuses is a counted degradation, not a failure:
// the subtree falls back to lazy revalidation and the tree still answers.
func TestARefusedRegistrationCountsAsDegraded(t *testing.T) {
	w, _, _ := start(t, Config{})

	// A share the watcher does not know is the reachable refusal: there is no
	// host path to register, exactly as when the kernel refuses one.
	before := w.Stats().Degraded
	w.Touch(vfs.ShareID(99), safePath(t, "nowhere"))

	if got := w.Stats().Degraded; got != before+1 {
		t.Errorf("degraded is %d, want %d", got, before+1)
	}
	// It is a degradation rather than a failure: nothing panicked, and the
	// watcher still reports its other numbers.
	if w.Stats().Shares != 1 {
		t.Errorf("the watcher lost track of its shares: %+v", w.Stats())
	}
}

// A directory the kernel discards takes its bookkeeping with it, which is what
// a deleted directory produces.
func TestAnIgnoredWatchIsForgotten(t *testing.T) {
	w, _, root := start(t, Config{})

	sub := filepath.Join(root, "gone")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	w.Touch(testShare, safePath(t, "gone"))

	w.mu.Lock()
	wd, ok := w.keyToWd[key{share: testShare, dir: "gone"}]
	w.mu.Unlock()
	if !ok {
		t.Skip("the directory did not register")
	}
	before := w.Stats().Registered

	w.consume(namedRecord(t, wd, unix.IN_IGNORED, ""))

	if got := w.Stats().Registered; got != before-1 {
		t.Errorf("registered is %d, want %d after the watch was discarded", got, before-1)
	}
}

// Lowering hot_set_max live evicts the excess immediately, through the same
// path that also releases the kernel watch, rather than waiting for churn to
// touch a fresh key.
func TestLoweringTheHotSetCapEvictsPromptly(t *testing.T) {
	w, _, root := start(t, Config{HotSetMax: 8, FullThreshold: 50_000})

	dirs := []string{"a", "b", "c", "d"}
	for _, d := range dirs {
		if err := os.Mkdir(filepath.Join(root, d), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
		w.Touch(testShare, safePath(t, d))
	}
	if got := w.Stats().Registered; got != len(dirs) {
		t.Fatalf("registered is %d before lowering the cap, want %d", got, len(dirs))
	}

	w.SetBounds(2, 50_000)

	if got := w.Stats().Registered; got != 2 {
		t.Errorf("registered is %d immediately after lowering the cap, want 2", got)
	}
	// Eviction went through unregister, so the kernel watch for a dropped
	// directory is gone rather than merely absent from the hot set.
	w.mu.Lock()
	remaining := len(w.keyToWd)
	w.mu.Unlock()
	if remaining != 2 {
		t.Errorf("%d kernel watches remain, want 2", remaining)
	}
}

// N rapid writes to one directory produce one event per window, not one per
// write.
func TestDebounceCoalescesABurst(t *testing.T) {
	w, sink, root := start(t, Config{Debounce: 150 * time.Millisecond})

	sub := filepath.Join(root, "busy")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	w.Touch(testShare, safePath(t, "busy"))

	for i := range 20 {
		name := filepath.Join(sub, "f"+string(rune('a'+i))+".txt")
		if err := os.WriteFile(name, []byte("x"), 0o600); err != nil {
			t.Fatalf("writing: %v", err)
		}
	}

	first := waitFor(t, sink, func(e InvalEvent) bool { return !e.All && e.Dir == "busy" })
	if first.Share != testShare {
		t.Errorf("the event names share %d", first.Share)
	}

	// Twenty writes inside one window cost one event, not twenty. A second may
	// follow for writes that landed after the flush, so the bound is loose
	// enough to be about coalescing rather than about timing.
	extra := 0
	settle := time.After(600 * time.Millisecond)
drain:
	for {
		select {
		case e := <-sink:
			if !e.All && e.Dir == "busy" {
				extra++
			}
		case <-settle:
			break drain
		}
	}
	if extra > 3 {
		t.Errorf("a 20-write burst produced %d further events, which is not coalescing", extra)
	}
}

// A share whose filesystem loses events is re-marked by the sweep, which only
// revisits what is already tracked and never walks a tree.
func TestRescanRemarksAnUnreliableShare(t *testing.T) {
	w, sink, root := start(t, Config{Debounce: time.Millisecond})
	w.AddShare(testShare, root, true)

	sub := filepath.Join(root, "nfs")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	w.Touch(testShare, safePath(t, "nfs"))
	// Drain whatever the registration itself produced.
	drainFor(sink, 200*time.Millisecond)

	w.rescan()

	ev := waitFor(t, sink, func(e InvalEvent) bool { return !e.All })
	if ev.Share != testShare {
		t.Errorf("the sweep named share %d", ev.Share)
	}
}

// A share whose filesystem does not lose events is left alone by the sweep.
func TestRescanSkipsAReliableShare(t *testing.T) {
	w, sink, root := start(t, Config{Debounce: time.Millisecond})

	sub := filepath.Join(root, "local")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	w.Touch(testShare, safePath(t, "local"))
	drainFor(sink, 200*time.Millisecond)

	w.rescan()

	w.mu.Lock()
	pending := len(w.pending)
	w.mu.Unlock()
	if pending != 0 {
		t.Errorf("the sweep marked %d directories on a reliable share", pending)
	}
}

// Close is idempotent and stops every loop.
func TestCloseIsIdempotent(t *testing.T) {
	sink := make(chan InvalEvent, 4)
	w, err := Start(t.Context(), Config{}, clock.System(), sink)
	if err != nil {
		t.Skipf("this host refused an inotify descriptor: %v", err)
	}
	if cerr := w.Close(); cerr != nil {
		t.Fatalf("the first Close: %v", cerr)
	}
	if cerr := w.Close(); cerr != nil {
		t.Errorf("a second Close: %v", cerr)
	}
}

// ancestorKeys is the directory and everything above it, root first.
func TestAncestorKeysWalksUpToTheRoot(t *testing.T) {
	got := ancestorKeys(testShare, safePath(t, "a/b/c"))
	want := []string{"", "a", "a/b", "a/b/c"}
	if len(got) != len(want) {
		t.Fatalf("got %d keys: %v", len(got), got)
	}
	for i, w := range want {
		if got[i].dir != w {
			t.Errorf("key %d is %q, want %q", i, got[i].dir, w)
		}
	}

	// The share root alone is one key, not zero.
	if root := ancestorKeys(testShare, vfs.RootPath()); len(root) != 1 || root[0].dir != "" {
		t.Errorf("the root produced %v", root)
	}
}

// drainFor reads whatever arrives for a while, so a test can start from a
// quiet channel.
func drainFor(sink <-chan InvalEvent, d time.Duration) {
	deadline := time.After(d)
	for {
		select {
		case <-sink:
		case <-deadline:
			return
		}
	}
}

// overflowRecord is the kernel's queue-overflow record: wd -1, the overflow
// bit, and no name.
func overflowRecord() []byte {
	buf := make([]byte, inotifyEventHeader)
	binary.NativeEndian.PutUint32(buf[offWd:], ^uint32(0))
	binary.NativeEndian.PutUint32(buf[offMask:], unix.IN_Q_OVERFLOW)
	return buf
}

// namedRecord is one well-formed record. inotify NUL-pads the name to a
// multiple of the alignment, which the parser reads through the declared
// length rather than by scanning.
func namedRecord(t *testing.T, wd int, mask uint32, name string) []byte {
	t.Helper()
	padded := len(name) + 1
	if r := padded % 8; r != 0 {
		padded += 8 - r
	}
	// Both crossings are checked rather than cast: the descriptor is what the
	// kernel just handed back, and the padded length is this fixture's own
	// arithmetic, so a failure here is a broken fixture.
	descriptor, derr := num.Narrow[uint32](wd)
	length, lerr := num.Narrow[uint32](padded)
	if derr != nil || lerr != nil {
		t.Fatalf("the record fixture does not fit its own fields: wd %d, len %d", wd, padded)
	}

	buf := make([]byte, inotifyEventHeader+padded)
	binary.NativeEndian.PutUint32(buf[offWd:], descriptor)
	binary.NativeEndian.PutUint32(buf[offMask:], mask)
	binary.NativeEndian.PutUint32(buf[offNameLen:], length)
	copy(buf[inotifyEventHeader:], name)
	return buf
}

// truncatedNameRecord declares a name longer than the buffer holds, which is
// the fail-closed case.
func truncatedNameRecord() []byte {
	buf := make([]byte, inotifyEventHeader+4)
	binary.NativeEndian.PutUint32(buf[offWd:], 1)
	binary.NativeEndian.PutUint32(buf[offMask:], unix.IN_CREATE)
	binary.NativeEndian.PutUint32(buf[offNameLen:], 4096)
	return buf
}
