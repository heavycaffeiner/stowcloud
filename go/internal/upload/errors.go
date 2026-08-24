package upload

import (
	"errors"
	"fmt"
	"strings"
)

// The engine's errors. None of them chooses an HTTP status: that is the HTTP
// layer's one mapping function, where the caller's grants are known.

var (
	// ErrBadRequest is a malformed offset, a digest that is not the right
	// length, or a deferred length that was never supplied.
	ErrBadRequest = errors.New("malformed upload request")

	// ErrNotFound is an unknown session, or one belonging to another account.
	// The two are one answer: distinguishing them tells a stranger a session
	// id is real.
	ErrNotFound = errors.New("no such upload session")

	// ErrOffsetConflict is a chunk that did not arrive at the resumable
	// offset, on a session that is not random-access.
	ErrOffsetConflict = errors.New("offset does not match the resumable offset")

	// ErrPrecondition is a destination that changed since the session was
	// created.
	ErrPrecondition = errors.New("the destination changed since this session was created")

	// ErrTooLarge is a write past the declared length.
	ErrTooLarge = errors.New("past the declared upload length")

	// ErrChunkTooSmall is a mid-stream chunk below the session's own floor.
	// It is what drives a client's auto-adjust, so it is ordinary operation
	// rather than a fault: a refusal here is the protocol working.
	ErrChunkTooSmall = errors.New("chunk below the minimum size")

	// ErrChecksum is a per-chunk digest that did not match. The range is not
	// recorded, so the client resends it rather than resuming past a hole.
	ErrChecksum = errors.New("chunk checksum mismatch")

	// ErrVerify is a whole-file digest that did not match at finalize.
	ErrVerify = errors.New("whole-file verification failed")

	// ErrSessionExpired is a session past its lifetime. The sweep may already
	// have taken its part file.
	ErrSessionExpired = errors.New("upload session expired")

	// ErrSessionState is a call against a session that is not receiving.
	ErrSessionState = errors.New("upload session is not receiving")

	// ErrExhausted is a per-account bound: too many sessions, too many
	// reserved bytes, or not enough room on the destination filesystem.
	ErrExhausted = errors.New("upload resource limit exceeded")

	// ErrAliasTaken is a transfer id this account already holds. Rebinding it
	// would orphan the first session's spool with nothing pointing at it.
	ErrAliasTaken = errors.New("transfer id already bound")
)

// IncompleteError is a finalize over holes, naming what is missing so the
// client knows what to resend rather than starting again.
type IncompleteError struct {
	Missing []Range
}

func (e *IncompleteError) Error() string {
	parts := make([]string, 0, len(e.Missing))
	for _, r := range e.Missing {
		parts = append(parts, fmt.Sprintf("%d-%d", r.Lo, r.Hi))
	}
	return "upload incomplete, missing " + strings.Join(parts, ",")
}

// Is reports ErrIncomplete so a caller matches the sentinel and reads Missing.
func (e *IncompleteError) Is(target error) bool { return target == ErrIncomplete }

// ErrIncomplete is a finalize whose interval set does not cover the file.
var ErrIncomplete = errors.New("upload incomplete")

// ConflictError carries the offset the client should have written at, so a
// resuming client does not need a second round trip to find out.
type ConflictError struct {
	Expected uint64
	Got      uint64
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("offset conflict: the resumable offset is %d, the chunk arrived at %d",
		e.Expected, e.Got)
}

func (e *ConflictError) Is(target error) bool { return target == ErrOffsetConflict }

// ChunkTooSmallError names the floor that refused, which is the number a
// client's auto-adjust needs.
type ChunkTooSmallError struct {
	Min uint64
	Got uint64
}

func (e *ChunkTooSmallError) Error() string {
	return fmt.Sprintf("chunk of %d bytes is below the %d-byte minimum and is not the last",
		e.Got, e.Min)
}

func (e *ChunkTooSmallError) Is(target error) bool { return target == ErrChunkTooSmall }

// ExhaustedError names which bound refused. "Resource exhausted" without the
// name is a refusal an operator cannot act on.
type ExhaustedError struct {
	Limit string
}

func (e *ExhaustedError) Error() string { return "upload refused: " + e.Limit }

func (e *ExhaustedError) Is(target error) bool { return target == ErrExhausted }
