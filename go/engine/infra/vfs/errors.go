package vfs

import (
	"errors"
	"fmt"
	"syscall"
)

// Sentinel errors this package returns. A caller matches one of these with
// errors.Is; the package never leaks a raw errno or an *os.PathError past
// mapErrno.
//
// This set stays deliberately small. Two service packages (core, upload)
// re-map every one of these into their own vocabulary by hand, so a new
// sentinel here is a new case two other packages must be told to add.
var (
	// ErrNotFound covers both ENOENT and ENOTDIR: something on the path does
	// not exist, or a step that had to be a directory was not one.
	ErrNotFound = errors.New("vfs: not found")

	// ErrDenied covers EACCES and EPERM.
	ErrDenied = errors.New("vfs: permission denied")

	ErrExists   = errors.New("vfs: already exists")
	ErrNotEmpty = errors.New("vfs: directory not empty")
	ErrNoSpace  = errors.New("vfs: no space left on device")

	// ErrCrossDevice is EXDEV: the two names named span a device boundary,
	// which a rename cannot cross. The caller above falls back to a copy.
	ErrCrossDevice = errors.New("vfs: crosses a device boundary")

	// ErrSymlinkDenied is ELOOP under this package's own resolve flags, kept
	// apart from ErrNotFound: "a symlink was refused" and "nothing is there"
	// are different facts, and only a caller holding the requester's grants
	// gets to decide which of the two that requester learns.
	ErrSymlinkDenied = errors.New("vfs: symlink traversal denied")

	// ErrIsDirectory is EISDIR: the target is a directory where a file was
	// wanted, the opposite case from ErrNotADirectory below. A caller opening
	// a path to stream or write content hits this, never a listing call.
	ErrIsDirectory = errors.New("vfs: is a directory")

	// ErrNotADirectory is produced by Alive, not by mapErrno: a directory
	// component turned out to be a plain file where a directory was
	// required. ENOTDIR itself still maps to ErrNotFound above, on the
	// reasoning that a missing step in a path and a wrong-typed step in a
	// path both mean "you cannot walk this the way you asked."
	ErrNotADirectory = errors.New("vfs: not a directory")
)

// mapErrno turns a raw errno from a syscall wrapper into this package's
// sentinel set. It is the only place a sentinel is built from an errno; every
// syscall wrapper in this package funnels its error through here rather than
// constructing one by hand.
//
// An errno absent from the table is wrapped, not dropped, so errors.As still
// finds the original syscall.Errno underneath op.
func mapErrno(op string, err error) error {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return err
	}
	switch errno {
	case syscall.ENOENT, syscall.ENOTDIR:
		return fmt.Errorf("%s: %w", op, ErrNotFound)
	case syscall.EACCES, syscall.EPERM:
		return fmt.Errorf("%s: %w", op, ErrDenied)
	case syscall.EEXIST:
		return fmt.Errorf("%s: %w", op, ErrExists)
	case syscall.ENOTEMPTY:
		return fmt.Errorf("%s: %w", op, ErrNotEmpty)
	case syscall.ENOSPC:
		return fmt.Errorf("%s: %w", op, ErrNoSpace)
	case syscall.EXDEV:
		return fmt.Errorf("%s: %w", op, ErrCrossDevice)
	case syscall.ELOOP:
		return fmt.Errorf("%s: %w", op, ErrSymlinkDenied)
	case syscall.EISDIR:
		return fmt.Errorf("%s: %w", op, ErrIsDirectory)
	}
	return fmt.Errorf("%s: %w", op, errno)
}

// isMissing is the only test a Unicode candidate-spelling loop uses to decide
// whether to try the next spelling, against the raw errno a syscall wrapper
// produced before mapErrno runs. Every other errno stops the loop at once:
// widening this set would let a permission refusal on the first spelling look
// like a missing file once a second spelling is tried.
func isMissing(err error) bool {
	return errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ENOTDIR)
}
