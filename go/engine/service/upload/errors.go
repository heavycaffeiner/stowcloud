//go:build linux

package upload

import (
	"errors"
	"fmt"
	"strings"
)

// The engine's refusals. None of them chooses a wire status: that mapping
// happens once, in the protocol layer, where the caller's grants are known.
var (
	// ErrBadRequest is a malformed offset, a digest of the wrong length, or a
	// deferred length that was never supplied.
	ErrBadRequest = errors.New("malformed upload request")

	// ErrNotFound is an unknown session, or one belonging to another account.
	// The two are one answer: telling them apart says a session id is real.
	ErrNotFound = errors.New("no such upload session")

	// ErrOffsetConflict is a chunk that did not arrive at the resumable
	// offset, on a session that is not random-access.
	ErrOffsetConflict = errors.New("offset does not match the resumable offset")

	// ErrTooLarge is a write past the declared length.
	ErrTooLarge = errors.New("past the declared upload length")

	// ErrChunkTooSmall is a mid-stream chunk below the session's own floor.
	// It drives a client's own adjustment, so it is ordinary operation rather
	// than a fault: the refusal is the protocol working.
	ErrChunkTooSmall = errors.New("chunk below the minimum size")

	// ErrChecksum is a per-chunk digest that did not match. The range is not
	// recorded, so the client resends it rather than resuming past a hole.
	ErrChecksum = errors.New("chunk checksum mismatch")

	// ErrVerify is a whole-file digest that did not match at finalize.
	ErrVerify = errors.New("whole-file verification failed")

	// ErrSessionExpired is a session past its lifetime.
	ErrSessionExpired = errors.New("upload session expired")

	// ErrSessionState is a call against a session that is not receiving.
	ErrSessionState = errors.New("upload session is not receiving")

	// ErrIncomplete is a finalize whose interval set does not cover the file.
	ErrIncomplete = errors.New("upload incomplete")

	// ErrFragmented is an insert that would take the interval set past the
	// bound on how many disjoint runs one session tracks. It costs the client
	// one chunk rather than the session.
	ErrFragmented = errors.New("too many disjoint received ranges")

	// ErrExhausted is a per-account bound: too many sessions, too many
	// reserved bytes, or not enough room on the destination filesystem.
	ErrExhausted = errors.New("upload resource limit exceeded")

	// ErrAliasTaken is a transfer id this account already holds. Rebinding it
	// would orphan the first session's spool with nothing naming it.
	ErrAliasTaken = errors.New("transfer id already bound")

	// ErrCacheFull is the spool at its budget. What it waits for is a disk
	// write already in progress, which is why the refusal carries a delay.
	ErrCacheFull = errors.New("the upload cache is full")

	// ErrUnknownAlgo is a checksum algorithm this server does not offer.
	ErrUnknownAlgo = errors.New("unknown checksum algorithm")

	// ErrNoCache is the cache switch on a deployment that has no spool.
	ErrNoCache = errors.New("this deployment has no upload cache")
)

// IncompleteError is a finalize over holes, naming what is missing so a
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

func (e *IncompleteError) Is(target error) bool { return target == ErrIncomplete }

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
// client's own adjustment needs.
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

// CacheFullError carries how long to wait before trying again, because what
// the caller is waiting for is a disk write already under way.
type CacheFullError struct {
	RetryAfterSeconds int
}

func (e *CacheFullError) Error() string {
	return fmt.Sprintf("the upload cache is full; retry in %d seconds", e.RetryAfterSeconds)
}

func (e *CacheFullError) Is(target error) bool { return target == ErrCacheFull }
