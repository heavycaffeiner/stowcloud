package auth

import (
	"context"
	"strconv"

	"github.com/heavycaffeiner/stowcloud/go/engine/store/state"
)

// The audit log. Rows are appended and never edited; the actor is nulled only
// by the account-deletion cascade, because the log deliberately outlives the
// accounts it names.

// The event names this package writes itself. Every other surface passes its
// own, because only the handler knows what it just did.
const (
	EventLogin = "login"
)

// AuditFilter narrows a page. A zero field is not a filter.
type AuditFilter struct {
	Actor   int64
	Event   string
	SinceNs int64
	UntilNs int64
	// Before is the previous page's last row id, exclusive. Cursor-paged
	// rather than offset-paged, so a page boundary stays correct while new
	// rows land ahead of it.
	Before int64
	Limit  int
}

// AuditRow is one entry as a screen reads it.
//
// The field names are the client's contract. OK is a boolean, because a
// numeric result renders as a blank outcome; the timestamp is a string,
// because nanoseconds exceed what a JSON number carries exactly and an entry
// landing on the wrong second is one nobody can correlate.
type AuditRow struct {
	RowID int64  `json:"rowid"`
	TsNs  string `json:"ts_ns"`
	Actor *int64 `json:"actor"`
	// ActorName is best effort: null for a system-attributed row and for an
	// account that has since been deleted.
	ActorName *string `json:"actor_name"`
	Event     string  `json:"event"`
	// Null rather than empty when an event names nothing, so a screen can
	// tell "no target" from "a target whose name is blank".
	Target *string `json:"target"`
	IP     *string `json:"ip"`
	UA     string  `json:"ua"`
	OK     bool    `json:"ok"`
	Detail *string `json:"detail"`
}

// Audit writes one row and returns its error.
//
// A caller that has not yet acted can refuse to act on a log it cannot keep,
// which is why this reports rather than swallows. A caller whose write has
// already committed uses Record instead.
func (s *Service) Audit(ctx context.Context, actor *int64, event, target, ip, ua string, ok bool) error {
	return s.store.AppendAudit(ctx, state.AuditEntry{
		TsNs:   s.now(),
		Actor:  actor,
		Event:  event,
		Target: target,
		IP:     ip,
		UA:     ua,
		OK:     ok,
	})
}

// Record writes one row for an action that has already happened, and never
// fails it.
//
// The account exists, the grant is live, the share is registered by the time
// this is called. Returning an error would report a change that did happen as
// one that did not, which is the defect the login path had. A row that could
// not be written is logged instead, where an operator can see the log has a
// hole in it.
//
// It is called from the handlers rather than from the write methods, because
// only the handler knows who is acting: the same method serves an
// administrator editing somebody else and an account editing itself.
func (s *Service) Record(ctx context.Context, actor int64, event, target, ip, ua string, ok bool) {
	var who *int64
	if actor != 0 {
		who = &actor
	}
	if err := s.Audit(ctx, who, event, target, ip, ua, ok); err != nil {
		s.warn("an action was not recorded in the audit log", err)
	}
}

// auditOverscan is how many rows are read per row returned, so filtering in
// this process still fills a page. It is bounded rather than unbounded: a
// filter matching nothing reads this many and stops rather than the whole
// log.
const auditOverscan = 20

// auditDefaultLimit is the page size a caller that names none gets.
const auditDefaultLimit = 100

// AuditPage reads one bounded page, newest first, and the cursor for the
// next.
//
// The filters apply here rather than being composed into the statement,
// because a statement built from optional parts is what every statement in
// the store being a constant exists to prevent.
func (s *Service) AuditPage(ctx context.Context, f AuditFilter) ([]AuditRow, *int64, error) {
	if f.Limit <= 0 {
		f.Limit = auditDefaultLimit
	}
	records, err := s.store.AuditPage(ctx, f.Before, f.Limit*auditOverscan)
	if err != nil {
		return nil, nil, err
	}
	names := s.actorNames(ctx)

	rows := make([]AuditRow, 0, f.Limit)
	for _, rec := range records {
		if !auditMatches(rec, f) {
			continue
		}
		row := AuditRow{
			RowID:  rec.RowID,
			TsNs:   strconv.FormatInt(rec.TsNs, 10),
			Actor:  rec.Actor,
			Event:  rec.Event,
			Target: rec.Target,
			IP:     rec.IP,
			UA:     rec.UA,
			OK:     rec.OK,
			Detail: rec.Detail,
		}
		if rec.Actor != nil {
			if name, ok := names[*rec.Actor]; ok {
				n := name
				row.ActorName = &n
			}
		}
		rows = append(rows, row)
		if len(rows) == f.Limit {
			break
		}
	}
	var next *int64
	if len(rows) == f.Limit {
		last := rows[len(rows)-1].RowID
		next = &last
	}
	return rows, next, nil
}

// actorNames maps account ids to names for the page being rendered, read once
// rather than per row: a page is a hundred rows and most share a handful of
// actors.
//
// Best effort: a lookup that fails leaves every actor unnamed rather than
// failing the page, because a log an administrator cannot open is worse than
// one whose authors show as numbers.
func (s *Service) actorNames(ctx context.Context) map[int64]string {
	accounts, err := s.store.ListAccounts(ctx)
	if err != nil {
		s.warn("the audit log's actor names could not be resolved", err)
		return nil
	}
	out := make(map[int64]string, len(accounts))
	for _, a := range accounts {
		out[a.ID] = a.Name
	}
	return out
}

func auditMatches(r state.AuditRecord, f AuditFilter) bool {
	switch {
	case f.Actor != 0 && (r.Actor == nil || *r.Actor != f.Actor):
		return false
	case f.Event != "" && r.Event != f.Event:
		return false
	case f.SinceNs != 0 && r.TsNs < f.SinceNs:
		return false
	case f.UntilNs != 0 && r.TsNs > f.UntilNs:
		return false
	default:
		return true
	}
}
