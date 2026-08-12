//go:build linux

package vfs

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

// mapErrno turns a raw errno into the error the layer above reads, keeping op
// so that "not found" says which operation did not find it.
//
// An errno with no entry in the table is wrapped rather than flattened, so
// errors.As still finds the number.
//
// ELOOP has its own error rather than folding into ErrNotFound, and the
// difference is load-bearing: it is how a share with the deny policy reports
// that a symlink was refused, which is a different fact from the target not
// existing, and only the layer that knows the caller's grants may decide which
// one a client learns.
func mapErrno(op string, err error) error {
	var e unix.Errno
	if !errors.As(err, &e) {
		return err
	}
	switch e {
	case unix.ENOENT, unix.ENOTDIR:
		return fmt.Errorf("%s: %w", op, ErrNotFound)
	case unix.EACCES, unix.EPERM:
		return fmt.Errorf("%s: %w", op, ErrDenied)
	case unix.EEXIST:
		return fmt.Errorf("%s: %w", op, ErrExists)
	case unix.ENOTEMPTY:
		return fmt.Errorf("%s: %w", op, ErrNotEmpty)
	case unix.ENOSPC:
		return fmt.Errorf("%s: %w", op, ErrNoSpace)
	case unix.EXDEV:
		return fmt.Errorf("%s: %w", op, ErrCrossDevice)
	case unix.ELOOP:
		return fmt.Errorf("%s: %w", op, ErrSymlinkDenied)
	case unix.EISDIR:
		return fmt.Errorf("%s: %w", op, ErrNotADirectory)
	}
	return fmt.Errorf("%s: %w", op, e)
}

// isMissing reports the two errnos a Unicode candidate loop continues on. Every
// other errno returns immediately, and collapsing that distinction makes a
// permission error look like a missing file.
func isMissing(err error) bool {
	return errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ENOTDIR)
}
