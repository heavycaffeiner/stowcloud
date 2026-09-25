//go:build linux

package files

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	core "github.com/heavycaffeiner/stowcloud/go/internal/feature/files"
	previewlimits "github.com/heavycaffeiner/stowcloud/go/internal/feature/preview/limits"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/archive"
)

const (
	archiveMaxRoots = 256
	archiveNameMax  = 200
)

const (
	archiveIncompleteName = "__stowcloud_incomplete__.txt"
	archiveIncompleteBody = "This archive is incomplete. One or more requested entries were omitted because an archive bound was reached or an entry changed while it was being read.\n"
)

const (
	archiveContentEntries = previewlimits.ArchivePackedEntries
	archiveContentBytes   = previewlimits.ArchivePackedBytes
)

var errArchiveBounded = errors.New("archive bounds reached")

// ArchiveGate limits archive streams and ZIP directory parses.
type ArchiveGate struct {
	mu     sync.Mutex
	active int
	limit  int
}

// NewArchiveGate returns an empty archive gate. A zero limit uses the compiled default.
func NewArchiveGate() *ArchiveGate { return &ArchiveGate{} }

func (g *ArchiveGate) SetLimit(limit int) {
	if limit <= 0 {
		limit = previewlimits.ConcurrentArchives
	}
	g.mu.Lock()
	g.limit = limit
	g.mu.Unlock()
}

func (g *ArchiveGate) TryAcquire() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	limit := g.limit
	if limit <= 0 {
		limit = previewlimits.ConcurrentArchives
	}
	if g.active >= limit {
		return false
	}
	g.active++
	return true
}

func (g *ArchiveGate) Release() {
	g.mu.Lock()
	if g.active > 0 {
		g.active--
	}
	g.mu.Unlock()
}

// ArchiveBusy classifies a request refused because archive capacity is full.
func ArchiveBusy() error {
	return fmt.Errorf("archive capacity exhausted")
}

func archiveToken() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

func archiveFilename(requested string) (string, bool) {
	if requested == "" {
		return "archive.zip", true
	}
	if len(requested) > archiveNameMax {
		return "", false
	}
	for i := 0; i < len(requested); i++ {
		c := requested[i]
		if c < 0x20 || c == 0x7f || c == '"' || c == '\\' || c == '/' {
			return "", false
		}
	}
	if len(requested) < 4 || requested[len(requested)-4:] != ".zip" {
		// Match the previous case-insensitive suffix behavior without importing
		// strings into the hot path for the ordinary already-suffixed case.
		lower := requested
		for i := range lower {
			if lower[i] >= 'A' && lower[i] <= 'Z' {
				lower = lower[:i] + string(lower[i]+('a'-'A')) + lower[i+1:]
			}
		}
		if len(lower) < 4 || lower[len(lower)-4:] != ".zip" {
			requested += ".zip"
		}
	}
	return requested, true
}

// ArchiveVisit is the callback used by BuildArchive to enumerate entries.
type ArchiveVisit func(core.WalkEntry, *core.Stream) error

// ArchiveWalk enumerates entries for one archive request.
type ArchiveWalk func(context.Context, ArchiveVisit) error

// BuildArchive streams one bounded archive and always closes its central directory.
// The response may already be committed when an error is returned; the incomplete
// marker makes that partial result explicit.
func BuildArchive(ctx context.Context, w io.Writer, name string, walk ArchiveWalk, logger *slog.Logger) error {
	z := archive.NewWriter(w)
	builder := archiveBuilder{z: z}
	walkErr := walk(ctx, func(entry core.WalkEntry, stream *core.Stream) error {
		if !entry.IsDir && !entry.Readable && logger != nil {
			logger.Warn("skipped an unreadable entry", "path", entry.RelPath)
		}
		return builder.add(entry, stream)
	})
	if walkErr != nil {
		builder.incomplete = true
	}
	if merr := builder.addMarker(); merr != nil && walkErr == nil {
		walkErr = merr
	}
	cerr := z.Close()
	if walkErr != nil {
		return walkErr
	}
	if cerr != nil {
		return fmt.Errorf("closing the archive %q: %w", name, cerr)
	}
	return nil
}

type archiveBuilder struct {
	z          *archive.Writer
	entries    int64
	packed     uint64
	incomplete bool
	names      map[string]struct{}
}

type archiveEntryReader struct {
	source     io.Reader
	remaining  uint64
	read       uint64
	incomplete bool
}

func (r *archiveEntryReader) Read(p []byte) (int, error) {
	if r.remaining == 0 {
		var extra [1]byte
		n, err := r.source.Read(extra[:])
		if n > 0 || (err != nil && !errors.Is(err, io.EOF)) {
			r.incomplete = true
		}
		return 0, io.EOF
	}
	if uint64(len(p)) > r.remaining {
		p = p[:r.remaining]
	}
	n, err := r.source.Read(p)
	if n > 0 {
		r.remaining -= uint64(n)
		r.read += uint64(n)
	}
	if err != nil {
		if !errors.Is(err, io.EOF) || r.remaining > 0 {
			r.incomplete = true
		}
		return n, io.EOF
	}
	return n, nil
}

func (b *archiveBuilder) add(entry core.WalkEntry, stream *core.Stream) error {
	if b.entries >= archiveContentEntries {
		b.incomplete = true
		return errArchiveBounded
	}
	b.entries++
	if !entry.Readable {
		b.incomplete = true
		return nil
	}
	if entry.IsDir {
		if err := b.z.AddDir(entry.RelPath, time.Unix(0, entry.MTimeNs)); err != nil {
			return err
		}
		b.rememberName(entry.RelPath)
		return nil
	}
	if b.packed >= archiveContentBytes || entry.Size > archiveContentBytes-b.packed {
		b.incomplete = true
		return errArchiveBounded
	}
	if stream == nil {
		b.incomplete = true
		return nil
	}
	reader := &archiveEntryReader{source: stream, remaining: entry.Size}
	if err := b.z.AddFile(entry.RelPath, reader, time.Unix(0, entry.MTimeNs)); err != nil {
		return err
	}
	b.rememberName(entry.RelPath)
	b.packed += reader.read
	if reader.incomplete {
		b.incomplete = true
	}
	return nil
}

func (b *archiveBuilder) rememberName(name string) {
	if b.names == nil {
		b.names = make(map[string]struct{})
	}
	b.names[name] = struct{}{}
}

func (b *archiveBuilder) addMarker() error {
	if !b.incomplete {
		return nil
	}
	name := archiveIncompleteName
	for suffix := 2; ; suffix++ {
		if _, exists := b.names[name]; !exists {
			break
		}
		name = fmt.Sprintf("__stowcloud_incomplete__%d.txt", suffix)
	}
	return b.z.AddBytes(name, []byte(archiveIncompleteBody), time.Unix(0, 0))
}
