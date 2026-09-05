package auth

import (
	"context"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/limits"
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
	// Before holds the last row id of the preceding page, exclusive. Paging is
	// by cursor rather than offset, keeping page boundaries correct while new
	// rows arrive ahead of them.
	Before int64
	Limit  int
}

// AuditRow is one entry of the log.
//
// No wire tags and no wire types: the timestamp is the number it is, and the
// tier that serves it decides how a number crosses. A service type shaped by
// a JSON format is one whose fields cannot be renamed without breaking a
// client that this package does not know exists.
type AuditRow struct {
	RowID int64
	TsNs  int64
	Actor *int64
	// ActorName is best effort: nil for a system-attributed row and for an
	// account that has since been deleted.
	ActorName *string
	Event     string
	// Nil rather than empty when an event names nothing, so a screen can tell
	// "no target" from "a target with a blank name".
	Target *string
	IP     *string
	UA     string
	OK     bool
	Detail *string
}

// Audit writes one row and returns its error.
//
// A caller that has not yet acted can refuse to act on a log it cannot keep,
// which is why this reports rather than swallows. A caller whose write has
// already committed uses Record instead.
func (s *Service) Audit(ctx context.Context, actor *int64, event, target, ip, ua string, ok bool) error {
	err := s.store.AppendAudit(ctx, state.AuditEntry{
		TsNs:   s.now(),
		Actor:  actor,
		Event:  event,
		Target: target,
		IP:     ip,
		UA:     ua,
		OK:     ok,
	})
	if err == nil {
		if s.auditOps.Add(1)%1000 == 0 {
			_, _ = s.store.PruneAudit(ctx, 0, limits.AuditRetentionMaxRows)
		}
	}
	return err
}

// PruneAudit removes audit entries older than olderThanNs or caps to maxRows.
func (s *Service) PruneAudit(ctx context.Context, olderThanNs int64, maxRows int) (int64, error) {
	return s.store.PruneAudit(ctx, olderThanNs, maxRows)
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

// auditOverscan sets how many rows are read for each row returned, so in-process
// filtering still fills a page. The multiplier is finite by design: a filter
// matching nothing reads this many rows and halts instead of scanning the entire
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
	var actor *int64
	if f.Actor != 0 {
		actor = &f.Actor
	}
	records, err := s.store.AuditPageFiltered(ctx, f.Before, f.Limit, f.Event, actor)
	if err != nil {
		return nil, nil, err
	}
	names := s.actorNames(ctx)

	rows := make([]AuditRow, 0, len(records))
	for _, rec := range records {
		if !auditMatches(rec, f) {
			continue
		}
		row := AuditRow{
			RowID:  rec.RowID,
			TsNs:   rec.TsNs,
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
	}
	var next *int64
	if len(records) == f.Limit {
		last := records[len(records)-1].RowID
		next = &last
	}
	return rows, next, nil
}

// auditCountCeiling stops the counting walk. A window covering a busy month
// would otherwise read the whole table to draw one screen.
const auditCountCeiling = 200_000

// AuditBucket is one interval's outcome counts.
type AuditBucket struct {
	StartNs int64
	OK      int
	Failed  int
}

// AuditCounts totals the log per interval, for the graph above the log list.
//
// Counts rather than rows, and its own walk rather than adding up a page: a
// page is a hundred rows and a graph covers a window, so a chart drawn from
// what the reader scrolled to would rise and fall with the scrolling.
//
// The frame is the caller's: since, until and the width are decided above
// this, so the two logs' bars line up. Buckets come back oldest first,
// covering the window with no holes, because a renderer that had to tell a
// gap from a zero would guess. Truncated reports a walk that stopped on the
// ceiling.
func (s *Service) AuditCounts(
	ctx context.Context, f AuditFilter, startNs, widthNs int64, count int,
) (buckets []AuditBucket, truncated bool, err error) {
	if widthNs <= 0 || count <= 0 {
		return nil, false, nil
	}

	buckets = make([]AuditBucket, count)
	for i := range buckets {
		buckets[i].StartNs = startNs + int64(i)*widthNs
	}

	endNs := startNs + int64(count)*widthNs
	if f.Actor == 0 && f.SinceNs == 0 && f.UntilNs == 0 {
		aggCounts, aerr := s.store.AuditCountsAgg(ctx, startNs, endNs, widthNs, f.Event)
		if aerr == nil {
			for _, b := range aggCounts {
				if b.BucketIdx >= 0 && b.BucketIdx < count {
					buckets[b.BucketIdx].OK += b.OK
					buckets[b.BucketIdx].Failed += b.Failed
				}
			}
			return buckets, false, nil
		}
	}
	var before int64
	last := count - 1
	for seen := 0; seen < auditCountCeiling; {
		records, rerr := s.store.AuditPage(ctx, before, auditDefaultLimit*auditOverscan)
		if rerr != nil {
			return nil, false, rerr
		}
		if len(records) == 0 {
			return buckets, false, nil
		}
		for _, rec := range records {
			seen++
			before = rec.RowID
			if rec.TsNs < startNs {
				// Past the window's oldest edge, and the walk is ordered, so
				// nothing older can land in a bucket either.
				return buckets, false, nil
			}
			if !auditMatches(rec, f) {
				continue
			}
			at := int((rec.TsNs - startNs) / widthNs)
			if at < 0 || at > last {
				continue
			}
			if rec.OK {
				buckets[at].OK++
			} else {
				buckets[at].Failed++
			}
		}
		if ctx.Err() != nil {
			return nil, false, ctx.Err()
		}
	}
	return buckets, true, nil
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
