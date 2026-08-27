// Package core is the protocol-agnostic domain API every protocol sits on.
// It knows nothing about HTTP, WebDAV or any wire shape: no error here
// chooses a status, and no type here carries a field one protocol wanted.
package core

import (
	"errors"
	"fmt"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
)

// The domain's whole error vocabulary. None of these chooses a wire status;
// that mapping happens once, in the protocol layer, where the caller's
// grants are known and the existence rule can be applied to the response.
var (
	// ErrNotFound is missing, or outside every grant. The two are one
	// answer by design: returning a denial tells a stranger the path
	// exists.
	ErrNotFound = errors.New("not found")

	// ErrDenied is a caller who may know the target exists but may not do
	// this to it. To a caller with no grant over the target at all, denied
	// and missing are the same error, ErrNotFound.
	ErrDenied = errors.New("permission denied")

	// ErrPrecondition is a supplied validator that failed strong
	// comparison. It always arrives wrapped by PreconditionError, which
	// carries the current token.
	ErrPrecondition = errors.New("precondition failed")

	// ErrConflict is an operation that conflicts with current state when no
	// validator was supplied. It is what opens the conflict dialogue on the
	// client.
	ErrConflict = errors.New("conflict")

	// ErrExists is a create against an existing name with no-clobber.
	ErrExists = errors.New("already exists")

	// ErrNotEmpty is a directory delete without recursion.
	ErrNotEmpty = errors.New("directory not empty")

	// ErrCrossShare is an operation that cannot span shares atomically. Its
	// message names which half completed.
	ErrCrossShare = errors.New("cannot span shares atomically")

	// ErrNoSpace is ENOSPC, or the configured free-space floor.
	ErrNoSpace = errors.New("no space left")

	// ErrTrashDisabled is a restore or a purge against a share with trash
	// off. A plain delete on such a share is not this error; it is simply a
	// permanent delete.
	ErrTrashDisabled = errors.New("trash is disabled for this share")

	// ErrLinkExpired is expired, or over the download cap. One error,
	// because distinguishing them tells a stranger about the link. A
	// revoked link is a deleted row, so its token answers ErrNotFound
	// instead.
	ErrLinkExpired = errors.New("share link is expired")

	// ErrQuotaExceeded is a write the acting user's ledger cap refuses.
	ErrQuotaExceeded = errors.New("quota exceeded")

	// ErrShareBroken is a share that is registered while its backing
	// directory is unavailable. Deliberately not ErrNotFound: the path the
	// caller named is good and the disk under it is gone, and telling
	// somebody their folder does not exist sends them looking in the wrong
	// place.
	ErrShareBroken = errors.New("the folder this share points at is unavailable")
)

// ShareBrokenError names which share is broken and why, so the message a
// user gets says the folder rather than the request.
//
// Share is the display name and Reason is the health surface's own token, so
// a screen and a probe asking the same question get the same word back.
// Neither carries a host path: the caller is being told which of their
// folders is unavailable, not where on the disk it lives.
type ShareBrokenError struct {
	Share  string
	Reason string
}

func (e *ShareBrokenError) Error() string {
	return fmt.Sprintf("%s: %s is %s", ErrShareBroken, e.Share, e.Reason)
}

func (e *ShareBrokenError) Unwrap() error { return ErrShareBroken }

// PreconditionError carries the current weak token alongside
// ErrPrecondition, so a conflict-resolution screen can show it without a
// second round trip. Current is empty when the target does not exist.
type PreconditionError struct {
	Current string
}

func (e *PreconditionError) Error() string {
	return fmt.Sprintf("%s: current token %s", ErrPrecondition, e.Current)
}

func (e *PreconditionError) Unwrap() error { return ErrPrecondition }

// IsPrecondition reports a refusal that cannot pass a supplied validator.
func IsPrecondition(err error) bool { return errors.Is(err, ErrPrecondition) }

// mapVFSErr converts a filesystem error into a domain sentinel. It lives
// here rather than beside any one caller because every file in the package
// crosses this boundary, and because the mapping is a property of the error
// taxonomy rather than of the operation that hit it.
//
// The existence rule is applied in Resolve, so a vfs.ErrNotFound reaching
// this function is a real missing path rather than a permission answer, and
// maps to the same ErrNotFound the resolver returns. The symlink denial
// folds into ErrDenied because which policy refused is not the caller's
// business.
//
// The default passes the error through unchanged. An error this table does
// not name is an infrastructure failure, and wrapping it in a domain
// sentinel would let a protocol layer map it to a 4xx it did not earn.
func mapVFSErr(err error) error {
	switch {
	case errors.Is(err, vfs.ErrNotFound):
		return ErrNotFound
	case errors.Is(err, vfs.ErrDenied), errors.Is(err, vfs.ErrSymlinkDenied):
		return ErrDenied
	case errors.Is(err, vfs.ErrExists):
		return ErrExists
	case errors.Is(err, vfs.ErrNotEmpty):
		return ErrNotEmpty
	case errors.Is(err, vfs.ErrNoSpace):
		return ErrNoSpace
	case errors.Is(err, vfs.ErrCrossDevice):
		return ErrCrossShare
	case errors.Is(err, vfs.ErrNotADirectory):
		// Listing a non-directory reports the path as not listable, which
		// is the same answer as a path that is not there.
		return ErrNotFound
	case errors.Is(err, vfs.ErrIsDirectory):
		// Streaming or reading a directory is a refusal, not a miss.
		return ErrDenied
	case errors.Is(err, vfs.ErrInvalidName):
		// A name that cannot address anything is a missing path.
		return ErrNotFound
	default:
		return err
	}
}
