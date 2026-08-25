//go:build linux

package core

import (
	"context"
	"errors"
	"io"

	"github.com/heavycaffeiner/stowcloud/go/internal/acl"
	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
)

// Stream is a bounded-memory reader over one already-open file, restricted to
// [start, end). It reads in fixed-size chunks, so memory does not scale with
// file size, and it holds the same descriptor for its whole lifetime, so a
// rename-based atomic replace by another process does not change what is being
// read.
type Stream struct {
	f   *vfs.File
	pos uint64
	end uint64
}

// Remaining is the bytes left to read, which is what Content-Length is built
// from.
func (s *Stream) Remaining() uint64 { return s.end - min64(s.pos, s.end) }

// Read implements io.Reader, clamped to the requested range.
func (s *Stream) Read(p []byte) (int, error) {
	if s.pos >= s.end {
		return 0, io.EOF
	}
	remaining := s.end - s.pos
	cap := int(min64(remaining, uint64(len(p)))) //nolint:gosec // G115 reads the conversion: the value is clamped by len(p), which is already int.
	cap = min(cap, streamChunk)
	if cap == 0 {
		return 0, nil
	}
	n, err := s.f.ReadAt(p[:cap], int64(s.pos)) //nolint:gosec // G115 reads the conversion: the offset is the file's own size, which the descriptor addressed at open.
	if n == 0 {
		// The file shrank out from under us (a concurrent write) before the
		// end of the range. A short read is still an honest stream: io.EOF is
		// just "no more bytes", which is exactly true.
		s.end = s.pos
		return 0, io.EOF
	}
	s.pos += uint64(n) //nolint:gosec // G115 reads the conversion: ReadAt returns a non-negative int.
	if err != nil && !errors.Is(err, io.EOF) {
		return n, err
	}
	return n, nil
}

// Close releases the descriptor. A stream reader owns its handle.
func (s *Stream) Close() error { return s.f.Close() }

// streamChunk is the bytes read per syscall, however large the callers buffer
// is. Memory must not scale with file size.
const streamChunk = 256 << 10

// FidEntry is what a protocol layer needs to build download headers without a
// second round trip through the store.
type FidEntry struct {
	Name     string
	Size     uint64
	MTime    int64
	ETag     string
	ETagWeak bool
}

// OpenStream opens a file for ranged reading, ACL-checked like every other
// entry point. range is an inclusive (start, end) byte range clamped to the
// actual size; nil reads the whole file.
func (c *Core) OpenStream(ctx context.Context, r Resolved, range_ *[2]uint64) (FidEntry, *Stream, error) {
	if err := r.Require(acl.Download); err != nil {
		return FidEntry{}, nil, err
	}
	f, err := r.root.OpenRead(r.path, vfs.IntentRead)
	if err != nil {
		return FidEntry{}, nil, mapVFSErr(err)
	}
	st, serr := f.Stat()
	if serr != nil {
		_ = f.Close() //nolint:errcheck // the stat failure is the answer; the descriptor is going away.
		return FidEntry{}, nil, mapVFSErr(serr)
	}
	if st.Kind.IsDir() {
		_ = f.Close() //nolint:errcheck // the denial is the answer; the descriptor is going away.
		return FidEntry{}, nil, errf(ErrDenied, "stream a directory")
	}
	etag, weak := FileETag(st)
	entry := FidEntry{Name: r.path.Name(), Size: st.Size, MTime: st.MtimeNs, ETag: etag, ETagWeak: weak}

	var start, end uint64
	if range_ == nil {
		start, end = 0, st.Size
	} else {
		start = min64((*range_)[0], st.Size)
		rawEnd := satAdd((*range_)[1])
		end = max64(min64(rawEnd, st.Size), start)
	}
	return entry, &Stream{f: f, pos: start, end: end}, nil
}

// RandomRead is a whole file open for reading at arbitrary offsets, which is
// what a format that keeps its index at the end needs: a zip's central
// directory is the last thing in the file.
//
// The caller closes it. It carries the size because every such format needs
// one to find its index, and asking the caller to stat again would be a second
// answer that can disagree with the descriptor it is reading through.
type RandomRead struct {
	f    *vfs.File
	Size int64
}

// ReadAt implements io.ReaderAt against the open descriptor.
func (r *RandomRead) ReadAt(p []byte, off int64) (int, error) { return r.f.ReadAt(p, off) }

// Close releases the descriptor.
func (r *RandomRead) Close() error { return r.f.Close() }

// OpenRandom opens a file for reading at arbitrary offsets, ACL-checked like
// every other entry point.
func (c *Core) OpenRandom(ctx context.Context, r Resolved) (FidEntry, *RandomRead, error) {
	if err := r.Require(acl.Read | acl.Download); err != nil {
		return FidEntry{}, nil, err
	}
	f, err := r.root.OpenRead(r.path, vfs.IntentRead)
	if err != nil {
		return FidEntry{}, nil, mapVFSErr(err)
	}
	st, serr := f.Stat()
	if serr != nil {
		_ = f.Close() //nolint:errcheck // the stat failure is the answer; the descriptor is going away.
		return FidEntry{}, nil, mapVFSErr(serr)
	}
	if st.Kind.IsDir() {
		_ = f.Close() //nolint:errcheck // the denial is the answer; the descriptor is going away.
		return FidEntry{}, nil, errf(ErrDenied, "read a directory")
	}
	etag, weak := FileETag(st)
	return FidEntry{Name: r.path.Name(), Size: st.Size, MTime: st.MtimeNs, ETag: etag, ETagWeak: weak},
		&RandomRead{f: f, Size: int64(st.Size)}, nil //nolint:gosec // G115 reads the conversion: the length is the file's own size, which the descriptor addressed at open.
}

func min64(a, b uint64) uint64 {
	if a < b {
		return a
	}
	return b
}

func max64(a, b uint64) uint64 {
	if a > b {
		return a
	}
	return b
}

// satAdd adds one, saturating so a range end of the maximum stays valid.
func satAdd(v uint64) uint64 {
	if v == ^uint64(0) {
		return v
	}
	return v + 1
}
