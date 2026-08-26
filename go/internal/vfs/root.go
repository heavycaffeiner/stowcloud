//go:build linux

package vfs

import (
	"errors"
	"fmt"
	"math"
	"os"
	"sync"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/internal/num"
	"golang.org/x/sys/unix"
)

// statMask is what every stat in this package asks for. BTIME is the reason it
// is statx and not fstat.
const statMask = unix.STATX_BASIC_STATS | unix.STATX_BTIME

// ShareRoot is one configured share, held open as an O_PATH directory
// descriptor for the process lifetime. Every resolution starts here, and
// nothing in this package accepts a path relative to the process working
// directory.
type ShareRoot struct {
	ID ShareID

	anchor *os.File
	policy SharePolicy
	dev    uint64
	// ino is the anchor's inode, recorded so a later resolution of the same
	// path can be compared against what was opened. Without it an unmounted
	// share is undetectable: the descriptor keeps the filesystem alive, so
	// every question asked of the handle answers as if nothing happened.
	ino      uint64
	fsType   FsType
	hasBtime bool

	// admitted caches the verdict per device, so a mount below the root is
	// classified once rather than on every resolution.
	mu       sync.Mutex
	admitted map[uint64]struct{}
}

// OpenShareRoot opens host as an anchor and records the device and filesystem
// type for the cross-mount and reflink decisions.
//
// It does not validate that host is a sensible share. Refusing a filesystem the
// design cannot support is the config layer's boundary, and FsType carries the
// facts that boundary decides from.
func OpenShareRoot(id ShareID, host string, policy SharePolicy) (*ShareRoot, error) {
	fd, err := unix.Open(host, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, mapErrno("open share root "+host, err)
	}
	anchor := os.NewFile(uintptr(fd), host)

	var stx unix.Statx_t
	if err := withFdErr(anchor, func(afd int) error {
		return unix.Statx(afd, "", unix.AT_EMPTY_PATH, statMask, &stx)
	}); err != nil {
		closeAfter(anchor, "share root anchor")
		return nil, mapErrno("stat share root", err)
	}

	var sfs unix.Statfs_t
	fsType := FsType(0)
	// fstatfs is documented to work on an O_PATH descriptor for the purpose of
	// identifying the filesystem. A kernel or filesystem that disagrees leaves
	// the type unknown rather than failing registration, because the type only
	// decides which gates apply.
	if err := withFdErr(anchor, func(afd int) error { return unix.Fstatfs(afd, &sfs) }); err == nil {
		magic, nerr := num.Narrow[uint64](sfs.Type)
		if nerr == nil {
			fsType = FsType(magic)
		}
	}

	return &ShareRoot{
		ID:       id,
		anchor:   anchor,
		policy:   policy,
		dev:      unix.Mkdev(stx.Dev_major, stx.Dev_minor),
		ino:      stx.Ino,
		fsType:   fsType,
		hasBtime: stx.Mask&unix.STATX_BTIME != 0,
		admitted: map[uint64]struct{}{},
	}, nil
}

// RegisterShareRoot opens host and admits it, or refuses.
//
// This is the entry point a share registration uses. The refusal happens here,
// at registration, rather than at the first operation that cannot hold its
// contract: a deployment that accepts anything and degrades quietly looks
// healthy and loses file identity months later, once the sync clients have
// already written the wrong thing into their journals.
func RegisterShareRoot(id ShareID, host string, policy SharePolicy) (*ShareRoot, Admission, error) {
	r, err := OpenShareRoot(id, host, policy)
	if err != nil {
		return nil, Admission{}, err
	}
	adm, err := AdmitMount(host, r.fsType, r.hasBtime)
	if err != nil {
		closeAfter(r.anchor, "refused share root")
		return nil, Admission{}, err
	}
	r.admitted[r.dev] = struct{}{}
	return r, adm, nil
}

// admitDevice checks a mount reached below the share root, once per device.
//
// A supported root does not bless an unsupported mount below it. Without this,
// putting a network or user-space filesystem under an admitted root is a
// trivial way around the whole gate.
//
// The result is cached per device because the alternative is a filesystem call
// on every resolution, and a mount's type does not change while it is mounted.
func (r *ShareRoot) admitDevice(dir *os.File, dev uint64, path string) error {
	r.mu.Lock()
	_, seen := r.admitted[dev]
	r.mu.Unlock()
	if seen {
		return nil
	}

	var sfs unix.Statfs_t
	if err := withFdErr(dir, func(fd int) error { return unix.Fstatfs(fd, &sfs) }); err != nil {
		return mapErrno("statfs "+path, err)
	}
	magic, nerr := num.Narrow[uint64](sfs.Type)
	if nerr != nil {
		// A magic value that does not fit is one this build cannot name, which
		// is the fail-closed case rather than a reason to admit it.
		return &AdmissionError{Path: path, Reason: "the filesystem type could not be read"}
	}

	var stx unix.Statx_t
	if err := withFdErr(dir, func(fd int) error {
		return unix.Statx(fd, "", unix.AT_EMPTY_PATH, statMask, &stx)
	}); err != nil {
		return mapErrno("statx "+path, err)
	}

	if _, err := AdmitMount(path, FsType(magic), stx.Mask&unix.STATX_BTIME != 0); err != nil {
		return err
	}
	r.mu.Lock()
	r.admitted[dev] = struct{}{}
	r.mu.Unlock()
	return nil
}

// Close releases the anchor. A ShareRoot normally lives for the process, so
// this exists for the share being deregistered and for a test.
func (r *ShareRoot) Close() error { return r.anchor.Close() }

func (r *ShareRoot) Policy() SharePolicy { return r.policy }

// Alive reports whether the configured path still names the directory this
// root has open.
//
// It asks about the path rather than about the descriptor, and that is the
// whole of why it works. A descriptor outlives the directory it names: an
// unmounted share root leaves a handle that still stats, still reports the
// same device, and is otherwise indistinguishable from a working one, because
// holding it is what keeps the filesystem alive. Nothing about the handle can
// tell you the mount is gone.
//
// What can is a fresh resolution of the same path: after an unmount it lands
// on the underlying directory, which is a different inode, or on nothing at
// all. So the comparison is device and inode against what registration
// recorded, and a mismatch is the mount having been swapped underneath.
//
// This is the one path resolution in this package that does not go through the
// anchor, and it is not a security decision: it decides whether to mark a
// share broken, never what a request may reach. Every operation still resolves
// through the anchor under openat2, so a path swapped between this probe and
// the next request changes nothing about what is reachable.
func (r *ShareRoot) Alive() error {
	var stx unix.Statx_t
	if err := unix.Statx(unix.AT_FDCWD, r.anchor.Name(), 0,
		unix.STATX_TYPE|unix.STATX_INO, &stx); err != nil {
		return mapErrno("probe share root", err)
	}
	// A path that resolves and is no longer a directory is a share root
	// something replaced with a file, which is not a share this can serve.
	if stx.Mode&unix.S_IFMT != unix.S_IFDIR {
		return fmt.Errorf("probe share root: %w", ErrNotADirectory)
	}
	if unix.Mkdev(stx.Dev_major, stx.Dev_minor) != r.dev || stx.Ino != r.ino {
		// The path is there and it is not what this root has open. The share
		// is serving a tree nobody can reach by its own name any more, which
		// is worse than serving nothing.
		return fmt.Errorf("probe share root: %w", ErrNotFound)
	}
	return nil
}

// Dev is the device the share root itself sits on. A subdirectory may be on
// another one, which is ordinary rather than a fault.
func (r *ShareRoot) Dev() uint64 { return r.dev }

func (r *ShareRoot) FsType() FsType { return r.fsType }

// Stat resolves p under the share's policy and stats the leaf.
//
// No O_NOFOLLOW here, and that is the policy doing its job rather than an
// omission: under the default the resolve flags refuse a symlink outright, and
// under a policy that says to follow one, following it is what stat means.
func (r *ShareRoot) Stat(p SafePath) (Stat, error) {
	f, err := r.openLeaf(p, unix.O_PATH|unix.O_CLOEXEC)
	if err != nil {
		return Stat{}, err
	}
	defer closeAfter(f, "stat handle")
	return statOf(f)
}

// statOf stats an already-open descriptor. O_PATH is enough for statx, which is
// why Stat never has to open the leaf for reading.
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
	// Both of these are read off the returned mask rather than assumed, even
	// ctime, which is part of the basic set: an absent timestamp and a zero one
	// are different facts.
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

// timestampNs saturates rather than wrapping. The seconds field comes off a
// filesystem another program wrote, so a value wide enough to overflow the
// multiply is untrusted input rather than an impossibility.
func timestampNs(sec int64, nsec uint32) int64 {
	const perSecond = int64(time.Second)
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
	}
	return KindOther
}

// OpenRead opens p for reading. intent decides the access mode; only the upload
// finalizer may pass IntentReadWrite, and a gate greps for a second site.
//
// There is no fallback chain here and that is the point. An O_RDONLY open does
// not fail with EACCES on a readable file, so nothing has to try O_RDWR first
// and fall back, which is what made every plain read of a mode-644 file hold a
// handle that could write.
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

// CreatePart creates a control file under the reserved prefix and returns it
// open for reading and writing.
//
// It is the upload engine's part file and nothing else. The handle is O_RDWR
// by construction rather than through the access intent, for the same reason
// the durable-write helper's staging file is: a file this server just created
// is writable because it made it, not because a caller asked for privilege on
// a path that already existed.
//
// O_EXCL, so a collision is the kernel's refusal rather than a clobber, and
// the mode is applied with fchmod because O_CREAT filters it through umask.
// The name has to come from JoinControl, which is the only call permitted to
// produce the reserved prefix; a name without it is refused here rather than
// quietly creating something a listing would show.
func (r *ShareRoot) CreatePart(p SafePath) (*File, error) {
	if p.IsRoot() {
		return nil, fmt.Errorf("create a part file: %w", ErrDenied)
	}
	if !IsReservedName(p.Name()) {
		return nil, &NameError{Component: p.Name(),
			Reason: "a part file has to carry a control prefix or a listing would show it",
			Err:    ErrInvalidName}
	}
	parentComps, leaf := splitLeaf(p.comps)
	parent, err := r.resolveDir(parentComps)
	if err != nil {
		return nil, err
	}
	defer closeAfter(parent, "part file parent")

	f, err := openat2(parent, leaf,
		unix.O_CREAT|unix.O_EXCL|unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC,
		uint64(r.policy.ModeFile), resolveFlags(r.policy))
	if err != nil {
		return nil, mapErrno("create a part file", err)
	}
	handle := &File{f: f}

	// The exact configured mode regardless of umask, and the result is
	// checked: the published file inherits this one when there is nothing at
	// the destination to transplant from.
	if cerr := handle.SetMode(r.policy.ModeFile); cerr != nil {
		return nil, errors.Join(cerr, handle.Close())
	}
	if o := r.policy.Chown; o != nil {
		if cerr := handle.SetOwner(*o); cerr != nil {
			return nil, errors.Join(cerr, handle.Close())
		}
	}
	return handle, nil
}

// Mkdir creates p with the share's configured directory mode, applied verbatim
// rather than through umask, and applies the configured ownership.
//
// A failure to apply either is returned, not logged. The premise of this
// product is that the folder is not ours: a directory whose configured
// ownership was not applied is exactly the failure that makes a media server or
// a backup script stop seeing files, and it produces no error, no log line and
// no failed request unless this returns one.
func (r *ShareRoot) Mkdir(p SafePath) error {
	if p.IsRoot() {
		return fmt.Errorf("mkdir: %w", ErrExists)
	}
	parentComps, leaf := splitLeaf(p.comps)
	parent, err := r.resolveDir(parentComps)
	if err != nil {
		return err
	}
	defer closeAfter(parent, "mkdir parent")

	name := normalizeNewName(leaf)
	if merr := withFdErr(parent, func(fd int) error {
		return unix.Mkdirat(fd, name, r.policy.ModeDir)
	}); merr != nil {
		return mapErrno("mkdir", merr)
	}

	// The mode and ownership are applied through a descriptor rather than by
	// name: reopening the name would be a second lookup, and a second lookup is
	// a window in which the name is something else.
	created, err := openat2(parent, name,
		unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0, resolveFlags(r.policy))
	if err != nil {
		return mapErrno("reopen the created directory", err)
	}
	defer closeAfter(created, "created directory")

	if cerr := withFdErr(created, func(fd int) error {
		return unix.Fchmod(fd, r.policy.ModeDir)
	}); cerr != nil {
		return mapErrno("apply the directory mode", cerr)
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

// Rename moves from to to within this share. noReplace maps to
// RENAME_NOREPLACE; without it the destination is replaced atomically.
//
// Crossing a mount boundary inside one share is an expected outcome rather than
// a bug, and surfaces as ErrCrossDevice for the caller to fall back on.
func (r *ShareRoot) Rename(from, to SafePath, noReplace bool) error {
	if from.IsRoot() || to.IsRoot() {
		return fmt.Errorf("rename: %w", ErrDenied)
	}
	fromParentComps, fromLeaf := splitLeaf(from.comps)
	toParentComps, toLeaf := splitLeaf(to.comps)

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

	dst := r.destinationSpelling(toParent, toLeaf)
	flags := uint(0)
	if noReplace {
		flags = unix.RENAME_NOREPLACE
	}

	var last error
	for _, cand := range lookupCandidates(fromLeaf) {
		err := renameat(fromParent, cand, toParent, dst, flags)
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

// renameat prefers renameat2 and falls back to renameat on ENOSYS only when the
// flag set was empty anyway, because the fallback cannot deliver
// RENAME_NOREPLACE and silently dropping it would turn a refusal to clobber
// into a clobber.
func renameat(fromDir *os.File, from string, toDir *os.File, to string, flags uint) error {
	err := withFd2Err(fromDir, toDir, func(f, t int) error {
		return unix.Renameat2(f, from, t, to, flags)
	})
	if errors.Is(err, unix.ENOSYS) && flags == 0 {
		return withFd2Err(fromDir, toDir, func(f, t int) error {
			return unix.Renameat(f, from, t, to)
		})
	}
	return err
}

// destinationSpelling is the name to publish under: the spelling already on
// disk when the destination exists, and NFC when it does not.
//
// Normalising unconditionally is a data-duplication bug rather than a tidy-up.
// A destination another client wrote in NFD, renamed onto its NFC spelling,
// becomes a second file with the same user-visible name, and the one the caller
// meant to replace is still there.
func (r *ShareRoot) destinationSpelling(parent *os.File, leaf string) string {
	resolve := resolveFlags(r.policy)
	for _, cand := range lookupCandidates(leaf) {
		f, err := openat2(parent, cand, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0, resolve)
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
	parentComps, leaf := splitLeaf(p.comps)
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

// SetTimes sets the modification time and leaves the access time alone. A
// symlink is never followed here: the timestamp belongs to the entry named,
// not to whatever it points at.
func (r *ShareRoot) SetTimes(p SafePath, mtimeNs int64) error {
	if p.IsRoot() {
		return fmt.Errorf("set times: %w", ErrDenied)
	}
	parentComps, leaf := splitLeaf(p.comps)
	parent, err := r.resolveDir(parentComps)
	if err != nil {
		return err
	}
	defer closeAfter(parent, "set times parent")

	const perSecond = int64(time.Second)
	sec := mtimeNs / perSecond
	nsec := mtimeNs % perSecond
	if nsec < 0 {
		sec--
		nsec += perSecond
	}
	ts := []unix.Timespec{
		{Sec: 0, Nsec: unix.UTIME_OMIT},
		{Sec: sec, Nsec: nsec},
	}

	var last error
	for _, cand := range lookupCandidates(leaf) {
		err := withFdErr(parent, func(fd int) error {
			return unix.UtimesNanoAt(fd, cand, ts, unix.AT_SYMLINK_NOFOLLOW)
		})
		switch {
		case err == nil:
			return nil
		case isMissing(err):
			last = mapErrno("set times", err)
		default:
			return mapErrno("set times", err)
		}
	}
	if last == nil {
		last = fmt.Errorf("set times: %w", ErrNotFound)
	}
	return last
}

// SyncDir makes the entries of the directory at p durable, which is a separate
// act from making a file's contents durable.
//
// The descriptor is opened for reading rather than O_PATH. fsync on an O_PATH
// descriptor is EBADF, which is not a durability failure anyone would ever hit
// in the field and is instead an unconditional one: it broke every write on the
// only platform that ships.
func (r *ShareRoot) SyncDir(p SafePath) error {
	d, err := r.openLeaf(p, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC)
	if err != nil {
		return err
	}
	defer closeAfter(d, "directory being synced")
	return syncDirFd(d)
}

// syncDirFd is a full fsync rather than fdatasync: a directory's data is its
// metadata.
func syncDirFd(d *os.File) error {
	if err := withFdErr(d, unix.Fsync); err != nil {
		return mapErrno("sync directory", err)
	}
	return nil
}

// Space reports the filesystem behind p, which is not always the one behind the
// share root: a RAID array mounted under media/ is a different device with
// different numbers, and answering from the anchor gives every such directory
// the root disk's.
func (r *ShareRoot) Space(p SafePath) (FsSpace, error) {
	dir, err := r.resolveDirOrParent(p)
	if err != nil {
		return FsSpace{}, err
	}
	defer closeAfter(dir, "space probe")
	return spaceOf(dir)
}

// DirDev is the device the directory at p sits on, for the same reason Space
// exists: a volume mounted under media/ is a different device from the share
// root, and a rename cannot cross that boundary. A caller choosing between a
// rename and a copy asks about the directory an entry lands in, not the root.
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

// resolveDirOrParent resolves p as a directory, falling back to its parent.
//
// Both ENOENT and ENOTDIR arrive as ErrNotFound, and the second is the ordinary
// case of p naming a file. The parent holds it and is therefore on the same
// filesystem, which is the only fact either probe above is after.
func (r *ShareRoot) resolveDirOrParent(p SafePath) (*os.File, error) {
	dir, err := r.resolveDir(p.comps)
	if err == nil {
		return dir, nil
	}
	if !p.IsRoot() && errors.Is(err, ErrNotFound) {
		return r.resolveDir(p.Parent().comps)
	}
	return nil, err
}

// spaceOf runs the accounting against an already-open descriptor, so the path
// probe and the open-handle one cannot disagree about what a block size means.
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
