package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
)

// audit is the append-only record of who did what. Rows are never edited and
// the actor is only ever nulled by a deletes chain, never this code.
func (s *Service) audit(ctx context.Context, actor sql.NullInt64, event, target, ip, ua string, result int, detail string) error {
	_, err := s.st.SQL().ExecContext(ctx, sqlInsertAudit, s.now(), actor, event, target, ip, ua, result, detail)
	if err != nil {
		return fmt.Errorf("writing to the audit log: %w", err)
	}
	return nil
}

// Audit writes one append-only row. It is exported for the surfaces that own
// non-login events (an admin change, a revocation outside the login flow).
func (s *Service) Audit(ctx context.Context, actor sql.NullInt64, event, target, ip, ua string, result int) error {
	return s.audit(ctx, actor, event, target, ip, ua, result, "")
}

// AuditFilter narrows a page of the log. A zero field is not a filter.
type AuditFilter struct {
	Actor   int64
	Event   string
	SinceNs int64
	UntilNs int64
	// Before is the previous page's last row id, exclusive. Cursor-paged
	// rather than offset-paged, so a page boundary stays correct while new
	// rows keep landing ahead of it.
	Before int64
	Limit  int
}

// AuditRow is one entry as the admin screen reads it.
type AuditRow struct {
	RowID  int64  `json:"rowid"`
	TsNs   string `json:"ts_ns"`
	Actor  *int64 `json:"actor"`
	Event  string `json:"event"`
	Target string `json:"target"`
	IP     string `json:"ip"`
	UA     string `json:"ua"`
	Result int    `json:"result"`
}

// AuditPage reads one bounded page, newest first, and the cursor for the next.
//
// The filters are applied here rather than composed into the statement,
// because a statement built from optional parts is the thing every statement
// in this tree is a constant to avoid. The log is bounded per page, so the
// rows this reads are bounded too.
func (s *Service) AuditPage(ctx context.Context, f AuditFilter) (rows []AuditRow, next *int64, err error) {
	if f.Limit <= 0 {
		f.Limit = 100
	}
	// One more than asked for, so a full page can be told from the last one
	// without a second query.
	q, qerr := s.st.SQL().QueryContext(ctx, sqlReadAuditPage, f.Limit*auditOverscan)
	if qerr != nil {
		return nil, nil, qerr
	}
	defer func() { err = errors.Join(err, q.Close()) }()

	for q.Next() {
		var (
			r     AuditRow
			actor sql.NullInt64
			ts    int64
		)
		if serr := q.Scan(&r.RowID, &ts, &actor, &r.Event, &r.Target, &r.IP, &r.UA, &r.Result); serr != nil {
			return nil, nil, serr
		}
		if actor.Valid {
			a := actor.Int64
			r.Actor = &a
		}
		// A timestamp is a string because it exceeds what a JSON number
		// carries exactly, and a log entry that lands on the wrong second is
		// a log entry nobody can correlate.
		r.TsNs = strconv.FormatInt(ts, 10)

		if !auditMatches(r, ts, f) {
			continue
		}
		rows = append(rows, r)
		if len(rows) == f.Limit {
			break
		}
	}
	if rerr := q.Err(); rerr != nil {
		return nil, nil, rerr
	}
	if len(rows) == f.Limit {
		last := rows[len(rows)-1].RowID
		next = &last
	}
	return rows, next, nil
}

// auditOverscan is how many rows are read per row returned, so filtering in
// this process still fills a page. It is bounded rather than unbounded: a
// filter matching nothing reads this many and stops rather than the whole log.
const auditOverscan = 20

func auditMatches(r AuditRow, tsNs int64, f AuditFilter) bool {
	if f.Before != 0 && r.RowID >= f.Before {
		return false
	}
	if f.Actor != 0 && (r.Actor == nil || *r.Actor != f.Actor) {
		return false
	}
	if f.Event != "" && r.Event != f.Event {
		return false
	}
	if f.SinceNs != 0 && tsNs < f.SinceNs {
		return false
	}
	if f.UntilNs != 0 && tsNs > f.UntilNs {
		return false
	}
	return true
}
