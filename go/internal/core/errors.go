package core

import (
	"errors"
	"fmt"
)

// The protocol-agnostic errors every core operation returns. None of them
// chooses an HTTP status: that is one mapping function in the HTTP layer,
// where the caller's grants are known and the rule that an unlistable path is
// 404 everywhere can be applied (S2).

var (
	// ErrNotFound means missing, or outside every grant. The two are one
	// answer by design: returning a denial tells a stranger the path exists.
	ErrNotFound = errors.New("not found")

	// ErrDenied means the caller may know the target exists but may not do
	// this to it. To a caller with no grant over it at all, denied and
	// missing are the same error, ErrNotFound.
	ErrDenied = errors.New("permission denied")

	// ErrPrecondition is a supplied validator that failed strong comparison,
	// carrying the current token so a conflict screen can show it.
	ErrPrecondition = errors.New("precondition failed")

	// ErrConflict is an operation that conflicts with current state without a
	// supplied validator.
	ErrConflict = errors.New("conflict")

	// ErrExists is create against an existing name with no-clobber.
	ErrExists = errors.New("already exists")

	// ErrNotEmpty is a directory delete without recursion.
	ErrNotEmpty = errors.New("directory not empty")

	// ErrCrossShare is an operation that cannot span shares atomically. It
	// names which half completed.
	ErrCrossShare = errors.New("cannot span shares atomically")

	// ErrNoSpace is ENOSPC, or the configured free-space floor.
	ErrNoSpace = errors.New("no space left")

	// ErrTrashDisabled is restore or purge against a share with trash off.
	ErrTrashDisabled = errors.New("trash is disabled for this share")

	// ErrLinkExpired is expired, revoked, or over its download cap. One
	// error because distinguishing them tells a stranger about the link.
	ErrLinkExpired = errors.New("share link is expired")

	// ErrQuotaExceeded is a write the acting user's ledger cap refuses.
	ErrQuotaExceeded = errors.New("quota exceeded")
)

// PreconditionError carries the current weak token alongside ErrPrecondition,
// so a conflict-resolution UI can show it without a second round trip.
type PreconditionError struct {
	Current string
}

func (e *PreconditionError) Error() string {
	return fmt.Sprintf("%s: current token %s", ErrPrecondition, e.Current)
}

func (e *PreconditionError) Unwrap() error { return ErrPrecondition }

// IsPrecondition reports a refusal that cannot pass a supplied validator.
func IsPrecondition(err error) bool { return errors.Is(err, ErrPrecondition) }
