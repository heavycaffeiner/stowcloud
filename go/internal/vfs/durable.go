//go:build linux

package vfs

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// stagingPrefix is the one reserved prefix a write ever produces.
const stagingPrefix = ".scpart-"

// DurableOpts is what a durable write needs beyond the content itself.
type DurableOpts struct {
	// Mode is applied to the staging file verbatim, not through umask. When the
	// write replaces an existing file the original's mode wins, because the
	// neighbours' access to it survives us.
	Mode uint32

	// Owner is applied to a newly created file. Nil leaves it at the process
	// uid and gid.
	Owner *Owner

	// NoClobber maps to RENAME_NOREPLACE, so publishing over an existing name
	// is refused atomically rather than checked and then raced.
	NoClobber bool
}

// Durable reports what a completed write did to the entry it replaced.
type Durable struct {
	// Replaced is true when the destination already existed and its mode and
	// ownership were transplanted onto the replacement.
	Replaced bool

	// OwnerRestore is nil when the replaced file's uid and gid were both put
	// back, and otherwise the error that refused. EPERM here is the ordinary
	// answer for an unprivileged process and is reported rather than swallowed,
	// so the caller decides whether transplanting the group alone would have
	// been enough. The mode is not reported this way: a mode that could not be
	// restored fails the write outright.
	OwnerRestore error
}

// WriteDurable stages content under the reserved prefix, makes it durable, and
// publishes it under p atomically.
//
// Every step is load-bearing. The staging name is unlistable while it exists,
// the content is synced before the rename that publishes it, the mode and
// ownership of a file being replaced are restored before that rename, and the
// parent directory is synced after it. Without that last step, on ext4 or XFS a
// power cut can leave the full contents under a staging name nobody will look
// for.
//
// This is the only function in the tree that renames. Everything that mutates a
// file goes through it.
func (r *ShareRoot) WriteDurable(p SafePath, opt DurableOpts, write func(*File) error) (Durable, error) {
	var done Durable
	if p.IsRoot() {
		return done, fmt.Errorf("durable write: %w", ErrDenied)
	}

	name, err := stagingName()
	if err != nil {
		return done, err
	}
	// Through JoinControl, which is the only function permitted to produce the
	// reserved prefix, so the staging name is bounded and validated by the same
	// table every other name is.
	stagingPath, err := p.Parent().JoinControl(name)
	if err != nil {
		return done, err
	}
	staging := stagingPath.Name()

	// One descriptor for the parent, opened for reading rather than O_PATH
	// because step seven fsyncs it and fsync on O_PATH is EBADF. It is also the
	// directory the create and the rename go through, so the staging file and
	// its destination are in the same directory by construction and the rename
	// is atomic.
	dir, err := r.openLeaf(p.Parent(), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC)
	if err != nil {
		return done, err
	}
	defer closeAfter(dir, "durable write parent")

	// The replaced file's metadata has to be read before anything is published
	// under its name.
	prior, priorName, replacing, err := r.priorOf(dir, p.Name())
	if err != nil {
		return done, err
	}
	if replacing && opt.NoClobber {
		// A cheap refusal before the content is written. RENAME_NOREPLACE below
		// is still the authority: this only avoids staging a whole file to
		// discover a collision that already existed.
		return done, fmt.Errorf("durable write: %w", ErrExists)
	}

	f, err := openat2(dir, staging,
		unix.O_CREAT|unix.O_EXCL|unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC,
		uint64(opt.Mode), resolveFlags(r.policy))
	if err != nil {
		return done, mapErrno("create the staging file", err)
	}
	handle := &File{f: f}

	published := false
	defer func() {
		closeAfter(f, "staging file")
		if published {
			return
		}
		// There is no Drop, so the staging file's removal on every failure path
		// is written out. A removal that itself fails leaves an unlistable
		// orphan for the sweep rather than a visible one for a user.
		if err := withFdErr(dir, func(fd int) error { return unix.Unlinkat(fd, staging, 0) }); err != nil {
			slog.Warn("a staging file survived a failed durable write",
				slog.String("name", staging), slog.Any("error", err))
		}
	}()

	// The exact configured mode regardless of umask, and the result is checked.
	// Discarding it is what makes a neighbour lose access.
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
	if err := renameat(dir, staging, dir, dest, flags); err != nil {
		return done, mapErrno("publish", err)
	}
	published = true

	// The name is durable only after this. Syncing the file guaranteed the
	// bytes; this is what guarantees anyone can find them.
	if err := syncDirFd(dir); err != nil {
		return done, err
	}
	return done, nil
}

// priorOf stats the destination through the parent descriptor and reports the
// spelling the filesystem actually has, so the publish lands on the entry being
// replaced rather than beside it.
func (r *ShareRoot) priorOf(dir *os.File, leaf string) (Stat, string, bool, error) {
	resolve := resolveFlags(r.policy)
	for _, cand := range lookupCandidates(leaf) {
		f, err := openat2(dir, cand, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0, resolve)
		if err != nil {
			if isMissing(err) {
				continue
			}
			return Stat{}, "", false, mapErrno("stat the entry being replaced", err)
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

// stagingName is the control name a write stages under. The random half comes
// from crypto/rand so two concurrent writes to one destination cannot pick the
// same name, and O_EXCL turns a collision into a refusal rather than a
// clobber.
func stagingName() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("name a staging file: %w", err)
	}
	return stagingPrefix + hex.EncodeToString(b[:]), nil
}

// IsStagingName reports a name this package produced for a write in flight. The
// upload orphan sweep needs it, and it is here rather than there so the prefix
// has one definition.
func IsStagingName(name string) bool {
	return strings.HasPrefix(name, stagingPrefix) && len(name) > len(stagingPrefix)
}
