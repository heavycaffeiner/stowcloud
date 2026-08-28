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

	// ErrNotFound covers an unknown session and one owned by another account.
	// Both yield the same answer, since separating them would confirm that a
	// session id exists.
	ErrNotFound = errors.New("no such upload session")

	// ErrOffsetConflict reports a chunk arriving somewhere other than the
	// resumable offset on a session that is not random-access.
	ErrOffsetConflict = errors.New("offset does not match the resumable offset")

	// ErrTooLarge reports a write extending beyond the declared length.
	ErrTooLarge = errors.New("past the declared upload length")

	// ErrChunkTooSmall reports a mid-stream chunk under the session's floor. It
	// prompts the client to adjust, making it routine operation rather than a
	// fault: the rejection is the protocol functioning.
	ErrChunkTooSmall = errors.New("chunk below the minimum size")

	// ErrChecksum reports a per-chunk digest mismatch. The range goes
	// unrecorded, so the client resends it instead of resuming beyond a gap.
	ErrChecksum = errors.New("chunk checksum mismatch")

	// ErrVerify reports a whole-file digest mismatch at finalize.
	ErrVerify = errors.New("whole-file verification failed")

	// ErrSessionExpired is a session past its lifetime.
	ErrSessionExpired = errors.New("upload session expired")

	// ErrSessionState reports a call made against a non-receiving session.
	ErrSessionState = errors.New("upload session is not receiving")

	// ErrIncomplete reports a finalize whose interval set leaves the file
	// uncovered.
	ErrIncomplete = errors.New("upload incomplete")

	// ErrFragmented is an insert that would take the interval set past the
	// bound on how many disjoint runs one session tracks. It costs the client
	// one chunk rather than the session.
	ErrFragmented = errors.New("too many disjoint received ranges")

	// ErrExhausted reports a per-account bound: excess sessions, excess reserved
	// bytes, or insufficient room on the destination filesystem.
	ErrExhausted = errors.New("upload resource limit exceeded")

	// ErrAliasTaken reports a transfer id the account already holds. Rebinding
	// would strand the first session's spool with nothing referencing it.
	ErrAliasTaken = errors.New("transfer id already bound")

	// ErrCacheFull is the spool at its budget. What it waits for is a disk
	// write already in progress, which is why the refusal carries a delay.
	ErrCacheFull = errors.New("the upload cache is full")

	// ErrUnknownAlgo reports a checksum algorithm this server does not
	// provide.
	ErrUnknownAlgo = errors.New("unknown checksum algorithm")

	// ErrNoCache is the cache switch on a deployment that has no spool.
	ErrNoCache = errors.New("this deployment has no upload cache")
)

// IncompleteError reports a finalize across gaps, listing what is absent so a
// client can resend precisely that rather than restarting.
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

// ConflictError supplies the offset the client should have written at, sparing a
// resuming client a second round trip to discover it.
type ConflictError struct {
	Expected uint64
	Got      uint64
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("offset conflict: the resumable offset is %d, the chunk arrived at %d",
		e.Expected, e.Got)
}

func (e *ConflictError) Is(target error) bool { return target == ErrOffsetConflict }

// ChunkTooSmallError states the floor that rejected the chunk, the figure a
// client needs to adjust.
type ChunkTooSmallError struct {
	Min uint64
	Got uint64
}

func (e *ChunkTooSmallError) Error() string {
	return fmt.Sprintf("chunk of %d bytes is below the %d-byte minimum and is not the last",
		e.Got, e.Min)
}

func (e *ChunkTooSmallError) Is(target error) bool { return target == ErrChunkTooSmall }

// ExhaustedError identifies which bound rejected the request. A bare "resource
// exhausted" gives an operator nothing to act on.
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
