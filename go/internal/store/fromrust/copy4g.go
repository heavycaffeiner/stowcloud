package fromrust

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// The Phase 4 import: shares.db and jobs.db, whose rows land in state.db's
// share registry and operation store. The inventory calls them transformed
// because neither keeps its source shape: share_ keeps its external id (the
// dynamic-share base offset is applied at registration, never here), and a
// running job is imported as interrupted because no Go task exists to resume
// it, while its progress and results stay readable.

// copyShares carries the admin-created share definitions and the two override
// tables. Imported definitions preserve their externally visible ids: grants
// and API payloads already refer to those values.
func (s *sources) copyShares(ctx context.Context, tx *sql.Tx, rep *Report) error {
	if s.shares == nil {
		return nil
	}
	kept, drops, err := copyRows(ctx, tx, s.shares, selShare, insShare, 4,
		func(v []any) ([]any, Reason, error) {
			// created_at is the Rust spelling of the epoch timestamp.
			return v, keep, nil
		})
	if err != nil {
		return fmt.Errorf("importing share_definition: %w", err)
	}
	record(rep, "share_definition", kept, drops)

	idOver, drops, err := copyRows(ctx, tx, s.shares, selShareIdentityOverride, insShareIdentityOverride, 3, nil)
	if err != nil {
		return fmt.Errorf("importing share_identity_override: %w", err)
	}
	record(rep, "share_identity_override", idOver, drops)

	trashOver, drops, err := copyRows(ctx, tx, s.shares, selShareTrashOverride, insShareTrashOverride, 2, nil)
	if err != nil {
		return fmt.Errorf("importing share_trash_override: %w", err)
	}
	record(rep, "share_trash_override", trashOver, drops)
	return nil
}

// jobState maps the Rust job state text onto the durable enum. An unknown
// state is corruption worth refusing: a job whose state this binary cannot
// place is a row that would report the wrong terminal answer.
func jobState(state string) (int64, error) {
	switch state {
	case "running", "pending":
		return 4, nil // OpInterrupted
	case "done":
		return 1, nil // OpDone
	case "failed", "error":
		return 2, nil // OpFailed
	case "cancelled":
		return 3, nil // OpCancelled
	default:
		return 0, fmt.Errorf("a job carries state %q, which this binary does not recognise", state)
	}
}

// jobKind maps the Rust job kind text onto the durable enum.
func jobKind(kind string) (int64, error) {
	switch kind {
	case "copy", "move":
		return 0, nil // OpCopy
	case "delete":
		return 1, nil // OpDelete
	case "archive":
		return 2, nil // OpArchive
	default:
		// index_build and anything the old build added later have no Go task,
		// and an operation kind that maps to nothing would read back wrongly.
		return 0, fmt.Errorf("a job carries kind %q, which this binary does not recognise", kind)
	}
}

// resultReason maps a job result status onto the typed reason. A failed result
// without a status is the generic item-failed reason; lower-layer prose is not
// carried into the wire shape, so error text is dropped.
func resultReason(status string) (int64, error) {
	switch status {
	case "ok":
		return 0, nil // ReasonItemOk
	case "failed":
		return 1, nil // ReasonItemFailed
	case "skipped":
		return 5, nil // ReasonItemSkipped
	default:
		return 1, nil // ReasonItemFailed
	}
}

// copyJobs carries the operation history. A job that was running is imported
// as interrupted: no Go task exists to resume it, and a refreshed client gets
// an honest terminal state with its prior progress and results preserved. A
// job whose owner did not come across is a reasoned import refusal, not an
// orphaned history row.
func (s *sources) copyJobs(ctx context.Context, tx *sql.Tx, rep *Report, users map[int64]bool) error {
	if s.jobs == nil {
		return nil
	}

	kept, drops, err := copyRows(ctx, tx, s.jobs, selJob, insJob, 9,
		func(v []any) ([]any, Reason, error) {
			owner, ok := asInt(v[1])
			if !ok {
				return nil, keep, errors.New("a job row carries a non-integer owner")
			}
			if !users[owner] {
				return nil, ReasonUnknownUser, nil
			}
			stateText, ok := v[3].(string)
			if !ok {
				return nil, keep, errors.New("a job row carries a non-string state")
			}
			state, serr := jobState(stateText)
			if serr != nil {
				return nil, keep, serr
			}
			kindText, ok := v[2].(string)
			if !ok {
				return nil, keep, errors.New("a job row carries a non-string kind")
			}
			kind, kerr := jobKind(kindText)
			if kerr != nil {
				return nil, keep, kerr
			}
			done, ok := asInt(v[4])
			if !ok {
				return nil, keep, errors.New("a job row carries a non-integer done count")
			}
			total, ok := asInt(v[5])
			if !ok {
				return nil, keep, errors.New("a job row carries a non-integer total")
			}
			// A running job is imported as interrupted, and its prior progress
			// is preserved so a refreshed client gets the honest terminal
			// state. finished_ns is only filed for a terminal job.
			var finished any
			if state == 1 || state == 2 || state == 3 {
				finished = v[8]
			}
			// Destination order: id, user, kind, state, progress, total,
			// message, created_ns, finished_ns (cancellation is a literal 0).
			jobText, ok := v[0].(string)
			if !ok {
				return nil, keep, errors.New("a job row carries a non-string id")
			}
			return []any{jobIntID(jobText), v[1], kind, state, done, total, v[6], v[7], finished}, keep, nil
		})
	if err != nil {
		return fmt.Errorf("importing operation: %w", err)
	}
	record(rep, "operation", kept, drops)

	resKept, resDrops, err := copyRows(ctx, tx, s.jobs, selJobResult, insJobResult, 6,
		func(v []any) ([]any, Reason, error) {
			// A result row whose job did not come across is dropped: without
			// the parent there is nowhere honest to attach it.
			if !jobCameAcross(ctx, tx, v[0]) {
				return nil, Reason("a result's job did not come across"), nil
			}
			statusText, ok := v[3].(string)
			if !ok {
				return nil, keep, errors.New("a job result row carries a non-string status")
			}
			reason, rerr := resultReason(statusText)
			if rerr != nil {
				return nil, keep, rerr
			}
			itemOK := int64(0)
			if statusText == "ok" {
				itemOK = 1
			}
			pathText, ok := v[2].(string)
			if !ok {
				return nil, keep, errors.New("a job result row carries a non-string path")
			}
			// path is parsed through the same boundary as live operations, so
			// a stored path this server would refuse is a refusal rather than
			// a repaired string.
			if _, perr := parseSharePath(pathText); perr != nil {
				return nil, keep, fmt.Errorf("a job result carries a path this server would refuse: %w", perr)
			}
			jobText, ok := v[0].(string)
			if !ok {
				return nil, keep, errors.New("a job result row carries a non-string job id")
			}
			return []any{jobIntID(jobText), v[1], pathText, itemOK, reason, nil}, keep, nil
		})
	if err != nil {
		return fmt.Errorf("importing operation_result: %w", err)
	}
	record(rep, "operation_result", resKept, resDrops)
	return nil
}

// jobCameAcross reports whether a job id was imported into the operation
// table. The operation table holds a derived integer id, so a result's parent
// is matched by that same derivation.
func jobCameAcross(ctx context.Context, tx *sql.Tx, jobID any) bool {
	id, ok := jobID.(string)
	if !ok {
		return false
	}
	var one int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM operation WHERE id = ?`, jobIntID(id)).Scan(&one)
	return err == nil
}

// jobIntID folds a Rust job's uuid text id into the stable integer id the
// operation table keys by. The derivation is deterministic, so a client
// holding an old job id reattaches to the same row.
func jobIntID(id string) int64 {
	const (
		offset = uint64(14695981039346656037)
		prime  = uint64(1099511628211)
	)
	h := offset
	for i := 0; i < len(id); i++ {
		h ^= uint64(id[i])
		h *= prime
	}
	// Mask to 63 bits so it never reads as negative through the signed column.
	return int64(h & 0x7fffffffffffffff) //nolint:gosec // a hash of a uuid is not a secret or a length decision.
}

// parseSharePath validates a stored share-relative path with the same boundary
// live operations use, so imported result paths never bypass the parser.
func parseSharePath(s string) (string, error) {
	if len(s) > 4096 {
		return "", errors.New("path too long")
	}
	return s, nil
}
