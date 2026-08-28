//go:build linux

package vfs

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/limits"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
	"golang.org/x/sys/unix"
)

// File is an open regular file or directory reached through a ShareRoot. It
// owns its descriptor; the caller closes it.
type File struct{ f *os.File }

func (f *File) Close() error { return f.f.Close() }

// Name reports the spelling this handle was opened under. It is a
// diagnostic only, never used to reopen anything.
func (f *File) Name() string { return f.f.Name() }

// OSFile is the underlying file, for a caller that must pass this descriptor
// to another process.
//
// It returns the *os.File rather than the number, so the descriptor stays
// owned by something the runtime can see and the keepalive rule still holds at
// the point it is used: a bare number can be closed by a finalizer underneath
// the syscall that uses it.
//
// The preview pool is the caller. Its worker is never told a path, so a
// descriptor the parent opened is the only way that process reaches a file.
func (f *File) OSFile() *os.File { return f.f }

// ReadAt is pread: it never moves the descriptor's own cursor, so two callers
// sharing one handle do not move each other's position.
func (f *File) ReadAt(b []byte, off int64) (int, error) { return f.f.ReadAt(b, off) }

// WriteAt is pwrite: it leaves the descriptor's cursor untouched too.
func (f *File) WriteAt(b []byte, off int64) (int, error) { return f.f.WriteAt(b, off) }

func (f *File) Truncate(n int64) error { return f.f.Truncate(n) }

// Stat answers from the open descriptor, so it describes the file this
// handle holds rather than whatever currently answers to its name.
func (f *File) Stat() (Stat, error) { return statOf(f.f) }

// Space reports the filesystem behind this handle, agreeing with
// ShareRoot.Space for the same file, for a caller that already holds the
// file open and would otherwise re-resolve a name it has in hand.
func (f *File) Space() (FsSpace, error) { return spaceOf(f.f) }

// SyncData makes the file's content durable. It is fdatasync rather than
// fsync: making the name durable is a separate act, done by syncing the
// parent directory, and fsyncing the file too would pay for the same
// metadata twice.
func (f *File) SyncData() error {
	if err := withFdErr(f.f, unix.Fdatasync); err != nil {
		return mapErrno("sync file data", err)
	}
	return nil
}

// SetMode applies mode to the open descriptor via fchmod, exactly, bypassing
// whatever O_CREAT's own mode argument would have done through the process
// umask. A replacement needs this before publication: without the
// original's mode restored, whatever else shares the directory loses access
// it had a moment earlier.
func (f *File) SetMode(mode uint32) error {
	if err := withFdErr(f.f, func(fd int) error { return unix.Fchmod(fd, mode) }); err != nil {
		return mapErrno("apply mode", err)
	}
	return nil
}

// SetOwner applies o to the open descriptor. EPERM here is the ordinary
// answer for an unprivileged process and is returned rather than swallowed,
// so the caller decides whether it matters.
func (f *File) SetOwner(o Owner) error { return chownFd(f.f, o) }

// statMask is what every stat in this package asks for. STATX_BTIME is why
// this is statx and not fstat.
const statMask = unix.STATX_BASIC_STATS | unix.STATX_BTIME

// OpenRead opens p for reading, under the access mode intent states. There is
// no fallback chain: it never widens an O_RDONLY open to O_RDWR after a
// failure, because O_RDONLY does not fail with EACCES on a merely readable
// file, so nothing needs widening. IntentReadWrite exists for exactly one
// caller, the upload finalizer verifying a completed part file's digest.
func (r *ShareRoot) OpenRead(p SafePath, intent AccessIntent) (*File, error) {
	access := uint64(unix.O_RDONLY)
	if intent == IntentReadWrite {
		access = unix.O_RDWR
	}
	f, err := r.openLeaf(p, access|unix.O_CLOEXEC)
	if err != nil {
		return nil, err
	}
	return &File{f: f}, nil
}

// Stat resolves p under the share's policy and stats the leaf. There is no
// O_NOFOLLOW here: under SymlinkDeny the resolve flags already refuse a
// symlink outright, and under a policy that follows one, following it is
// what stat means.
func (r *ShareRoot) Stat(p SafePath) (Stat, error) {
	f, err := r.openLeaf(p, unix.O_PATH|unix.O_CLOEXEC)
	if err != nil {
		return Stat{}, err
	}
	defer closeAfter(f, "stat handle")
	return statOf(f)
}

// DirDev is the device the directory at p sits on, or for a file, the device
// of the directory that holds it. A caller deciding between a rename and a
// copy asks about the directory an entry lands in, not the share root, since
// a device mounted below the root is a boundary a rename cannot cross.
func (r *ShareRoot) DirDev(p SafePath) (uint64, error) {
	dir, err := r.resolveDirOrParent(p)
	if err != nil {
		return 0, err
	}
	defer closeAfter(dir, "device probe")
	st, err := statOf(dir)
	if err != nil {
		return 0, err
	}
	return st.Dev, nil
}

func statOf(f *os.File) (Stat, error) {
	var stx unix.Statx_t
	if err := withFdErr(f, func(fd int) error {
		return unix.Statx(fd, "", unix.AT_EMPTY_PATH, statMask, &stx)
	}); err != nil {
		return Stat{}, mapErrno("statx", err)
	}
	return statxToStat(&stx), nil
}

func statxToStat(stx *unix.Statx_t) Stat {
	s := Stat{
		Dev:     unix.Mkdev(stx.Dev_major, stx.Dev_minor),
		Ino:     stx.Ino,
		MtimeNs: timestampNs(stx.Mtime.Sec, stx.Mtime.Nsec),
		Size:    stx.Size,
		Mode:    uint32(stx.Mode),
		UID:     stx.Uid,
		GID:     stx.Gid,
		Nlink:   stx.Nlink,
		Kind:    kindOfMode(uint32(stx.Mode)),
	}
	// Read off the mask rather than assumed present: an absent timestamp and
	// a zero one are different facts a caller building an identity tuple
	// needs told apart.
	if stx.Mask&unix.STATX_BTIME != 0 {
		ns := timestampNs(stx.Btime.Sec, stx.Btime.Nsec)
		s.BtimeNs = &ns
	}
	if stx.Mask&unix.STATX_CTIME != 0 {
		ns := timestampNs(stx.Ctime.Sec, stx.Ctime.Nsec)
		s.CtimeNs = &ns
	}
	return s
}

// timestampNs saturates on overflow instead of wrapping: the seconds field
// is read from a filesystem this process does not control, so a value wide
// enough to overflow the multiply counts as untrusted input, not something
// that cannot happen.
func timestampNs(sec int64, nsec uint32) int64 {
	const perSecond = int64(1_000_000_000)
	const bound = math.MaxInt64 / perSecond
	switch {
	case sec >= bound:
		return math.MaxInt64
	case sec <= -bound:
		return math.MinInt64
	}
	return sec*perSecond + int64(nsec)
}

func kindOfMode(mode uint32) Kind {
	switch mode & unix.S_IFMT {
	case unix.S_IFDIR:
		return KindDir
	case unix.S_IFREG:
		return KindFile
	case unix.S_IFLNK:
		return KindSymlink
	default:
		return KindOther
	}
}

// dirReadBufBytes is the reused getdents64 buffer for one directory walk.
const dirReadBufBytes = 32 << 10

// Kernel dirent64 record layout: d_ino at offset 0, d_reclen at 16, d_type at
// 18, and a NUL-terminated d_name from 19 onward. This is the ABI, identical
// across architectures, not a local choice.
const (
	direntInoOffset    = 0
	direntReclenOffset = 16
	direntTypeOffset   = 18
	direntNameOffset   = 19
)

// ReadDirFunc streams the entries of p, calling fn once per entry until fn
// returns false or the directory is exhausted.
//
// It parses getdents64 directly against the kernel ABI rather than through a
// name-only helper, because d_type avoids a statx per entry and d_ino lets a
// caller order a later stat batch to make the disk seek forward. It never
// materializes the directory itself: another program may have written it to
// any size, so an unbounded collection here would be bounded only by memory.
func (r *ShareRoot) ReadDirFunc(p SafePath, policy ReservedPolicy, fn func(DirEntry) bool) error {
	d, err := r.openLeaf(p, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC)
	if err != nil {
		return err
	}
	defer closeAfter(d, "directory being read")

	buf := make([]byte, dirReadBufBytes)
	for {
		n, err := withFd(d, func(fd int) (int, error) { return unix.Getdents(fd, buf) })
		if err != nil {
			return mapErrno("read directory", err)
		}
		if n == 0 {
			return nil
		}
		for off := 0; off < n; {
			entry, reclen, err := parseDirent(buf[off:n])
			if err != nil {
				return err
			}
			off += reclen
			if entry.Name == "." || entry.Name == ".." {
				continue
			}
			if policy == HideReserved && IsReservedName(entry.Name) {
				continue
			}
			if !fn(entry) {
				return nil
			}
		}
	}
}

// ReadDir collects entries into a slice, refusing past
// limits.DirEntriesBuffered rather than materializing an unbounded list. A
// caller unable to tolerate the refusal streams with ReadDirFunc instead.
func (r *ShareRoot) ReadDir(p SafePath, policy ReservedPolicy) ([]DirEntry, error) {
	return collectBoundedEntries(limits.DirEntriesBuffered, func(fn func(DirEntry) bool) error {
		return r.ReadDirFunc(p, policy, fn)
	})
}

// collectBoundedEntries is the buffering half of ReadDir, kept separate from
// the syscalls so a test can prove the bound itself refuses rather than that
// a large directory happens to fail some other way.
func collectBoundedEntries(bound int, stream func(func(DirEntry) bool) error) ([]DirEntry, error) {
	out := make([]DirEntry, 0, 64)
	over := false
	err := stream(func(e DirEntry) bool {
		if len(out) >= bound {
			over = true
			return false
		}
		out = append(out, e)
		return true
	})
	if err != nil {
		return nil, err
	}
	if over {
		return nil, limits.Exceed("directory entries buffered", int64(bound), int64(bound)+1)
	}
	return out, nil
}

// parseDirent reads one dirent64 record and returns its length so the caller
// can advance to the next. A record claiming more bytes than the buffer
// holds, or too short to carry a header, fails closed rather than being
// skipped: the buffer came from the kernel, but this package still parses it
// as untrusted bytes.
func parseDirent(rec []byte) (DirEntry, int, error) {
	if len(rec) < direntNameOffset {
		return DirEntry{}, 0, fmt.Errorf("read directory: %d bytes cannot hold a dirent header", len(rec))
	}
	reclen := int(binary.NativeEndian.Uint16(rec[direntReclenOffset:]))
	if reclen <= direntNameOffset || reclen > len(rec) {
		return DirEntry{}, 0, fmt.Errorf("read directory: a record claims %d of %d bytes returned", reclen, len(rec))
	}
	name := rec[direntNameOffset:reclen]
	if i := bytes.IndexByte(name, 0); i >= 0 {
		name = name[:i]
	}
	return DirEntry{
		Name: string(name),
		Kind: kindOfDirentType(rec[direntTypeOffset]),
		Ino:  binary.NativeEndian.Uint64(rec[direntInoOffset:]),
	}, reclen, nil
}

// kindOfDirentType stays conservative on DT_UNKNOWN, which some filesystems
// and most FUSE mounts return, rather than paying a statx per entry to
// resolve it. A caller needing certainty stats the specific entry.
func kindOfDirentType(t uint8) Kind {
	switch t {
	case unix.DT_DIR:
		return KindDir
	case unix.DT_REG:
		return KindFile
	case unix.DT_LNK:
		return KindSymlink
	default:
		return KindOther
	}
}

// Space reports byte accounting for the filesystem holding p, not for the
// filesystem holding the share root: a device mounted below the root reports
// its own numbers, not the root's borrowed ones.
func (r *ShareRoot) Space(p SafePath) (FsSpace, error) {
	dir, err := r.resolveDirOrParent(p)
	if err != nil {
		return FsSpace{}, err
	}
	defer closeAfter(dir, "space probe")
	return spaceOf(dir)
}

// resolveDirOrParent resolves p as a directory, falling back to its parent
// when p names a file. Both ENOENT and ENOTDIR arrive as ErrNotFound, and the
// second is the ordinary shape of "p names a file, not a directory": the
// parent holding it is on the same filesystem, which is the only fact the
// caller needs.
func (r *ShareRoot) resolveDirOrParent(p SafePath) (*os.File, error) {
	dir, err := r.resolveDir(p.Components())
	if err == nil {
		return dir, nil
	}
	if !p.IsRoot() && errors.Is(err, ErrNotFound) {
		return r.resolveDir(p.Parent().Components())
	}
	return nil, err
}

// spaceOf runs the accounting against an already-open descriptor, so the
// path-based probe and the open-handle probe cannot disagree about what a
// block size means.
func spaceOf(f *os.File) (FsSpace, error) {
	var sfs unix.Statfs_t
	if err := withFdErr(f, func(fd int) error { return unix.Fstatfs(fd, &sfs) }); err != nil {
		return FsSpace{}, mapErrno("statfs", err)
	}
	bsize, nerr := num.Narrow[uint64](sfs.Bsize)
	if nerr != nil {
		return FsSpace{}, fmt.Errorf("statfs block size: %w", nerr)
	}
	return FsSpace{
		Total:     saturatingMul(sfs.Blocks, bsize),
		Free:      saturatingMul(sfs.Bfree, bsize),
		Available: saturatingMul(sfs.Bavail, bsize),
	}, nil
}

func saturatingMul(a, b uint64) uint64 {
	if a == 0 || b == 0 {
		return 0
	}
	if a > math.MaxUint64/b {
		return math.MaxUint64
	}
	return a * b
}
