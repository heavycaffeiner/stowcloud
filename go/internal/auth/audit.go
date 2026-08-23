package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
)

// The two values the result column takes. A row records whether the thing was
// allowed to happen, which is the question an audit log is read to answer.
const (
	auditFailed = 0
	auditOK     = 1
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

// Record writes one row for an administrator's action and never fails the
// action itself.
//
// The write already happened by the time this is called: an account exists, a
// grant is live, a share is registered. Returning an error here would report a
// change that did happen as one that did not, which is the same defect the
// login path had. A row that could not be written is logged instead, where an
// operator can see that the log has a hole in it.
//
// Called from the handlers rather than from the write methods, because only
// the handler knows who is acting: the same method serves an administrator
// editing somebody else and an account editing itself.
func (s *Service) Record(ctx context.Context, actor int64, event, target, ip, ua string, ok bool) {
	result := auditFailed
	if ok {
		result = auditOK
	}
	var who sql.NullInt64
	if actor != 0 {
		who = sql.NullInt64{Int64: actor, Valid: true}
	}
	if err := s.audit(ctx, who, event, target, ip, ua, result, ""); err != nil {
		s.warnf("an action was not recorded in the audit log", err)
	}
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
//
// The field names are the client's contract. It reads `ok` as a boolean and
// resolves the actor to a name, so a row sent with a numeric `result` and no
// name renders as an entry whose outcome and author are both blank: present in
// the list, and saying nothing.
type AuditRow struct {
	RowID int64  `json:"rowid"`
	TsNs  string `json:"ts_ns"`
	Actor *int64 `json:"actor"`
	// ActorName is best-effort: null for a system-attributed row and for an
	// account that has since been deleted, which the log deliberately outlives.
	ActorName *string `json:"actor_name"`
	Event     string  `json:"event"`
	// Null rather than empty when an event names nothing, so the screen can
	// tell "no target" from "a target whose name is blank".
	Target *string `json:"target"`
	IP     *string `json:"ip"`
	UA     string  `json:"ua"`
	OK     bool    `json:"ok"`
	Detail *string `json:"detail"`
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

	// The account names, read once rather than per row: a page is a hundred
	// rows and most of them share a handful of actors.
	names := s.actorNames(ctx)

	for q.Next() {
		var (
			r      AuditRow
			actor  sql.NullInt64
			ts     int64
			target string
			ip     string
			result int
			detail string
		)
		if serr := q.Scan(&r.RowID, &ts, &actor, &r.Event, &target, &ip, &r.UA, &result, &detail); serr != nil {
			return nil, nil, serr
		}
		r.OK = result == auditOK
		if target != "" {
			r.Target = &target
		}
		if ip != "" {
			r.IP = &ip
		}
		if detail != "" {
			r.Detail = &detail
		}
		if actor.Valid {
			a := actor.Int64
			r.Actor = &a
			if n, ok := names[a]; ok {
				name := n
				r.ActorName = &name
			}
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

// actorNames maps account ids to names for the page being rendered.
//
// Best-effort: a lookup that fails leaves every actor unnamed rather than
// failing the page, because an audit log an administrator cannot open is worse
// than one whose authors show as ids.
func (s *Service) actorNames(ctx context.Context) map[int64]string {
	rows, err := s.ListUsers(ctx)
	if err != nil {
		s.warnf("the audit log's actor names could not be resolved", err)
		return nil
	}
	out := make(map[int64]string, len(rows))
	for _, u := range rows {
		out[u.ID] = u.Name
	}
	return out
}

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
