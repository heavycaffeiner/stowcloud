//go:build linux

package preview

import (
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"lukechampine.com/blake3"

	"github.com/heavycaffeiner/stowcloud/go/engine/store/fsatomic"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/ident"
)

// The thumbnail cache.
//
// A directory of files keyed by a digest of the identity tuple, the mtime, the
// size and the preset. A source that changed produces a different key, so a
// stale thumbnail is unreachable rather than evicted: there is no invalidation
// step to get wrong, because the old key is simply never asked for again.
//
// The whole cache is disposable. Delete the directory and previews regenerate.

// Key identifies one cached thumbnail.
type Key struct {
	Ident   ident.Ident
	MTimeNs int64
	Size    uint64
	Preset  Preset
	// Width and Height are set only for an exact-size request, which is the
	// compatibility content route. They are part of the key so a sized preview
	// never collides with a preset one, or with a different size.
	Width  int
	Height int
}

// String is the key's on-disk name.
//
// A digest rather than the fields laid out, because a path built from a
// filename is a path an attacker chooses. The digest is the whole name, so
// there is nothing in it that came from a caller.
func (k Key) String() string {
	h := blake3.New(16, nil)
	// Every field is written with its name, a fixed encoding and a separator,
	// so two different keys cannot produce the same byte sequence by running
	// fields together.
	writeField(h, "share", uint64(k.Ident.Share))
	writeField(h, "dev", k.Ident.Dev)
	writeField(h, "ino", k.Ident.Ino)
	if k.Ident.Btime != nil {
		writeSigned(h, "btime", *k.Ident.Btime)
	} else {
		writeField(h, "nobtime", 0)
	}
	writeSigned(h, "mtime", k.MTimeNs)
	writeField(h, "size", k.Size)
	writeField(h, "preset", uint64(k.Preset))
	writeSigned(h, "w", int64(k.Width))
	writeSigned(h, "h", int64(k.Height))
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}

func writeSigned(h io.Writer, name string, v int64) {
	// The bit pattern, not a narrowing: the digest only needs two different
	// values to produce two different keys, and a signed count reinterpreted is
	// lossless in both directions.
	writeField(h, name, uint64(v)) //nolint:gosec // G115: the bit pattern is the point; see above.
}

func writeField(h io.Writer, name string, v uint64) {
	// hash.Hash never fails, which is what the interface documents.
	_, _ = h.Write([]byte(name + "=" + strconv.FormatUint(v, 10) + ";")) //nolint:errcheck // hash.Hash.Write cannot fail.
}

// Cache is the on-disk thumbnail store.
type Cache struct {
	dir string
}

// NewCache opens a cache directory.
func NewCache(dir string) (*Cache, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("preview: creating the cache: %w", err)
	}
	return &Cache{dir: dir}, nil
}

// Dir is the cache directory.
func (c *Cache) Dir() string { return c.dir }

// path is where a key lives.
//
// The first two characters of the digest are a subdirectory, so a large cache
// does not become one directory with a million entries in it.
func (c *Cache) path(k Key) string {
	name := k.String()
	return filepath.Join(c.dir, name[:2], name)
}

// Open returns a cached thumbnail, or reports that there is none.
func (c *Cache) Open(k Key) (*os.File, bool) {
	f, err := os.Open(c.path(k)) //nolint:gosec // G304: the whole name is a digest this package computed.
	if err != nil {
		return nil, false
	}
	return f, true
}

// Put stores a thumbnail.
//
// Staged and published by an atomic rename with the parent directory synced, so
// a crash mid-write leaves the old entry rather than a half thumbnail a later
// Open would serve as a broken image until the source changed.
func (c *Cache) Put(k Key, write func(*os.File) error) error {
	dest := c.path(k)
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return fmt.Errorf("preview: creating a cache shard: %w", err)
	}
	if err := fsatomic.ReplaceFileDurable(dest, 0o600, write); err != nil {
		return fmt.Errorf("preview: publishing a thumbnail: %w", err)
	}
	return nil
}

// Negative is why a source has no thumbnail, cached so a corrupt file in a
// folder does not cost a worker on every listing.
type Negative uint8

const (
	NegativeNone Negative = iota
	// NegativeTooLarge is a decode limit refusing.
	NegativeTooLarge
	// NegativeUnsupported is a format with no decoder.
	NegativeUnsupported
	// NegativeNotImplemented is video.
	NegativeNotImplemented
	// NegativeDecodeFailed is a file that is not what it claimed to be.
	NegativeDecodeFailed
	// NegativeWorkerDied is a worker killed on this input.
	NegativeWorkerDied
)

// Lifetime is how long a negative result is trusted.
//
// They differ because the facts differ. A file too large now will be too large
// next time, and so will one in a format this build cannot read, so those are
// held for a long time. A worker death might have been the machine rather than
// the file, so it is held briefly and then retried.
func (n Negative) Lifetime() time.Duration {
	switch n {
	case NegativeTooLarge, NegativeUnsupported, NegativeNotImplemented:
		return 30 * 24 * time.Hour
	case NegativeDecodeFailed:
		return 24 * time.Hour
	case NegativeWorkerDied:
		return 5 * time.Minute
	}
	return 0
}

func (n Negative) String() string {
	switch n {
	case NegativeTooLarge:
		return "too large"
	case NegativeUnsupported:
		return "unsupported"
	case NegativeNotImplemented:
		return "not implemented"
	case NegativeDecodeFailed:
		return "decode failed"
	case NegativeWorkerDied:
		return "worker died"
	}
	return "none"
}

// Negatives is the in-memory negative cache.
//
// A file that failed to decode will fail again, and without this a grid full of
// corrupt files re-runs the worker on every scroll.
//
// In memory rather than on disk, and that is deliberate: a negative result is
// a statement about a decode attempt, not about the file, and a restart is a
// reasonable moment to try again. It also means a bad deploy cannot leave a
// folder permanently thumbnail-free.
//
// It carries its own mutex. Every caller is a request goroutine, so a type that
// needed one caller to remember the lock is one where the next caller is the
// data race.
type Negatives struct {
	mu      sync.Mutex
	entries map[string]negativeEntry
}

type negativeEntry struct {
	reason  Negative
	expires time.Time
}

// NewNegatives builds the negative cache.
func NewNegatives() *Negatives { return &Negatives{entries: map[string]negativeEntry{}} }

// Get reports a remembered failure, if it has not expired.
func (n *Negatives) Get(k Key, now time.Time) (Negative, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()

	e, ok := n.entries[k.String()]
	if !ok || now.After(e.expires) {
		return NegativeNone, false
	}
	return e.reason, true
}

// Put remembers a failure.
func (n *Negatives) Put(k Key, reason Negative, now time.Time) {
	if reason == NegativeNone {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.entries[k.String()] = negativeEntry{reason: reason, expires: now.Add(reason.Lifetime())}
}

// Len is how many failures are remembered, which the sweep reports.
func (n *Negatives) Len() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.entries)
}

// Sweep drops expired entries so the map does not grow without bound. The
// maintenance loop calls it.
func (n *Negatives) Sweep(now time.Time) int {
	n.mu.Lock()
	defer n.mu.Unlock()

	dropped := 0
	for k, e := range n.entries {
		if now.After(e.expires) {
			delete(n.entries, k)
			dropped++
		}
	}
	return dropped
}
