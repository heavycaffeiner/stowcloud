//go:build linux

package vfs

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime"

	"golang.org/x/sys/unix"
)

// withFd runs fn with f's descriptor and keeps f reachable until fn returns.
//
// (*os.File).Fd takes the descriptor out of the runtime's view for the duration
// of the call, so nothing keeps the owning file alive and a finalizer is free
// to close it underneath the syscall. Every raw descriptor in this package goes
// through here or through withFd2 for that reason, and the gate refuses a third
// site that does it by hand.
//
// Fd also puts the file into blocking mode, which is right here and would not
// be for a socket: everything this package opens is a regular file or a
// directory.
func withFd[T any](f *os.File, fn func(fd int) (T, error)) (T, error) {
	v, err := fn(int(f.Fd()))
	runtime.KeepAlive(f)
	return v, err
}

func withFd2[T any](a, b *os.File, fn func(x, y int) (T, error)) (T, error) {
	v, err := fn(int(a.Fd()), int(b.Fd()))
	runtime.KeepAlive(a)
	runtime.KeepAlive(b)
	return v, err
}

func withFdErr(f *os.File, fn func(fd int) error) error {
	_, err := withFd(f, func(fd int) (struct{}, error) { return struct{}{}, fn(fd) })
	return err
}

func withFd2Err(a, b *os.File, fn func(x, y int) error) error {
	_, err := withFd2(a, b, func(x, y int) (struct{}, error) { return struct{}{}, fn(x, y) })
	return err
}

// closeAfter is the deferred close every descriptor here gets. There is no Drop
// in this language, so a descriptor's lifetime is the author's problem on every
// path including the error paths; a close that fails on data already synced is
// worth a line and not a failed operation.
func closeAfter(c io.Closer, what string) {
	if err := c.Close(); err != nil {
		slog.Warn("closing a descriptor failed",
			slog.String("what", what), slog.Any("error", err))
	}
}

// resolveFlags is the whole security posture in five lines.
//
// RESOLVE_NO_MAGICLINKS is unconditional, because it is what blocks escape
// through /proc/self/fd/*. RESOLVE_NO_XDEV is added unless the share opts into
// crossing a mount boundary.
func resolveFlags(p SharePolicy) uint64 {
	f := uint64(unix.RESOLVE_NO_MAGICLINKS)
	switch p.Symlink {
	case SymlinkDeny:
		f |= unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS
	case SymlinkWithinShare:
		f |= unix.RESOLVE_IN_ROOT
	case SymlinkFollow:
		f |= unix.RESOLVE_BENEATH
	}
	if !p.CrossMount {
		f |= unix.RESOLVE_NO_XDEV
	}
	return f
}

// openat2 is the one call that opens anything in this package. name is carried
// onto the *os.File only so a leaked descriptor names itself in a stack.
func openat2(dir *os.File, path string, flags uint64, mode uint64, resolve uint64) (*os.File, error) {
	how := unix.OpenHow{Flags: flags, Mode: mode, Resolve: resolve}
	fd, err := withFd(dir, func(dfd int) (int, error) { return unix.Openat2(dfd, path, &how) })
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}

// resolveDir resolves comps to an O_PATH directory descriptor in a single
// openat2 call, so the kernel enforces the resolve flags across every
// intermediate component atomically.
//
// One call is the whole point. There is no normalise, then check, then open:
// between a check and an open a component can become a symlink, and there is no
// window here because there is no second step.
func (r *ShareRoot) resolveDir(comps []string) (*os.File, error) {
	resolve := resolveFlags(r.policy)
	var last error
	for _, cand := range pathCandidates(comps) {
		f, err := openat2(r.anchor, cand,
			unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0, resolve)
		if err == nil {
			// A share whose policy crosses mount boundaries can reach a
			// filesystem the share root's own verdict says nothing about, so
			// the mount is classified before anything on it is exposed.
			if aerr := r.admitResolved(f, cand); aerr != nil {
				closeAfter(f, "unadmitted mount")
				return nil, aerr
			}
			return f, nil
		}
		if isMissing(err) {
			last = mapErrno("resolve directory", err)
			continue
		}
		return nil, mapErrno("resolve directory", err)
	}
	if last == nil {
		last = fmt.Errorf("resolve directory: %w", ErrNotFound)
	}
	return nil, last
}

// admitResolved classifies the filesystem a resolved directory sits on, when
// it is not the one the share root was admitted on.
//
// A share that cannot cross a mount boundary needs no check: the kernel already
// refused the crossing, so the descriptor is on the admitted device.
func (r *ShareRoot) admitResolved(dir *os.File, path string) error {
	if !r.policy.CrossMount {
		return nil
	}
	var stx unix.Statx_t
	if err := withFdErr(dir, func(fd int) error {
		return unix.Statx(fd, "", unix.AT_EMPTY_PATH, unix.STATX_BASIC_STATS, &stx)
	}); err != nil {
		return mapErrno("stat resolved directory", err)
	}
	dev := unix.Mkdev(stx.Dev_major, stx.Dev_minor)
	if dev == r.dev {
		return nil
	}
	return r.admitDevice(dir, dev, path)
}

// openLeafNamed opens p's last component under the share's resolve flags,
// trying each Unicode spelling of that name in turn, and reports which spelling
// the filesystem actually had.
//
// The parent chain is resolved in one call and the leaf is one more hop against
// that already-safe descriptor. The leaf needs its own hop because its
// symlink-ness has to be gated by the same policy, and because a name written
// in one normal form under a parent written in another is the ordinary case.
func (r *ShareRoot) openLeafNamed(p SafePath, flags uint64) (*os.File, string, error) {
	resolve := resolveFlags(r.policy)
	if p.IsRoot() {
		f, err := openat2(r.anchor, ".", flags, 0, resolve)
		if err != nil {
			return nil, "", mapErrno("open share root", err)
		}
		return f, ".", nil
	}

	parentComps, leaf := splitLeaf(p.comps)
	parent, err := r.resolveDir(parentComps)
	if err != nil {
		return nil, "", err
	}
	defer closeAfter(parent, "parent directory")

	var last error
	for _, cand := range lookupCandidates(leaf) {
		f, err := openat2(parent, cand, flags, 0, resolve)
		if err == nil {
			// The leaf itself can be the mount point, which the parent's own
			// verdict says nothing about.
			if aerr := r.admitResolved(f, cand); aerr != nil {
				closeAfter(f, "unadmitted mount")
				return nil, "", aerr
			}
			return f, cand, nil
		}
		if isMissing(err) {
			last = mapErrno("open", err)
			continue
		}
		return nil, "", mapErrno("open", err)
	}
	if last == nil {
		last = fmt.Errorf("open: %w", ErrNotFound)
	}
	return nil, "", last
}

func (r *ShareRoot) openLeaf(p SafePath, flags uint64) (*os.File, error) {
	f, _, err := r.openLeafNamed(p, flags)
	return f, err
}

// splitLeaf splits a non-root path into its parent components and its last
// component. Callers special-case the share root before reaching here.
func splitLeaf(comps []string) ([]string, string) {
	n := len(comps)
	return comps[:n-1], comps[n-1]
}

// keepAlive holds a file until the current call returns, so a descriptor
// handed to the kernel cannot be closed by a finalizer underneath it.
func keepAlive(f *os.File) { runtime.KeepAlive(f) }

// rawFd is a descriptor number for a call that needs several at once, such as
// building an SCM_RIGHTS message. The caller must hold every file alive across
// the syscall; withFd and withFd2 do that for the one- and two-file cases and
// keepAliveAll for the rest.
func rawFd(f *os.File) (int, error) {
	if f == nil {
		return 0, errors.New("vfs: a nil file has no descriptor")
	}
	return int(f.Fd()), nil
}
