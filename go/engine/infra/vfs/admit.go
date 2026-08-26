package vfs

import (
	"errors"
	"fmt"
)

// Admission decides whether a filesystem's guarantees are strong enough to
// hold this package's contracts: stable inode identity, a birth time, and
// notification behavior this server can rely on.
//
// This is an allow-list, not a deny-list. A magic number this build has no
// case for falls through to the default refusal, which is the fail-closed
// half of the design: an unclassified filesystem does not become supported
// by nobody having reasoned about it yet.

// ErrUnsupportedFilesystem is the sentinel every admission refusal wraps.
var ErrUnsupportedFilesystem = errors.New("vfs: the filesystem is not supported")

// AdmissionError names where the refused filesystem was found and why.
// Path is the mount's own location, which for a nested mount is not the
// share root the operator configured; naming it is what turns a refusal an
// operator cannot act on into one they can.
type AdmissionError struct {
	Path   string
	Type   FsType
	Reason string
}

func (e *AdmissionError) Error() string {
	return fmt.Sprintf("vfs: %s is on %s, which is not supported: %s", e.Path, e.Type, e.Reason)
}

func (e *AdmissionError) Is(target error) bool { return target == ErrUnsupportedFilesystem }

// Admission is the verdict for one filesystem instance.
type Admission struct {
	// OK is whether a share may be served from this filesystem at all.
	OK bool
	// Warn is set for a filesystem that is admitted despite a caveat the
	// operator has to be told about.
	Warn string
	// Reflink is true when a copy on this filesystem can be a metadata
	// operation rather than a full byte-for-byte duplication.
	Reflink bool
}

// AdmitFsType classifies a filesystem by its magic number alone.
//
// The type is necessary but not sufficient: AdmitMount below still checks
// that this particular mount instance reports a birth time, since some
// mount options on an otherwise-supported type omit it.
func AdmitFsType(t FsType) (Admission, string) {
	switch t {
	case FsExt4, FsZfs, FsF2fs:
		return Admission{OK: true}, ""
	case FsBtrfs, FsXfs:
		return Admission{OK: true, Reflink: true}, ""
	case FsTmpfs:
		return Admission{OK: true, Warn: "everything on this share is lost when the machine restarts"}, ""
	case FsOverlay:
		return Admission{}, "a container's writable layer has inodes that do not survive a restart, and it misses changes made to the layer beneath"
	case FsFuse:
		return Admission{}, "identity cannot be proven to survive a restart or a remount"
	case FsNfs:
		return Admission{}, "identity cannot be proven to survive a remount, and changes made elsewhere are not seen"
	case FsCifs, FsSmb2:
		return Admission{}, "identity cannot be proven to survive a remount, and the remote name rules differ from this server's"
	case FsSquashfs:
		return Admission{}, "read-only, and this server has no read-only share contract"
	case FsNtfs:
		return Admission{}, "this driver's identity, name and notification behavior are not established here"
	default:
		return Admission{}, "this server has no name for this filesystem, so its identity and notification behavior are unknown"
	}
}

// AdmitMount is the full verdict for one mount: the type table plus the
// per-instance birth-time check.
//
// File identity in this product is derived from (share, dev, ino, btime).
// Without a birth time, an inode reused after a deletion is
// indistinguishable from the file that previously held it, so a mount
// reporting none is refused even when its type is on the allow-list.
func AdmitMount(path string, t FsType, hasBtime bool) (Admission, error) {
	adm, reason := AdmitFsType(t)
	if !adm.OK {
		return Admission{}, &AdmissionError{Path: path, Type: t, Reason: reason}
	}
	if !hasBtime {
		return Admission{}, &AdmissionError{
			Path:   path,
			Type:   t,
			Reason: "this mount reports no birth time, so an inode reused after a deletion cannot be told apart from the file it replaced",
		}
	}
	return adm, nil
}
