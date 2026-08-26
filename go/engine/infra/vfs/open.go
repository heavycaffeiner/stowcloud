//go:build linux

package vfs

import (
	"fmt"
	"io"
	"log/slog"
	"os"

	"golang.org/x/sys/unix"
)

// closeAfter closes c and logs rather than propagates a failure. A close
// failing after its data was already synced tells a caller nothing it can
// act on, so every deferred close in this package goes through here instead
// of returning a second error alongside the operation's own.
func closeAfter(c io.Closer, what string) {
	if err := c.Close(); err != nil {
		slog.Warn("vfs: closing a descriptor failed", slog.String("what", what), slog.Any("error", err))
	}
}

// resolveFlags turns a share's policy into the openat2 resolve flag set that
// governs every resolution against that share.
//
// RESOLVE_NO_MAGICLINKS is unconditional: it is what refuses escape through
// /proc/self/fd and similar magic links, which are not ordinary symlinks and
// sit outside the symlink policy entirely.
func resolveFlags(p SharePolicy) uint64 {
	flags := uint64(unix.RESOLVE_NO_MAGICLINKS)
	switch p.Symlink {
	case SymlinkDeny:
		flags |= unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS
	case SymlinkWithinShare:
		flags |= unix.RESOLVE_IN_ROOT
	case SymlinkFollow:
		flags |= unix.RESOLVE_BENEATH
	}
	if !p.CrossMount {
		flags |= unix.RESOLVE_NO_XDEV
	}
	return flags
}

// openat2Raw is the sole entry point this package uses to open anything. Every
// multi-component walk this package performs, and every single leaf open,
// goes through it, so the kernel enforces the resolve flags atomically with
// the open: there is no separate check-then-open step for a race to land in.
func openat2Raw(dir *os.File, path string, flags, mode, resolve uint64) (*os.File, error) {
	how := unix.OpenHow{Flags: flags, Mode: mode, Resolve: resolve}
	fd, err := withFd(dir, func(dfd int) (int, error) { return unix.Openat2(dfd, path, &how) })
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}

// resolveDir resolves a whole component chain to an O_PATH directory
// descriptor in one openat2 call per Unicode candidate spelling, so every
// intermediate component is confined by the same kernel-enforced flags in one
// atomic walk.
//
// A candidate spelling is tried only when the previous one was missing
// (isMissing); any other errno is a real refusal and stops the loop at once,
// so a permission error on the first spelling is never masked by a second
// spelling that happens to be absent too.
func (r *ShareRoot) resolveDir(comps []string) (*os.File, error) {
	resolve := resolveFlags(r.policy)
	var last error
	for _, cand := range pathCandidates(comps) {
		f, err := openat2Raw(r.anchor, cand, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0, resolve)
		if err == nil {
			if aerr := r.admitResolved(f, cand); aerr != nil {
				closeAfter(f, "unadmitted directory")
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

// admitResolved classifies the device a resolved descriptor sits on, the
// first time a resolution crosses onto one this root has not seen. path is
// the spelling that was resolved, carried through only so a refusal names
// the mount an operator has to go look at.
//
// A share that refuses to cross a mount boundary needs no check here at all:
// the kernel's RESOLVE_NO_XDEV already refused the crossing before this ever
// runs, which is the stronger guarantee.
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

// splitLeaf separates a non-root path into its parent components and its
// last component. The share root itself has no leaf, so callers special-case
// it before reaching here.
func splitLeaf(comps []string) ([]string, string) {
	n := len(comps)
	return comps[:n-1], comps[n-1]
}

// openLeafNamed opens the leaf of p against the share's resolve flags,
// trying each Unicode spelling of the leaf name against the already
// resolved parent directory, and reports which spelling actually matched.
//
// The parent chain resolves in one call; the leaf is one further hop against
// that descriptor, kept separate because the leaf's own symlink-ness is
// gated by the same policy and because a leaf spelled in a different normal
// form than its parent is the ordinary case a macOS client produces.
func (r *ShareRoot) openLeafNamed(p SafePath, flags uint64) (*os.File, string, error) {
	resolve := resolveFlags(r.policy)
	if p.IsRoot() {
		f, err := openat2Raw(r.anchor, ".", flags, 0, resolve)
		if err != nil {
			return nil, "", mapErrno("open share root", err)
		}
		return f, ".", nil
	}

	parentComps, leaf := splitLeaf(p.Components())
	parent, err := r.resolveDir(parentComps)
	if err != nil {
		return nil, "", err
	}
	defer closeAfter(parent, "leaf parent directory")

	var last error
	for _, cand := range lookupCandidates(leaf) {
		f, err := openat2Raw(parent, cand, flags, 0, resolve)
		if err == nil {
			if aerr := r.admitResolved(f, cand); aerr != nil {
				closeAfter(f, "unadmitted leaf")
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

// openLeaf is openLeafNamed without the spelling report, for the ordinary
// caller that does not need to know which candidate matched.
func (r *ShareRoot) openLeaf(p SafePath, flags uint64) (*os.File, error) {
	f, _, err := r.openLeafNamed(p, flags)
	return f, err
}
