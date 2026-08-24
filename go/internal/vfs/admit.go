package vfs

import (
	"errors"
	"fmt"
)

// The filesystem admission gate.
//
// A fail-closed allow-list, not a list of refusals. A magic value this version
// has no name for cannot become supported by nobody having thought about it
// yet.
//
// Refusing at registration is the decision. Accepting anything and degrading
// quietly produces a deployment that looks healthy and loses file identity
// months later, by which time the sync clients have already written the wrong
// thing into their own journals.

// ErrUnsupportedFilesystem is a share on a filesystem this design cannot hold
// its contracts on.
var ErrUnsupportedFilesystem = errors.New("vfs: the filesystem is not supported")

// AdmissionError names the filesystem and says why. Naming it is the whole
// point: "unsupported filesystem" is a refusal an operator cannot act on.
type AdmissionError struct {
	// Path is where the refused filesystem was found, which for a nested mount
	// is not the share root the operator configured.
	Path   string
	Type   FsType
	Reason string
}

func (e *AdmissionError) Error() string {
	return fmt.Sprintf("vfs: %s is on %s, which is not supported: %s", e.Path, e.Type, e.Reason)
}

func (e *AdmissionError) Is(target error) bool { return target == ErrUnsupportedFilesystem }

// Admission is the verdict for one filesystem.
type Admission struct {
	// OK is whether the share may be served from here at all.
	OK bool
	// Warn is set for a filesystem that is admitted with a caveat the operator
	// has to see.
	Warn string
	// Reflink is whether a copy on this filesystem can be a reference rather
	// than a duplication of every byte.
	Reflink bool
}

// AdmitFsType classifies a filesystem by its magic alone.
//
// The named type is necessary and not sufficient: the caller also has to check
// that this instance reports a birth time, which AdmitMount does.
func AdmitFsType(t FsType) (Admission, string) {
	switch t {
	case FsExt4, FsZfs, FsF2fs:
		return Admission{OK: true}, ""
	case FsBtrfs, FsXfs:
		// These two can copy by reference, so a copy costs metadata rather
		// than every byte.
		return Admission{OK: true, Reflink: true}, ""
	case FsTmpfs:
		return Admission{
			OK:   true,
			Warn: "this share is on tmpfs, so everything in it is lost when the machine restarts",
		}, ""

	case FsOverlay:
		// A container's writable layer has unstable inodes across restarts and
		// misses changes made to the layer beneath, so the whole file-identity
		// design breaks, the derived node id included.
		return Admission{}, "a container's writable layer has inodes that do not survive a restart"
	case FsFuse:
		// Stability across a process restart or a remote remount cannot be
		// proven at registration, and this design has no path-based identity
		// mode to fall back to.
		return Admission{}, "identity cannot be proven to survive a restart or a remount"
	case FsNfs:
		return Admission{}, "identity cannot be proven to survive a remount, and changes made elsewhere are not seen"
	case FsCifs, FsSmb2:
		return Admission{}, "identity cannot be proven to survive a remount, and the remote name rules differ from this server's"
	case FsSquashfs:
		// The product has no read-only share contract, so this is a mount on
		// which every write fails.
		return Admission{}, "it is read-only, and this server has no read-only share contract"
	case FsNtfs:
		return Admission{}, "this driver's identity, name and notification behaviour are not established here"
	}

	// A future or unclassified filesystem is unsupported until its identity
	// and notification behaviour are known. This is the fail-closed half, and
	// it is the reason this is an allow-list.
	return Admission{}, "this server has no name for this filesystem, so its identity and notification behaviour are unknown"
}

// AdmitMount is the full verdict for one mount: the type, and whether this
// instance of it reports a birth time.
//
// A filesystem instance that reports no birth time is refused even when its
// type is one of the supported ones. A device and inode pair alone cannot tell
// a file from a different file that reused the inode after a deletion, so the
// replacement and stable-identity contracts cannot be held without it.
func AdmitMount(path string, t FsType, hasBtime bool) (Admission, error) {
	adm, reason := AdmitFsType(t)
	if !adm.OK {
		return Admission{}, &AdmissionError{Path: path, Type: t, Reason: reason}
	}
	if !hasBtime {
		return Admission{}, &AdmissionError{
			Path: path, Type: t,
			Reason: "this instance reports no birth time, and an inode reused after a deletion is otherwise indistinguishable from the file that had it",
		}
	}
	return adm, nil
}
