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
// A directory of files named by a digest over the identity tuple, mtime, size
// and preset. A changed source yields a different key, so a stale thumbnail
// becomes unreachable rather than being evicted. No invalidation step exists to
// get wrong, because nothing ever requests the old key again.
//
// The cache is entirely disposable. Remove the directory and previews
// regenerate.

// Key names a single cached thumbnail.
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

// String gives the key's on-disk name.
//
// A digest rather than the fields spelled out, because a path assembled from a
// filename is a path the attacker chooses. The digest constitutes the entire
// name, so nothing within it originated from a caller.
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
	// Reinterpreted bits rather than a narrowing conversion. The digest only
	// requires distinct values to yield distinct keys, and reinterpreting a
	// signed count loses nothing in either direction.
	writeField(h, name, uint64(v)) //nolint:gosec // G115: the bit pattern is the point; see above.
}

func writeField(h io.Writer, name string, v uint64) {
	// hash.Hash cannot fail, as its interface documents.
	_, _ = h.Write([]byte(name + "=" + strconv.FormatUint(v, 10) + ";")) //nolint:errcheck // hash.Hash.Write cannot fail.
}

// Cache is the thumbnail store on disk.
type Cache struct {
	dir string
}

// NewCache opens a directory for the cache.
func NewCache(dir string) (*Cache, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("preview: creating the cache: %w", err)
	}
	return &Cache{dir: dir}, nil
}

// Dir is the cache directory.
func (c *Cache) Dir() string { return c.dir }

// path locates a key.
//
// The digest's first two characters form a subdirectory, keeping a large cache
// from collapsing into a single directory holding a million entries.
func (c *Cache) path(k Key) string {
	name := k.String()
	return filepath.Join(c.dir, name[:2], name)
}

// Open returns a cached thumbnail, or reports its absence.
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

// Negative records why a source produced no thumbnail, cached so a corrupt file
// in a folder does not consume a worker on every listing.
type Negative uint8

const (
	NegativeNone Negative = iota
	// NegativeTooLarge records a decode limit rejecting the job.
	NegativeTooLarge
	// NegativeUnsupported records a format with no available decoder.
	NegativeUnsupported
	// NegativeNotImplemented records video.
	NegativeNotImplemented
	// NegativeDecodeFailed records a file that is not what it claimed.
	NegativeDecodeFailed
	// NegativeWorkerDied records a worker killed by this input.
	NegativeWorkerDied
)

// Lifetime states how long a negative result remains trusted.
//
// The durations differ because the underlying facts do. A file too large now
// remains too large later, as does one in a format this build cannot read, so
// both persist for a long time. A worker death may reflect the machine rather
// than the file, so it persists briefly before a retry.
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

// Negatives holds the in-memory negative cache.
//
// A file that failed to decode will fail again, and without this a grid full of
// corrupt files reruns the worker on every scroll.
//
// Kept in memory rather than on disk, deliberately: a negative result describes
// a decode attempt rather than the file itself, and a restart is a sensible
// moment to retry. It also ensures a bad deploy cannot leave a folder
// permanently without thumbnails.
//
// It owns its mutex. Every caller is a request goroutine, so a type relying on
// one caller to remember the lock is a type where the next caller becomes the
// data race.
type Negatives struct {
	mu      sync.Mutex
	entries map[string]negativeEntry
}

type negativeEntry struct {
	reason  Negative
	expires time.Time
}

// NewNegatives constructs the negative cache.
func NewNegatives() *Negatives { return &Negatives{entries: map[string]negativeEntry{}} }

// Get returns a remembered failure when it has not yet expired.
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

// Len counts the remembered failures, which the sweep reports.
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
