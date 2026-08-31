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

// CreatePart makes a control file beneath the reserved prefix, returning it
// opened for reading and writing.
//
// This serves the upload engine's part files exclusively. Read-write access
// follows from construction rather than from a requested access intent, on the
// same grounds as the staging file used by durable writes: this server may
// write the file because it just created it, not because someone asked for
// privileges on a pre-existing path.
//
// Creation is exclusive, turning a collision into a kernel rejection instead of
// an overwrite. The mode is set after the fact, since creation would otherwise
// filter it through the process umask.
//
// Names must originate from JoinControl, the sole call allowed to produce the
// reserved prefix. Anything else is rejected here rather than silently creating
// a file that would appear in a listing.
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
