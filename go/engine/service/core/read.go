//go:build linux

package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
)

// streamChunk bounds one read syscall whatever buffer the caller brought, so
// the memory a download costs does not scale with the file.
const streamChunk = 256 << 10

// Stream is a bounded reader over one already-open file, restricted to
// [pos, end).
//
// It holds the descriptor it was opened with for its whole life. A download
// can run for minutes, and re-opening by path mid-transfer would splice the
// bytes of two different files into one response body when another process
// replaces the file by rename.
type Stream struct {
	f   *vfs.File
	pos uint64
	end uint64
}

// Remaining is how many bytes are left in the range, which is what a
// Content-Length is built from.
func (s *Stream) Remaining() uint64 { return s.end - min(s.pos, s.end) }

// Read fills at most streamChunk bytes per call.
//
// A read that returns nothing before the end of the range means the file
// shrank under a concurrent write. The stream ends there and reports io.EOF
// rather than an error: EOF says "no more bytes", which is exactly true, and
// padding the range would invent content.
func (s *Stream) Read(p []byte) (int, error) {
	if s.pos >= s.end {
		return 0, io.EOF
	}
	off, err := num.Narrow[int64](s.pos)
	if err != nil {
		return 0, fmt.Errorf("stream position: %w", err)
	}
	// A range wider than an int is not a range one call can take, and the
	// chunk bound is what decides the size anyway.
	want, err := num.Narrow[int](s.end - s.pos)
	if err != nil {
		want = streamChunk
	}
	want = min(want, len(p), streamChunk)
	if want == 0 {
		return 0, nil
	}
	n, err := s.f.ReadAt(p[:want], off)
	if n == 0 {
		s.end = s.pos
		return 0, io.EOF
	}
	read, nerr := num.Narrow[uint64](n)
	if nerr != nil {
		return n, fmt.Errorf("stream read length: %w", nerr)
	}
	s.pos += read
	if err != nil && !errors.Is(err, io.EOF) {
		return n, err
	}
	return n, nil
}

// Close releases the descriptor. Whoever received the stream owns it.
func (s *Stream) Close() error { return s.f.Close() }

// FidEntry is what a protocol needs to build download headers for the file a
// stream is reading. It comes from the open descriptor rather than a second
// stat by path, so the headers cannot describe a different file than the one
// whose bytes follow them.
type FidEntry struct {
	Name     string
	Size     uint64
	MTime    int64
	ETag     string
	ETagWeak bool
}

// OpenStream opens the resolved file and bounds a stream over it. range_ is
// an inclusive byte pair; nil reads the whole file.
//
// A start past the size, or a start past the end, produces an empty stream
// rather than an error. Whether an unsatisfiable range is a wire error is
// the protocol layer's decision from FidEntry.Size; the core only clamps.
func (c *Core) OpenStream(ctx context.Context, r Resolved, range_ *[2]uint64) (FidEntry, *Stream, error) {
	// Download alone, not Read: a drop-style grant that hands out bytes
	// without letting the holder inspect the tree is a real configuration.
	if err := r.Require(acl.Download); err != nil {
		return FidEntry{}, nil, err
	}
	f, entry, err := c.openFid(r, "stream a directory")
	if err != nil {
		return FidEntry{}, nil, err
	}

	start, end := uint64(0), entry.Size
	if range_ != nil {
		start = min(range_[0], entry.Size)
		end = max(min(satAdd(range_[1]), entry.Size), start)
	}
	return entry, &Stream{f: f, pos: start, end: end}, nil
}

// RandomRead opens an entire file for reading at arbitrary offsets, which suits
// formats storing their index at the end. A zip's central directory sits last in
// the file, so a forward-only stream cannot serve a zip browser.
type RandomRead struct {
	f *vfs.File

	// Size is exported because every such format needs it to find its
	// index. Asking the caller to stat again would produce a second answer
	// that can disagree with the descriptor being read.
	Size int64
}

func (r *RandomRead) ReadAt(p []byte, off int64) (int, error) { return r.f.ReadAt(p, off) }

func (r *RandomRead) Close() error { return r.f.Close() }

// OpenRandom opens the resolved file for random access. The caller closes it.
func (c *Core) OpenRandom(ctx context.Context, r Resolved) (FidEntry, *RandomRead, error) {
	// Both bits: a random reader exists to parse a container format, which
	// is inspecting the tree and taking its bytes at once.
	if err := r.Require(acl.Read | acl.Download); err != nil {
		return FidEntry{}, nil, err
	}
	f, entry, err := c.openFid(r, "read a directory")
	if err != nil {
		return FidEntry{}, nil, err
	}
	size, err := num.Narrow[int64](entry.Size)
	if err != nil {
		c.closeAfterFailure(f)
		return FidEntry{}, nil, err
	}
	return entry, &RandomRead{f: f, Size: size}, nil
}

// openFid opens the resolved path and describes it from the open descriptor,
// which is the truth the reader will read rather than whatever currently
// answers to the name. refusal names the operation in the directory refusal,
// so the message says what the caller tried to do.
func (c *Core) openFid(r Resolved, refusal string) (*vfs.File, FidEntry, error) {
	f, err := r.root.OpenRead(r.path, vfs.IntentRead)
	if err != nil {
		return nil, FidEntry{}, mapVFSErr(err)
	}
	st, err := f.Stat()
	if err != nil {
		c.closeAfterFailure(f)
		return nil, FidEntry{}, mapVFSErr(err)
	}
	if st.Kind.IsDir() {
		c.closeAfterFailure(f)
		return nil, FidEntry{}, errf(ErrDenied, "%s", refusal)
	}
	etag, weak := FileETag(st)
	return f, FidEntry{
		Name:     r.path.Name(),
		Size:     st.Size,
		MTime:    st.MtimeNs,
		ETag:     etag,
		ETagWeak: weak,
	}, nil
}

// closeAfterFailure drops a descriptor on a path that is already returning an
// error. The close failure has nothing to add to the error the caller is
// about to receive.
func (c *Core) closeAfterFailure(f *vfs.File) {
	if err := f.Close(); err != nil {
		c.warn("core: closing a descriptor after a refused open", "error", err)
	}
}

// WalkEntry is one row of a server-side archive.
type WalkEntry struct {
	// RelPath is relative to the archive root, slash-joined, which is the
	// natural zip entry name.
	RelPath string
	IsDir   bool

	// Readable false is an entry that exists and was skipped. The visitor
	// records it; it must not fail the archive.
	Readable bool
	Size     uint64
	MTimeNs  int64
}

// ArchiveWalk enumerates a subtree and hands each readable file to visit as
// an open stream. The zip writer lives in the protocol layer, which owns the
// wire format; the core owns what is in the archive and what is skipped.
//
// The visitor receives a stream valid exactly for the duration of the call,
// and the walk closes it before touching the next entry, so an archive of
// any size holds one descriptor at a time. A visitor error aborts the walk
// and comes back unchanged, which is how a client disconnect propagates.
func (c *Core) ArchiveWalk(ctx context.Context, r Resolved, visit func(WalkEntry, *Stream) error) error {
	if err := r.Require(acl.Read); err != nil {
		return err
	}
	st, err := r.root.Stat(r.path)
	if err != nil {
		return mapVFSErr(err)
	}

	// RelPath starts at the root's own leaf name in both cases. A walk
	// rooted at the share root has no leaf, so its descendants carry their
	// own paths unprefixed.
	base := r.path.Name()
	if !st.Kind.IsDir() {
		// A file root is the whole archive, so an open failure here fails
		// the walk: there is nothing else to put in it.
		entry, stream, oerr := c.OpenStream(ctx, r, nil)
		if oerr != nil {
			return oerr
		}
		verr := visit(WalkEntry{
			RelPath:  base,
			Readable: true,
			Size:     entry.Size,
			MTimeNs:  entry.MTime,
		}, stream)
		return firstErr(verr, stream.Close())
	}
	// The root itself is announced when it is a directory, before anything
	// under it. Only its descendants used to be visited, so archiving an
	// empty directory produced an archive with nothing in it: the caller
	// asked for a directory and got back a zip that extracts to nothing.
	//
	// Skipped for a share root, whose leaf name is empty. A zip member with
	// no name is not a directory entry, and everything beneath it already
	// carries its own path.
	if base != "" {
		if verr := visit(WalkEntry{
			RelPath:  base,
			IsDir:    true,
			Readable: true,
			MTimeNs:  st.MtimeNs,
		}, nil); verr != nil {
			return verr
		}
	}
	return c.walkArchive(ctx, r, base, visit)
}

func (c *Core) walkArchive(ctx context.Context, r Resolved, rel string, visit func(WalkEntry, *Stream) error) error {
	entries, err := r.root.ReadDir(r.path, vfs.HideReserved)
	if err != nil {
		// The directory vanished or turned unreadable after its parent's
		// check. Nothing further under it is reported, and the archive
		// carries on.
		return nil
	}
	for _, e := range entries {
		childPath, jerr := r.path.JoinExisting(e.Name)
		if jerr != nil {
			continue
		}
		child, cerr := c.ResolveUnder(r, childPath, 0)
		if cerr != nil {
			continue
		}
		st, serr := r.root.Stat(childPath)
		if serr != nil {
			continue
		}
		childRel := e.Name
		if rel != "" {
			childRel = rel + "/" + e.Name
		}

		if st.Kind.IsDir() {
			if verr := visit(WalkEntry{RelPath: childRel, IsDir: true, Readable: true}, nil); verr != nil {
				return verr
			}
			// A fresh evaluation at this path, not the root's bits: an
			// unreadable subtree costs one directory row and nothing under
			// it leaks.
			if child.perms.Has(acl.Read) {
				if werr := c.walkArchive(ctx, child, childRel, visit); werr != nil {
					return werr
				}
			}
			continue
		}

		if !child.perms.Has(acl.Read) {
			if verr := visit(WalkEntry{RelPath: childRel, Readable: false}, nil); verr != nil {
				return verr
			}
			continue
		}
		entry, stream, oerr := c.OpenStream(ctx, child, nil)
		if oerr != nil {
			// It vanished between the stat and the open. Skipped, not
			// failed: an archive missing one file and saying so beats a
			// response that dies mid-body.
			if verr := visit(WalkEntry{RelPath: childRel, Readable: false}, nil); verr != nil {
				return verr
			}
			continue
		}
		verr := visit(WalkEntry{
			RelPath:  childRel,
			Readable: true,
			Size:     entry.Size,
			MTimeNs:  entry.MTime,
		}, stream)
		if err := firstErr(verr, stream.Close()); err != nil {
			return err
		}
	}
	return nil
}

// canRead asks the evaluator about this exact path rather than trusting the
// permissions the walk descended with.
func (c *Core) canRead(r Resolved) bool {
	at := acl.Vpath{Share: int64(r.share), Path: aclPath(r.path)}
	return c.acl.Evaluate(int64(r.user), at, acl.Read).Allowed
}

// firstErr prefers the visitor's error over the close that followed it: the
// visitor's is what the caller acts on, and a close failing after the bytes
// were already delivered tells nobody anything.
func firstErr(visit, close error) error {
	if visit != nil {
		return visit
	}
	return close
}

// satAdd adds one, saturating at the maximum, so a range end of the maximum
// stays valid instead of wrapping to zero.
func satAdd(v uint64) uint64 {
	if v == math.MaxUint64 {
		return v
	}
	return v + 1
}
