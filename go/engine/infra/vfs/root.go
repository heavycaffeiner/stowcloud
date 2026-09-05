//go:build linux

package vfs

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"sync"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
	"golang.org/x/sys/unix"
)

// rootStatMask asks statx for the fields registration needs: device, inode
// and whether the filesystem carries a birth time at all.
const rootStatMask = unix.STATX_BASIC_STATS | unix.STATX_BTIME

// ShareRoot is one configured share, held open as a single O_PATH directory
// descriptor for the life of the process or until the share is
// deregistered. Every resolution in this package starts from this
// descriptor; no method here or in a sibling file accepts a path relative
// to the process working directory or opens anything by host path once a
// root is registered.
type ShareRoot struct {
	id ShareID

	anchor   *os.File
	policy   SharePolicy
	dev      uint64
	ino      uint64
	fsType   FsType
	hasBtime bool

	// scratch marks a root that is server-owned space rather than a share.
	// Nothing resolves a request path in one, and its id means nothing, so
	// the flag is what a caller asks instead of comparing against an id
	// value that would have to be reserved.
	scratch bool

	// admitted caches the admission verdict per device, so a mount reached
	// below the root pays for classification once rather than on every
	// resolution that crosses it.
	mu       sync.Mutex
	admitted map[uint64]struct{}
}

// withFd runs fn against f's raw descriptor and keeps f reachable for the
// whole call.
//
// (*os.File).Fd takes the descriptor out of the Go runtime's view for the
// duration of the call it is used in, so nothing keeps the owning File
// alive on its own; a finalizer is free to close the descriptor while fn is
// still running. Every raw-descriptor use in this package goes through this
// helper or withFd2, never Fd called directly, so KeepAlive always covers
// the syscall.
func withFd[T any](f *os.File, fn func(fd int) (T, error)) (T, error) {
	v, err := fn(int(f.Fd()))
	runtime.KeepAlive(f)
	return v, err
}

// withFd2 is withFd for a call that needs two descriptors at once, such as
// a rename between two open directories.
func withFd2[T any](a, b *os.File, fn func(x, y int) (T, error)) (T, error) {
	v, err := fn(int(a.Fd()), int(b.Fd()))
	runtime.KeepAlive(a)
	runtime.KeepAlive(b)
	return v, err
}

// withFdErr is withFd for a call with no value to report besides an error.
func withFdErr(f *os.File, fn func(fd int) error) error {
	_, err := withFd(f, func(fd int) (struct{}, error) { return struct{}{}, fn(fd) })
	return err
}

// withFd2Err is withFd2 for a call with no value to report besides an
// error.
func withFd2Err(a, b *os.File, fn func(x, y int) error) error {
	_, err := withFd2(a, b, func(x, y int) (struct{}, error) { return struct{}{}, fn(x, y) })
	return err
}

// closeFailed closes f and reports the outcome, for a path that is already
// returning some other error and has nowhere else to put a second one
// besides joining it on.
func closeFailed(f *os.File) error {
	if err := f.Close(); err != nil {
		return fmt.Errorf("close share root anchor: %w", err)
	}
	return nil
}

// OpenShareRoot opens host as an O_PATH anchor and records the facts
// admission and later resolution need: device, inode, filesystem type, and
// whether this instance reports a birth time.
//
// It performs no admission of its own. A caller that wants the refusal
// calls RegisterShareRoot; this constructor exists separately for a test
// or a tool that needs to inspect a root's raw facts before deciding
// whether to admit it.
func OpenShareRoot(id ShareID, host string, policy SharePolicy) (*ShareRoot, error) {
	fd, err := unix.Open(host, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, mapErrno("open share root "+host, err)
	}
	anchor := os.NewFile(uintptr(fd), host)

	var stx unix.Statx_t
	if err := withFdErr(anchor, func(afd int) error {
		return unix.Statx(afd, "", unix.AT_EMPTY_PATH, rootStatMask, &stx)
	}); err != nil {
		return nil, errors.Join(mapErrno("stat share root", err), closeFailed(anchor))
	}

	var sfs unix.Statfs_t
	fsType := FsType(0)
	// Fstatfs is documented to answer for an O_PATH descriptor. A kernel
	// that disagrees leaves the type at zero rather than failing
	// registration outright, since the type only decides which gate
	// applies next, and that gate itself rejects an unrecognized value.
	if err := withFdErr(anchor, func(afd int) error { return unix.Fstatfs(afd, &sfs) }); err == nil {
		if magic, nerr := num.Narrow[uint64](sfs.Type); nerr == nil {
			fsType = FsType(magic)
		}
	}

	return &ShareRoot{
		id:       id,
		anchor:   anchor,
		policy:   policy,
		dev:      unix.Mkdev(stx.Dev_major, stx.Dev_minor),
		ino:      stx.Ino,
		fsType:   fsType,
		hasBtime: stx.Mask&unix.STATX_BTIME != 0,
		admitted: map[uint64]struct{}{},
	}, nil
}

// RegisterShareRoot opens host and admits it, or refuses and leaves nothing
// registered.
//
// Refusing here, at registration, is deliberate: a deployment that accepts
// an unsupported filesystem and degrades quietly looks healthy right up
// until a sync client's own journal disagrees with what this server can
// actually promise, months after the fact. A refused admission closes the
// anchor before returning, so nothing is left half open.
func RegisterShareRoot(id ShareID, host string, policy SharePolicy) (*ShareRoot, Admission, error) {
	r, err := OpenShareRoot(id, host, policy)
	if err != nil {
		return nil, Admission{}, err
	}
	adm, err := AdmitMount(host, r.fsType, r.hasBtime)
	if err != nil {
		return nil, Admission{}, errors.Join(err, closeFailed(r.anchor))
	}
	if rerr := proveReadable(host); rerr != nil {
		return nil, Admission{}, errors.Join(classifyUnreadable(rerr), closeFailed(r.anchor))
	}
	r.admitted[r.dev] = struct{}{}
	return r, adm, nil
}

// ErrSandboxDenied names a proveReadable failure that follows a successful
// OpenShareRoot: the anchor's O_PATH open is a reference the domain permits
// without granting any access right at all, so reaching this point already
// proved the directory resolvable, and only the real read-open was
// refused. It wraps ErrDenied, so a caller matching only the general
// sentinel still finds it; RejectionKind (core/registry.go) reports it
// under its own token so the message names the sandbox instead of
// repeating "permission denied" on a directory nothing is wrong with.
var ErrSandboxDenied = errors.New("vfs: the sandbox does not grant this path")

// classifyUnreadable tells a sandbox refusal apart from a directory that is
// genuinely unreadable, using only what OpenShareRoot and proveReadable
// already produced: no shelling out, no reading /proc. A permission denial
// reaching this point followed a successful O_PATH open, which is the
// asymmetry a domain that admits resolution but not reading produces; an
// error this is not, such as the directory having vanished in the interval,
// passes through unchanged.
func classifyUnreadable(err error) error {
	if errors.Is(err, ErrDenied) {
		return fmt.Errorf("%w: %w", ErrSandboxDenied, err)
	}
	return err
}

// proveReadable opens the root the way a listing will.
//
// The anchor above is O_PATH, which asks the kernel for a reference rather
// than for read access, and a sandbox that will refuse every later read
// grants it. Registration then succeeded and every listing answered
// not-found, with nothing anywhere saying why: the folder was in the
// listing, its properties were readable, and opening it did nothing.
//
// Doing the real open here turns that into a refusal at the moment somebody
// registers the folder, which is when they can still act on it.
func proveReadable(host string) error {
	fd, err := unix.Open(host, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return mapErrno("read share root "+host, err)
	}
	return closeFailed(os.NewFile(uintptr(fd), host))
}

// OpenScratchRoot opens a directory that is not a share: server-owned
// scratch space such as the upload spool.
//
// It exists so that space does not have to borrow a share id. The code this
// replaces opened the spool as share zero with a comment admitting the id
// was a lie, which put a non-share into the share domain: every accessor
// keyed by id could reach it, and every reader of an id had to know that one
// value meant "not really".
//
// The handle type is the same, so every safe-path method works unchanged, and
// the admission gate is the same, because scratch space on a filesystem this
// build cannot hold its contracts on is the same problem there as anywhere.
// What is absent is the id and the share semantics that come with it.
func OpenScratchRoot(dir string, policy SharePolicy) (*ShareRoot, error) {
	r, _, err := RegisterShareRoot(0, dir, policy)
	if err != nil {
		return nil, err
	}
	r.scratch = true
	return r, nil
}

// IsScratch reports whether this root is server-owned scratch space rather
// than a registered share.
func (r *ShareRoot) IsScratch() bool { return r.scratch }

// admitDevice classifies a mount reached below the share root, the first
// time a resolution lands on a device this root has not seen, and caches
// the verdict so later resolutions onto the same device do not pay for it
// again.
//
// A supported share root does not bless whatever is mounted underneath it:
// without this check, putting a network share or a FUSE mount under an
// admitted root would be an easy way around the whole allow-list.
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
		// A magic value this build cannot even represent is unclassifiable,
		// which is the fail-closed outcome and not a reason to admit it.
		return &AdmissionError{Path: path, Reason: "the filesystem type could not be read"}
	}

	var stx unix.Statx_t
	if err := withFdErr(dir, func(fd int) error {
		return unix.Statx(fd, "", unix.AT_EMPTY_PATH, rootStatMask, &stx)
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

// Close releases the anchor descriptor.
//
// A ShareRoot ordinarily lives for the process, so this exists for share
// deregistration and for tests. There is no reference counting: a *File
// opened through this root before Close keeps working, since the kernel
// keeps an open file alive independent of the directory descriptor used to
// reach it, but no new resolution through this root is possible afterward.
func (r *ShareRoot) Close() error { return r.anchor.Close() }

// ID is the share this root serves.
func (r *ShareRoot) ID() ShareID { return r.id }

func (r *ShareRoot) Policy() SharePolicy { return r.policy }

// Dev is the device the share root itself sits on. A subdirectory can sit
// on a different one once a nested mount is involved, which is ordinary
// rather than a fault.
func (r *ShareRoot) Dev() uint64 { return r.dev }

func (r *ShareRoot) FsType() FsType { return r.fsType }

// HasBtime reports whether the share root's own filesystem instance
// reported a birth time at registration.
func (r *ShareRoot) HasBtime() bool { return r.hasBtime }

// Alive reports whether the host path this root was configured with still
// names the directory this root holds open.
//
// This exists because a descriptor outlives the directory it names: an
// unmounted share leaves a handle that still stats successfully, still
// reports the original device and inode, and is otherwise indistinguishable
// from a healthy root, precisely because holding the descriptor keeps the
// underlying filesystem instance alive. Nothing about the handle itself can
// reveal that the mount underneath it is gone.
//
// The check therefore resolves the configured path afresh, via Statx
// against the path string rather than the anchor descriptor, and compares
// device and inode against what registration recorded:
//
//   - not a directory now: ErrNotADirectory.
//   - a directory whose (dev, ino) differs: ErrNotFound, since something
//     else now occupies the name and the original tree is unreachable by
//     it.
//   - unresolvable at all: the mapped errno, typically ErrNotFound.
//   - a match: nil.
//
// This is explicitly not a security decision. It decides whether to mark a
// share broken for health reporting, and it never decides what a request
// may reach: every operation still resolves exclusively through the anchor
// under openat2, so a path swapped between one Alive call and the next
// request changes nothing about what that request can touch.
func (r *ShareRoot) Alive() error {
	var stx unix.Statx_t
	err := unix.Statx(unix.AT_FDCWD, r.anchor.Name(), 0, unix.STATX_TYPE|unix.STATX_INO, &stx)
	if err != nil {
		return mapErrno("probe share root", err)
	}
	if stx.Mode&unix.S_IFMT != unix.S_IFDIR {
		return fmt.Errorf("probe share root: %w", ErrNotADirectory)
	}
	if unix.Mkdev(stx.Dev_major, stx.Dev_minor) != r.dev || stx.Ino != r.ino {
		return fmt.Errorf("probe share root: %w", ErrNotFound)
	}
	return nil
}
