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
	cap := int(min64(remaining, uint64(len(p))))
	cap = min(cap, streamChunk)
	if cap == 0 {
		return 0, nil
	}
	n, err := s.f.ReadAt(p[:cap], int64(s.pos))
	if n == 0 {
		// The file shrank out from under us (a concurrent write) before the
		// end of the range. A short read is still an honest stream: io.EOF is
		// just "no more bytes", which is exactly true.
		s.end = s.pos
		return 0, io.EOF
	}
	s.pos += uint64(n)
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
		_ = f.Close()
		return FidEntry{}, nil, mapVFSErr(serr)
	}
	if st.Kind.IsDir() {
		_ = f.Close()
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

// openSeekOf is the whole-file seekable reader a zip reader needs: the central
// directory lives at the end of the file.
type openSeekable struct {
	f   *vfs.File
	pos int64
	len int64
}

// Read implements io.Reader.
func (s *openSeekable) Read(p []byte) (int, error) {
	if s.pos >= s.len {
		return 0, io.EOF
	}
	n, err := s.f.ReadAt(p, s.pos)
	s.pos += int64(n)
	return n, err
}

// Seek implements io.Seeker, refusing a position before the start.
func (s *openSeekable) Seek(offset int64, whence int) (int64, error) {
	var target int64
	switch whence {
	case io.SeekStart:
		target = offset
	case io.SeekCurrent:
		target = s.pos + offset
	case io.SeekEnd:
		target = s.len + offset
	default:
		return 0, errf(ErrNotFound, "an invalid seek whence")
	}
	if target < 0 {
		return 0, errf(ErrNotFound, "seek before the start of the file")
	}
	s.pos = target
	return target, nil
}

func (c *Core) OpenSeekable(ctx context.Context, r Resolved) (FidEntry, io.ReadSeeker, error) {
	if err := r.Require(acl.Download); err != nil {
		return FidEntry{}, nil, err
	}
	f, err := r.root.OpenRead(r.path, vfs.IntentRead)
	if err != nil {
		return FidEntry{}, nil, mapVFSErr(err)
	}
	st, serr := f.Stat()
	if serr != nil {
		_ = f.Close()
		return FidEntry{}, nil, mapVFSErr(serr)
	}
	if st.Kind.IsDir() {
		_ = f.Close()
		return FidEntry{}, nil, errf(ErrDenied, "stream a directory")
	}
	etag, weak := FileETag(st)
	return FidEntry{Name: r.path.Name(), Size: st.Size, MTime: st.MtimeNs, ETag: etag, ETagWeak: weak},
		&openSeekable{f: f, len: int64(st.Size)}, nil
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
