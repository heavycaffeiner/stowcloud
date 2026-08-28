//go:build linux

package vfs

import (
	"errors"
	"fmt"
	"time"

	"golang.org/x/sys/unix"
)

// The two operations the upload engine needs that no other caller does: the
// part file it accumulates bytes in, and the modification time a client asks
// its finished file to carry.

// CreatePart creates a control file under the reserved prefix and returns it
// open for reading and writing.
//
// It is the upload engine's part file and nothing else. The handle is
// read-write by construction rather than through an access intent, for the
// same reason the durable write's staging file is: a file this server just
// created is writable because it made it, not because a caller asked for
// privilege on a path that already existed.
//
// The create is exclusive, so a collision is the kernel's refusal rather than
// a clobber, and the mode is applied afterwards because the create filters it
// through the process umask.
//
// The name has to come from JoinControl, which is the only call permitted to
// produce the reserved prefix. A name without it is refused here rather than
// quietly creating something a listing would show.
func (r *ShareRoot) CreatePart(p SafePath) (*File, error) {
	if p.IsRoot() {
		return nil, fmt.Errorf("create a part file: %w", ErrDenied)
	}
	if !IsReservedName(p.Name()) {
		return nil, invalidName(p.Name(),
			"a part file has to carry a control prefix or a listing would show it")
	}
	parentComps, leaf := splitLeaf(p.Components())
	parent, err := r.resolveDir(parentComps)
	if err != nil {
		return nil, err
	}
	defer closeAfter(parent, "part file parent")

	f, err := openat2Raw(parent, leaf,
		unix.O_CREAT|unix.O_EXCL|unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC,
		uint64(r.policy.ModeFile), resolveFlags(r.policy))
	if err != nil {
		return nil, mapErrno("create a part file", err)
	}
	// By descriptor, never by a second name lookup: a second lookup is a
	// window in which the just-created name could already be something else.
	if cerr := withFdErr(f, func(fd int) error { return unix.Fchmod(fd, r.policy.ModeFile) }); cerr != nil {
		return nil, errors.Join(mapErrno("set the part file's mode", cerr), closeFailed(f))
	}
	return &File{f: f}, nil
}

// SetTimes sets the modification time and leaves the access time alone.
//
// A symlink is never followed: the timestamp belongs to the entry named, not
// to whatever it points at.
func (r *ShareRoot) SetTimes(p SafePath, mtimeNs int64) error {
	if p.IsRoot() {
		return fmt.Errorf("set times: %w", ErrDenied)
	}
	parentComps, leaf := splitLeaf(p.Components())
	parent, err := r.resolveDir(parentComps)
	if err != nil {
		return err
	}
	defer closeAfter(parent, "set times parent")

	// The division is written out rather than left to the truncating one,
	// because a timestamp before the epoch has a negative remainder and would
	// otherwise land a second in the future.
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
		if err == nil {
			return nil
		}
		last = err
		if !isMissing(err) {
			break
		}
	}
	return mapErrno("set times", last)
}
