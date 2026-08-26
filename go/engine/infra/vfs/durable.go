//go:build linux

package vfs

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
	"golang.org/x/sys/unix"
)

// DurableOpts carries what a durable write needs beyond the write callback
// itself.
type DurableOpts struct {
	// Mode is applied to the staging file verbatim via fchmod, never through
	// the process umask. When the write replaces an existing entry, that
	// entry's own mode wins instead, since the neighbours' access to it
	// survives the replace.
	Mode uint32

	// Owner is applied to a newly created file only. Nil leaves it at the
	// process uid and gid.
	Owner *Owner

	// NoClobber maps to RENAME_NOREPLACE, so publishing over an existing
	// name is refused atomically by the kernel rather than checked first and
	// raced.
	NoClobber bool
}

// Durable reports what a completed write did to the entry it replaced, if
// any.
type Durable struct {
	// Replaced is true when a prior entry occupied the destination name and
	// its mode and ownership were transplanted onto the write.
	Replaced bool

	// OwnerRestore is nil when a replaced entry's uid and gid were both
	// restored, and otherwise the error that refused. EPERM here is the
	// ordinary outcome for an unprivileged process and is reported rather
	// than swallowed, so the caller decides whether it matters. Mode is not
	// reported this way: a mode that could not be restored fails the write
	// outright.
	OwnerRestore error
}

// WriteDurable stages content under a reserved name, syncs it, and publishes
// it under p by an atomic rename.
//
// Every step is load-bearing: the staging name is unlistable while the write
// is in flight, the content is fsynced before the rename that publishes it,
// a replaced entry's mode and ownership are transplanted onto the
// replacement before that rename, and the parent directory is fsynced after
// it. Without the last step, on ext4 or XFS, a power cut can leave the full
// content sitting under the staging name with nothing pointed at it: syncing
// the file makes the bytes durable, syncing the directory makes the name
// that finds them durable, and those are two different facts.
//
// This is the only function in the package that renames a new file into
// place; every write to share content goes through it.
func (r *ShareRoot) WriteDurable(p SafePath, opt DurableOpts, write func(*File) error) (Durable, error) {
	var done Durable
	if p.IsRoot() {
		return done, fmt.Errorf("durable write: %w", ErrDenied)
	}

	name, err := stagingName()
	if err != nil {
		return done, err
	}
	stagingPath, err := p.Parent().JoinControl(name)
	if err != nil {
		return done, err
	}
	staging := stagingPath.Name()

	// One descriptor for the parent, opened for reading rather than O_PATH:
	// the parent fsync below is EBADF on an O_PATH descriptor. The staging
	// create and the publishing rename both go through this same descriptor,
	// which is what makes the rename atomic: both names are relative to one
	// open directory.
	dir, err := r.openLeaf(p.Parent(), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC)
	if err != nil {
		return done, err
	}
	defer closeAfter(dir, "durable write parent")

	prior, priorName, replacing, err := r.priorEntry(dir, p.Name())
	if err != nil {
		return done, err
	}
	if replacing && opt.NoClobber {
		// A cheap refusal ahead of staging a whole file to find a collision
		// that already existed. RENAME_NOREPLACE below is still the
		// authority against a race landing between this check and the
		// rename.
		return done, fmt.Errorf("durable write: %w", ErrExists)
	}

	f, err := openat2Raw(dir, staging,
		unix.O_CREAT|unix.O_EXCL|unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC,
		uint64(opt.Mode), resolveFlags(r.policy))
	if err != nil {
		return done, mapErrno("create staging file", err)
	}
	handle := &File{f: f}

	published := false
	defer func() {
		closeAfter(f, "staging file")
		if published {
			return
		}
		// No Drop in this language, so cleanup on every failure path is
		// written out here. A removal that itself fails leaves an
		// unlistable orphan for the upload sweep, not a visible one for a
		// user, and failing the caller's already-failed write a second time
		// over that detail tells them nothing new.
		if err := withFdErr(dir, func(fd int) error { return unix.Unlinkat(fd, staging, 0) }); err != nil {
			slog.Warn("vfs: a staging file survived a failed durable write",
				slog.String("name", staging), slog.Any("error", err))
		}
	}()

	// fchmod on the open descriptor, not O_CREAT's own mode argument: that
	// one is filtered through the process umask, and a key or credential
	// file created wider than configured is exactly what this prevents.
	if err := handle.SetMode(opt.Mode); err != nil {
		return done, err
	}
	if !replacing && opt.Owner != nil {
		if err := handle.SetOwner(*opt.Owner); err != nil {
			return done, err
		}
	}

	if err := write(handle); err != nil {
		return done, err
	}
	if err := handle.SyncData(); err != nil {
		return done, err
	}

	dest := normalizeNewName(p.Name())
	if replacing {
		done.Replaced = true
		dest = priorName
		if err := handle.SetMode(prior.Mode & 0o7777); err != nil {
			return done, err
		}
		done.OwnerRestore = handle.SetOwner(Owner{UID: prior.UID, GID: prior.GID})
	}

	flags := uint(0)
	if opt.NoClobber {
		flags = unix.RENAME_NOREPLACE
	}
	if err := renameWithin(dir, staging, dest, flags); err != nil {
		return done, mapErrno("publish", err)
	}
	published = true

	if err := syncDirFd(dir); err != nil {
		return done, err
	}
	return done, nil
}

// PublishPart renames an already-complete, already-synced control file onto
// its destination.
//
// It is WriteDurable's second half without the first: the upload engine may
// have been writing the part file for hours and has already synced it, so
// staging it again would mean copying a whole file beside itself. Everything
// after the content, the mode transplant and the parent fsync, is identical
// to WriteDurable's for the same reason: omitting the mode transplant is what
// silently strips access every other process sharing the directory had a
// moment earlier, and omitting the fsync is what a power cut can turn into a
// complete upload sitting under an unlisted name.
func (r *ShareRoot) PublishPart(part, dest SafePath, replacing bool) (Durable, error) {
	var done Durable
	if part.IsRoot() || dest.IsRoot() {
		return done, fmt.Errorf("publish part: %w", ErrDenied)
	}
	if !part.Parent().Equal(dest.Parent()) {
		// The rename below is atomic only because both names resolve
		// through one open directory descriptor; a caller naming two
		// different directories is refused rather than trusted to have
		// gotten this right.
		return done, fmt.Errorf("publish part across directories: %w", ErrDenied)
	}

	dir, err := r.openLeaf(dest.Parent(), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC)
	if err != nil {
		return done, err
	}
	defer closeAfter(dir, "publish parent")

	prior, priorName, occupied, err := r.priorEntry(dir, dest.Name())
	if err != nil {
		return done, err
	}
	if occupied && !replacing {
		return done, fmt.Errorf("publish part: %w", ErrExists)
	}

	name := normalizeNewName(dest.Name())
	flags := uint(unix.RENAME_NOREPLACE)
	if occupied {
		// The replaced entry's mode and ownership are transplanted before
		// the rename, through a descriptor rather than a second name
		// lookup: a second lookup is a window in which the name could be
		// something else.
		f, oerr := openat2Raw(dir, part.Name(), unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0, resolveFlags(r.policy))
		if oerr != nil {
			return done, mapErrno("reopen part file", oerr)
		}
		handle := &File{f: f}
		merr := handle.SetMode(prior.Mode & 0o7777)
		if merr == nil {
			done.OwnerRestore = handle.SetOwner(Owner{UID: prior.UID, GID: prior.GID})
		}
		closeAfter(f, "part file metadata transplant")
		if merr != nil {
			return done, merr
		}
		done.Replaced = true
		name = priorName
		flags = 0
	}

	if err := renameWithin(dir, part.Name(), name, flags); err != nil {
		return done, mapErrno("publish", err)
	}
	if err := syncDirFd(dir); err != nil {
		return done, err
	}
	return done, nil
}

// priorEntry stats whatever currently occupies leaf under dir, trying every
// Unicode candidate spelling, so a publish lands on the entry being replaced
// rather than beside it under a different normal form.
func (r *ShareRoot) priorEntry(dir *os.File, leaf string) (Stat, string, bool, error) {
	resolve := resolveFlags(r.policy)
	for _, cand := range lookupCandidates(leaf) {
		f, err := openat2Raw(dir, cand, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0, resolve)
		if err != nil {
			if isMissing(err) {
				continue
			}
			return Stat{}, "", false, mapErrno("stat entry being replaced", err)
		}
		st, serr := statOf(f)
		closeAfter(f, "entry being replaced")
		if serr != nil {
			return Stat{}, "", false, serr
		}
		return st, cand, true, nil
	}
	return Stat{}, "", false, nil
}

// renameWithin renames from to to inside one already-open directory
// descriptor, which is what makes the rename atomic: both names resolve
// through the same descriptor.
func renameWithin(dir *os.File, from, to string, flags uint) error {
	return renameBetween(dir, from, dir, to, flags)
}

// syncDirFd fsyncs an already-open directory descriptor, which is what makes
// a name inside it durable, separately from fdatasync making a file's own
// content durable.
func syncDirFd(d *os.File) error {
	if err := withFdErr(d, unix.Fsync); err != nil {
		return mapErrno("sync directory", err)
	}
	return nil
}

// Mkdir creates p with the share's configured directory mode, applied
// verbatim rather than filtered through the process umask, and the
// configured ownership if any.
//
// The mode and ownership are applied through a descriptor reopened by
// Fchmod/Fchown, never by a second name lookup: a second lookup is a window
// in which the just-created name could already be something else. A failure
// to apply either is returned rather than logged and ignored, since the
// premise of this product is that the directory is not this server's own,
// and silently losing the configured ownership is exactly what makes a
// media server or backup script stop seeing files with nothing in this
// process's own request path ever reporting why.
func (r *ShareRoot) Mkdir(p SafePath) error {
	if p.IsRoot() {
		return fmt.Errorf("mkdir: %w", ErrExists)
	}
	parentComps, leaf := splitLeaf(p.Components())
	parent, err := r.resolveDir(parentComps)
	if err != nil {
		return err
	}
	defer closeAfter(parent, "mkdir parent")

	name := normalizeNewName(leaf)
	if mkErr := withFdErr(parent, func(fd int) error { return unix.Mkdirat(fd, name, r.policy.ModeDir) }); mkErr != nil {
		return mapErrno("mkdir", mkErr)
	}

	created, err := openat2Raw(parent, name,
		unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0, resolveFlags(r.policy))
	if err != nil {
		return mapErrno("reopen created directory", err)
	}
	defer closeAfter(created, "created directory")

	if err := withFdErr(created, func(fd int) error { return unix.Fchmod(fd, r.policy.ModeDir) }); err != nil {
		return mapErrno("apply directory mode", err)
	}
	if o := r.policy.Chown; o != nil {
		if err := chownFd(created, *o); err != nil {
			return err
		}
	}
	return nil
}

func chownFd(f *os.File, o Owner) error {
	uid, err := num.Narrow[int](o.UID)
	if err != nil {
		return fmt.Errorf("apply ownership: %w", err)
	}
	gid, err := num.Narrow[int](o.GID)
	if err != nil {
		return fmt.Errorf("apply ownership: %w", err)
	}
	if err := withFdErr(f, func(fd int) error { return unix.Fchown(fd, uid, gid) }); err != nil {
		return mapErrno("apply ownership", err)
	}
	return nil
}

// Rename moves the entry at from to the name to, inside this share.
// noReplace requests RENAME_NOREPLACE; left false, the kernel replaces
// whatever already occupies the destination name.
//
// Crossing a mount boundary inside one share is an expected outcome, not a
// bug: a share is a tree, not necessarily one filesystem, and surfaces as
// ErrCrossDevice for the caller above to fall back to a copy for.
func (r *ShareRoot) Rename(from, to SafePath, noReplace bool) error {
	if from.IsRoot() || to.IsRoot() {
		return fmt.Errorf("rename: %w", ErrDenied)
	}
	fromParentComps, fromLeaf := splitLeaf(from.Components())
	toParentComps, toLeaf := splitLeaf(to.Components())

	fromParent, err := r.resolveDir(fromParentComps)
	if err != nil {
		return err
	}
	defer closeAfter(fromParent, "rename source parent")

	toParent, err := r.resolveDir(toParentComps)
	if err != nil {
		return err
	}
	defer closeAfter(toParent, "rename destination parent")

	dst := destinationSpelling(r, toParent, toLeaf)
	flags := uint(0)
	if noReplace {
		flags = unix.RENAME_NOREPLACE
	}

	var last error
	for _, cand := range lookupCandidates(fromLeaf) {
		err := renameBetween(fromParent, cand, toParent, dst, flags)
		switch {
		case err == nil:
			return nil
		case isMissing(err):
			last = mapErrno("rename", err)
		default:
			return mapErrno("rename", err)
		}
	}
	if last == nil {
		last = fmt.Errorf("rename: %w", ErrNotFound)
	}
	return last
}

// renameBetween renames from one open directory descriptor to another,
// preferring renameat2 and falling back to plain renameat only when flags
// was already empty. Plain renameat cannot honor RENAME_NOREPLACE, so
// falling back with a non-empty flag set would turn a caller's explicit
// "refuse to clobber" into a silent clobber.
func renameBetween(fromDir *os.File, from string, toDir *os.File, to string, flags uint) error {
	err := withFd2Err(fromDir, toDir, func(f, t int) error { return unix.Renameat2(f, from, t, to, flags) })
	if errors.Is(err, unix.ENOSYS) && flags == 0 {
		return withFd2Err(fromDir, toDir, func(f, t int) error { return unix.Renameat(f, from, t, to) })
	}
	return err
}

// destinationSpelling is the name a rename publishes under: the spelling
// already on disk when the destination exists, and NFC otherwise.
//
// Normalizing unconditionally would be a data-duplication bug rather than a
// tidy-up: renaming onto the NFC spelling of a destination another client
// wrote in NFD creates a second file under the requested name and leaves the
// one the caller meant to replace untouched.
func destinationSpelling(r *ShareRoot, parent *os.File, leaf string) string {
	resolve := resolveFlags(r.policy)
	for _, cand := range lookupCandidates(leaf) {
		f, err := openat2Raw(parent, cand, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0, resolve)
		if err == nil {
			closeAfter(f, "destination probe")
			return cand
		}
	}
	return normalizeNewName(leaf)
}

// Unlink removes a non-directory leaf.
func (r *ShareRoot) Unlink(p SafePath) error { return r.unlinkAt(p, 0, "unlink") }

// Rmdir removes an empty directory.
func (r *ShareRoot) Rmdir(p SafePath) error { return r.unlinkAt(p, unix.AT_REMOVEDIR, "rmdir") }

func (r *ShareRoot) unlinkAt(p SafePath, flags int, op string) error {
	if p.IsRoot() {
		return fmt.Errorf("%s: %w", op, ErrDenied)
	}
	parentComps, leaf := splitLeaf(p.Components())
	parent, err := r.resolveDir(parentComps)
	if err != nil {
		return err
	}
	defer closeAfter(parent, op+" parent")

	var last error
	for _, cand := range lookupCandidates(leaf) {
		err := withFdErr(parent, func(fd int) error { return unix.Unlinkat(fd, cand, flags) })
		switch {
		case err == nil:
			return nil
		case isMissing(err):
			last = mapErrno(op, err)
		default:
			return mapErrno(op, err)
		}
	}
	if last == nil {
		last = fmt.Errorf("%s: %w", op, ErrNotFound)
	}
	return last
}

// CopyRange copies n bytes from src to dst, both already open, using
// copy_file_range: a reflink on btrfs and XFS when the ranges align, an
// in-kernel byte copy otherwise, and no userspace round trip in either case.
//
// copy_file_range is documented to perform a short copy even on success, so
// this loops until n bytes moved or the source is exhausted. EXDEV (crossing
// a mount inside one share, ordinary since a share is a tree and not one
// filesystem), EOPNOTSUPP, and ENOSYS fall back to a bounded userspace copy;
// any other errno is a real failure and is returned.
//
// Offsets are always explicit, never nil: this package reads and writes
// positionally throughout, and letting either syscall advance a descriptor's
// own cursor would move it under whatever the next caller of the same handle
// expects.
func CopyRange(src *File, srcOff uint64, dst *File, dstOff uint64, n uint64) (uint64, error) {
	if n == 0 {
		return 0, nil
	}
	var copied uint64
	for copied < n {
		remaining := n - copied
		want, werr := num.Narrow[int](remaining)
		if werr != nil {
			want = int(^uint(0) >> 1)
		}
		in, err := int64Offset(srcOff + copied)
		if err != nil {
			return copied, err
		}
		out, err := int64Offset(dstOff + copied)
		if err != nil {
			return copied, err
		}

		got, cerr := withFd2(src.f, dst.f, func(s, d int) (int, error) {
			return unix.CopyFileRange(s, &in, d, &out, want, 0)
		})
		switch {
		case cerr == nil && got == 0:
			return copied, nil
		case cerr == nil:
			n64, nerr := num.Narrow[uint64](got)
			if nerr != nil {
				return copied, fmt.Errorf("copy range: %w", nerr)
			}
			copied += n64
		case errors.Is(cerr, unix.EXDEV), errors.Is(cerr, unix.EOPNOTSUPP), errors.Is(cerr, unix.ENOSYS):
			rest, berr := bufferedCopyRange(src, srcOff+copied, dst, dstOff+copied, n-copied)
			return copied + rest, berr
		default:
			return copied, mapErrno("copy range", cerr)
		}
	}
	return copied, nil
}

func int64Offset(v uint64) (int64, error) {
	off, err := num.Narrow[int64](v)
	if err != nil {
		return 0, fmt.Errorf("copy range offset: %w", err)
	}
	return off, nil
}

// copyBufBytes bounds the userspace fallback copy. No amount of content the
// caller asks for is ever fully in memory, whatever the copy size.
const copyBufBytes = 256 << 10

// bufferedCopyRange is the single userspace copy loop in the package: the
// bounded-memory guarantee only has to be argued for once, here.
func bufferedCopyRange(src *File, srcOff uint64, dst *File, dstOff uint64, n uint64) (uint64, error) {
	buf := make([]byte, copyBufBytes)
	var copied uint64
	for copied < n {
		want := uint64(copyBufBytes)
		if remaining := n - copied; remaining < want {
			want = remaining
		}
		rOff, err := int64Offset(srcOff + copied)
		if err != nil {
			return copied, err
		}
		got, err := src.ReadAt(buf[:want], rOff)
		if got > 0 {
			wOff, oerr := int64Offset(dstOff + copied)
			if oerr != nil {
				return copied, oerr
			}
			if _, werr := dst.WriteAt(buf[:got], wOff); werr != nil {
				return copied, werr
			}
			n64, nerr := num.Narrow[uint64](got)
			if nerr != nil {
				return copied, fmt.Errorf("copy range: %w", nerr)
			}
			copied += n64
		}
		if err != nil {
			if isEOF(err) {
				return copied, nil
			}
			return copied, err
		}
		if got == 0 {
			return copied, nil
		}
	}
	return copied, nil
}

// isEOF folds the two ways a short ReadAt reports itself: (*os.File).ReadAt
// returns io.EOF when it could not fill the slice, which is a short copy
// here, not a failure.
func isEOF(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}
