package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// The audit log: append-only, never edited. The actor column is nulled only
// by the account-deletion cascade, because the log deliberately outlives the
// accounts it names: "who did this" matters most for the account that no
// longer exists.

// AuditEntry is one row to append.
type AuditEntry struct {
	TsNs int64
	// Actor is absent for an attempt that names no account, which is what a
	// login against an unknown name looks like.
	Actor  *int64
	Event  string
	Target string
	IP     string
	UA     string
	OK     bool
	Detail string
}

// AuditRecord is one row as it reads back. Target, IP and Detail come back as
// absent rather than empty, so a surface can tell "no target" from "a target
// whose name is blank".
type AuditRecord struct {
	RowID  int64
	TsNs   int64
	Actor  *int64
	Event  string
	Target *string
	IP     *string
	Detail *string
	UA     string
	OK     bool
}

// AppendAudit writes one row.
//
// It returns its error, and whether that error fails the action being
// recorded is the caller's decision: a caller that has not yet acted can
// refuse to act on a log it cannot keep, and a caller whose write already
// committed must not report a change that happened as one that did not.
func (d *DB) AppendAudit(ctx context.Context, e AuditEntry) error {
	if err := d.f.EnsureWritable(); err != nil {
		return err
	}
	result := int64(0)
	if e.OK {
		result = 1
	}
	if err := d.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, sqlInsertAudit,
			e.TsNs, idArg(e.Actor), e.Event, textArg(e.Target),
			textArg(e.IP), textArg(e.UA), result, textArg(e.Detail))
		return err
	}); err != nil {
		return fmt.Errorf("writing to the audit log: %w", err)
	}
	return nil
}

// AuditPage reads rows newest first, starting strictly before a cursor.
//
// The cursor is the previous page's last rowid, so a page boundary stays
// correct while new rows land ahead of it; an offset would shift every page
// whenever the log grew. before <= 0 starts at the newest row.
//
// Filtering is the caller's, over the rows this returns. A statement composed
// from optional filter parts is exactly what every statement in this package
// being a constant exists to prevent, and the caller bounds how much it reads
// by the limit it passes.
func (d *DB) AuditPage(ctx context.Context, before int64, limit int) (out []AuditRecord, err error) {
	query, args := sqlSelectAuditPage, []any{limit}
	if before > 0 {
		query, args = sqlSelectAuditPageBefore, []any{before, limit}
	}
	return d.queryAuditRecords(ctx, query, args...)
}

func (d *DB) queryAuditRecords(ctx context.Context, query string, args ...any) (out []AuditRecord, err error) {
	rows, err := d.f.SQL().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("reading the audit log: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var (
			r                  AuditRecord
			actor              sql.NullInt64
			target, ip, detail sql.NullString
			ua                 sql.NullString
			result             int64
		)
		if serr := rows.Scan(&r.RowID, &r.TsNs, &actor, &r.Event,
			&target, &ip, &ua, &result, &detail); serr != nil {
			return nil, fmt.Errorf("reading an audit row: %w", serr)
		}
		if actor.Valid {
			a := actor.Int64
			r.Actor = &a
		}
		if target.Valid {
			v := target.String
			r.Target = &v
		}
		if ip.Valid {
			v := ip.String
			r.IP = &v
		}
		if detail.Valid {
			v := detail.String
			r.Detail = &v
		}
		r.UA = ua.String
		r.OK = result == 1
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading the audit log: %w", err)
	}
	return out, nil
}

// AuditBucketCount is one aggregated interval's outcome count.
type AuditBucketCount struct {
	BucketIdx int
	OK        int
	Failed    int
}

// AuditPageFiltered reads rows matching event and/or actor.
func (d *DB) AuditPageFiltered(ctx context.Context, before int64, limit int, event string, actor *int64) ([]AuditRecord, error) {
	if event != "" && actor == nil {
		query, args := sqlSelectAuditPageEvent, []any{event, limit}
		if before > 0 {
			query, args = sqlSelectAuditPageBeforeEvent, []any{event, before, limit}
		}
		return d.queryAuditRecords(ctx, query, args...)
	}
	if actor != nil && event == "" {
		query, args := sqlSelectAuditPageActor, []any{*actor, limit}
		if before > 0 {
			query, args = sqlSelectAuditPageBeforeActor, []any{*actor, before, limit}
		}
		return d.queryAuditRecords(ctx, query, args...)
	}
	return d.AuditPage(ctx, before, limit)
}

// AuditCountsAgg aggregates outcome counts using SQLite group-by.
func (d *DB) AuditCountsAgg(ctx context.Context, startNs, endNs, widthNs int64, event string) ([]AuditBucketCount, error) {
	query, args := sqlSelectAuditCounts, []any{startNs, widthNs, startNs, endNs}
	if event != "" {
		query, args = sqlSelectAuditCountsEvent, []any{startNs, widthNs, startNs, endNs, event}
	}
	rows, err := d.f.SQL().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("aggregating audit counts: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()

	bucketMap := make(map[int]*AuditBucketCount)
	for rows.Next() {
		var idx, result, count int
		if err := rows.Scan(&idx, &result, &count); err != nil {
			return nil, fmt.Errorf("reading audit count row: %w", err)
		}
		b, exists := bucketMap[idx]
		if !exists {
			b = &AuditBucketCount{BucketIdx: idx}
			bucketMap[idx] = b
		}
		if result == 1 {
			b.OK += count
		} else {
			b.Failed += count
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("aggregating audit counts: %w", err)
	}
	out := make([]AuditBucketCount, 0, len(bucketMap))
	for _, b := range bucketMap {
		out = append(out, *b)
	}
	return out, nil
}

// PruneAudit trims audit rows older than retentionNs, or when maxRows > 0,
// ensures no more than maxRows of the newest entries remain.
func (d *DB) PruneAudit(ctx context.Context, olderThanNs int64, maxRows int) (int64, error) {
	if err := d.f.EnsureWritable(); err != nil {
		return 0, err
	}
	var total int64
	err := d.Write(ctx, func(tx *sql.Tx) error {
		if olderThanNs > 0 {
			res, err := tx.ExecContext(ctx, sqlDeleteAuditBefore, olderThanNs)
			if err != nil {
				return err
			}
			if n, rerr := res.RowsAffected(); rerr == nil {
				total += n
			}
		}
		if maxRows > 0 {
			res, err := tx.ExecContext(ctx, sqlDeleteAuditOldestKeepN, maxRows)
			if err != nil {
				return err
			}
			if n, rerr := res.RowsAffected(); rerr == nil {
				total += n
			}
		}
		return nil
	})
	return total, err
}
