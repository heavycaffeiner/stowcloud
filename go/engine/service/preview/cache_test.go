//go:build linux

package preview

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/ident"
)

func newCache(t *testing.T) *Cache {
	t.Helper()
	c, err := NewCache(filepath.Join(t.TempDir(), "thumbs"))
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}
	return c
}

func keyFor(share uint32, ino uint64, preset Preset) Key {
	return Key{
		Ident:   ident.Ident{Share: vfs.ShareID(share), Dev: 7, Ino: ino},
		MTimeNs: 1000,
		Size:    2048,
		Preset:  preset,
	}
}

func put(t *testing.T, c *Cache, k Key, body string) {
	t.Helper()
	err := c.Put(k, func(f *os.File) error {
		_, werr := f.Write([]byte(body))
		return werr
	})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
}

func read(t *testing.T, c *Cache, k Key) (string, bool) {
	t.Helper()
	f, ok := c.Open(k)
	if !ok {
		return "", false
	}
	defer func() {
		if cerr := f.Close(); cerr != nil {
			t.Errorf("close: %v", cerr)
		}
	}()
	b, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("reading a cached entry: %v", err)
	}
	return string(b), true
}

func TestCacheHitAndMiss(t *testing.T) {
	c := newCache(t)
	k := keyFor(1, 100, PresetSmall)

	if _, ok := c.Open(k); ok {
		t.Error("an empty cache reported a hit")
	}
	put(t, c, k, "thumbnail bytes")
	body, ok := read(t, c, k)
	if !ok {
		t.Fatal("a stored entry did not read back")
	}
	if body != "thumbnail bytes" {
		t.Errorf("read %q", body)
	}
	if c.Dir() == "" {
		t.Error("the cache reports no directory")
	}
}

// A source that changed produces a different key, so a stale thumbnail is
// unreachable rather than evicted: there is no invalidation step to get wrong.
func TestTheKeyChangesWithContentIdentity(t *testing.T) {
	c := newCache(t)
	base := keyFor(1, 100, PresetSmall)
	put(t, c, base, "original")

	btime := int64(42)
	variants := map[string]Key{
		"a different inode":  keyFor(1, 101, PresetSmall),
		"a different share":  keyFor(2, 100, PresetSmall),
		"a different preset": keyFor(1, 100, PresetLarge),
	}
	changed := base
	changed.MTimeNs = 2000
	variants["a different mtime"] = changed
	changed = base
	changed.Size = 4096
	variants["a different size"] = changed
	changed = base
	changed.Ident.Btime = &btime
	variants["a birth time appearing"] = changed
	changed = base
	changed.Width, changed.Height = 64, 64
	variants["an exact size"] = changed
	changed = base
	changed.Width, changed.Height = 64, 65
	variants["a different exact size"] = changed

	for name, k := range variants {
		t.Run(name, func(t *testing.T) {
			if k.String() == base.String() {
				t.Fatal("the key did not change")
			}
			if _, ok := c.Open(k); ok {
				t.Error("a changed key hit the old entry")
			}
		})
	}

	// The same key is the same name, or nothing would ever hit.
	if keyFor(1, 100, PresetSmall).String() != base.String() {
		t.Error("two equal keys produced different names")
	}
}

// The name is a digest, so there is nothing in it that came from a caller: a
// path built from a filename is a path an attacker chooses.
func TestTheKeyNameIsADigest(t *testing.T) {
	name := keyFor(1, 100, PresetSmall).String()
	if name == "" {
		t.Fatal("the key produced no name")
	}
	for _, c := range name {
		isSafe := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '-' || c == '_'
		if !isSafe {
			t.Errorf("the key name carries %q, which is not digest output", c)
		}
	}
}

// Put is a durable atomic replace, so a crash mid-write leaves the old entry
// rather than a half thumbnail a later Open would serve.
func TestAFailedWriteLeavesTheOldEntryServed(t *testing.T) {
	c := newCache(t)
	k := keyFor(1, 100, PresetSmall)
	put(t, c, k, "the good thumbnail")

	// The write fails partway, which is what a crash between stage and rename
	// looks like to the publish path.
	boom := errors.New("the disk went away")
	err := c.Put(k, func(f *os.File) error {
		if _, werr := f.Write([]byte("half a thum")); werr != nil {
			return werr
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("the failure was not reported: %v", err)
	}

	body, ok := read(t, c, k)
	if !ok {
		t.Fatal("the old entry is gone after a failed write")
	}
	if body != "the good thumbnail" {
		t.Errorf("a partial write became visible: %q", body)
	}
}

// A replacement is visible whole, never half.
func TestPutReplacesAnExistingEntry(t *testing.T) {
	c := newCache(t)
	k := keyFor(1, 100, PresetSmall)
	put(t, c, k, "first")
	put(t, c, k, "second")

	if body, _ := read(t, c, k); body != "second" {
		t.Errorf("read %q, want the replacement", body)
	}
}

// The cache is disposable: delete the directory and previews regenerate.
func TestTheCacheDirectoryIsDisposable(t *testing.T) {
	c := newCache(t)
	k := keyFor(1, 100, PresetSmall)
	put(t, c, k, "bytes")

	if err := os.RemoveAll(c.Dir()); err != nil {
		t.Fatalf("removing the cache: %v", err)
	}
	if _, ok := c.Open(k); ok {
		t.Error("an entry survived the directory being deleted")
	}
	// And it still works afterwards.
	put(t, c, k, "regenerated")
	if body, _ := read(t, c, k); body != "regenerated" {
		t.Errorf("the cache did not recover: %q", body)
	}
}

// A failed decode is remembered with its reason, so a grid full of corrupt
// files does not re-run the worker on every scroll.
func TestNegativesRememberAReasonUntilItExpires(t *testing.T) {
	n := NewNegatives()
	k := keyFor(1, 100, PresetSmall)
	now := time.Unix(1_700_000_000, 0)

	if _, ok := n.Get(k, now); ok {
		t.Error("an empty negative cache reported a hit")
	}

	n.Put(k, NegativeDecodeFailed, now)
	reason, ok := n.Get(k, now)
	if !ok || reason != NegativeDecodeFailed {
		t.Errorf("got %v, %v; want the remembered reason", reason, ok)
	}
	if n.Len() != 1 {
		t.Errorf("the cache holds %d entries, want 1", n.Len())
	}

	// The TTL expires it.
	later := now.Add(NegativeDecodeFailed.Lifetime() + time.Second)
	if _, ok := n.Get(k, later); ok {
		t.Error("an expired entry still answered")
	}
}

// The lifetimes differ because the facts differ: a file too large now will be
// too large next time, and a worker death might have been the machine.
func TestNegativeLifetimesReflectTheFact(t *testing.T) {
	if NegativeWorkerDied.Lifetime() >= NegativeDecodeFailed.Lifetime() {
		t.Error("a worker death is held at least as long as a decode failure")
	}
	if NegativeDecodeFailed.Lifetime() >= NegativeUnsupported.Lifetime() {
		t.Error("a decode failure is held at least as long as an unsupported format")
	}
	if NegativeNone.Lifetime() != 0 {
		t.Error("the absence of a failure has a lifetime")
	}
	for n, want := range map[Negative]string{
		NegativeNone: "none", NegativeTooLarge: "too large",
		NegativeUnsupported: "unsupported", NegativeNotImplemented: "not implemented",
		NegativeDecodeFailed: "decode failed", NegativeWorkerDied: "worker died",
		Negative(99): "none",
	} {
		if got := n.String(); got != want {
			t.Errorf("%d.String() = %q, want %q", n, got, want)
		}
	}
}

// NegativeNone is not a failure, so it is not remembered: a success would
// otherwise poison the key.
func TestPuttingNoReasonRemembersNothing(t *testing.T) {
	n := NewNegatives()
	n.Put(keyFor(1, 100, PresetSmall), NegativeNone, time.Now())
	if n.Len() != 0 {
		t.Errorf("the cache holds %d entries after remembering nothing", n.Len())
	}
}

// The sweep drops expired entries and counts them, so the map does not grow
// without bound.
func TestSweepDropsExpiredEntriesAndCounts(t *testing.T) {
	n := NewNegatives()
	now := time.Unix(1_700_000_000, 0)

	// One short-lived and one long-lived.
	n.Put(keyFor(1, 1, PresetSmall), NegativeWorkerDied, now)
	n.Put(keyFor(1, 2, PresetSmall), NegativeUnsupported, now)
	if n.Len() != 2 {
		t.Fatalf("the cache holds %d entries, want 2", n.Len())
	}

	// Past the worker-death lifetime and well inside the unsupported one.
	later := now.Add(NegativeWorkerDied.Lifetime() + time.Minute)
	if dropped := n.Sweep(later); dropped != 1 {
		t.Errorf("the sweep dropped %d, want 1", dropped)
	}
	if n.Len() != 1 {
		t.Errorf("the cache holds %d entries after the sweep, want 1", n.Len())
	}
	// A sweep with nothing to do reports zero.
	if dropped := n.Sweep(later); dropped != 0 {
		t.Errorf("a second sweep dropped %d", dropped)
	}
}

// In memory rather than on disk: a restart retries, which is the desired
// behaviour after an upgrade fixes a decoder.
func TestNegativesAreForgottenOnRestart(t *testing.T) {
	k := keyFor(1, 100, PresetSmall)
	now := time.Now()

	first := NewNegatives()
	first.Put(k, NegativeUnsupported, now)
	if _, ok := first.Get(k, now); !ok {
		t.Fatal("the entry was not remembered")
	}

	// A new process starts with nothing.
	if _, ok := NewNegatives().Get(k, now); ok {
		t.Error("a fresh negative cache answered from a previous run")
	}
}
