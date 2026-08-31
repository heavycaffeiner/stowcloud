// Linux only, for the same reason as the rest of this package.
//go:build linux

// The administration family's projection.
//
// The audit log is the sensitive one. It records what people did, so it is
// read by an administrator investigating something and must not become a
// second copy of what it was recording: an event's detail may name a target,
// never a credential.
package handler

import (
	"strconv"

	"github.com/heavycaffeiner/stowcloud/go/engine/service/auth"
)

// AuditRowView is one log entry.
type AuditRowView struct {
	// ID is the row's cursor value, which a client passes back to page. A
	// string because it is an int64 and paging with a rounded id skips or
	// repeats a page.
	ID   string `json:"id"`
	TsNs string `json:"ts_ns"`

	// Actor is nil for a system-attributed row. ActorName is best effort and
	// stays nil for an account that has since been deleted, which is a
	// different thing from an event nobody performed.
	Actor     *string `json:"actor,omitempty"`
	ActorName *string `json:"actor_name,omitempty"`

	Event string `json:"event"`

	// Target is nil when an event names nothing, so a screen can tell that
	// from a target whose name is blank.
	Target *string `json:"target,omitempty"`

	IP *string `json:"ip,omitempty"`
	UA string  `json:"ua,omitempty"`

	// OK is a boolean because a numeric result renders as a blank outcome, and
	// an audit screen that cannot show whether an action succeeded is not an
	// audit screen.
	OK bool `json:"ok"`

	Detail *string `json:"detail,omitempty"`
}

// AuditRowOf projects one entry.
func AuditRowOf(r auth.AuditRow) AuditRowView {
	v := AuditRowView{
		ID:        strconv.FormatInt(r.RowID, 10),
		TsNs:      strconv.FormatInt(r.TsNs, 10),
		ActorName: r.ActorName,
		Event:     r.Event,
		Target:    r.Target,
		IP:        r.IP,
		UA:        r.UA,
		OK:        r.OK,
		Detail:    r.Detail,
	}
	if r.Actor != nil {
		a := strconv.FormatInt(*r.Actor, 10)
		v.Actor = &a
	}
	return v
}

// AuditPageView is one page of the log.
type AuditPageView struct {
	Rows []AuditRowView `json:"rows"`

	// Next is absent on the final page, so its presence is what a client tests
	// rather than comparing the row count against a limit it may not know.
	Next string `json:"next,omitempty"`
}

// AuditPageOf projects one page.
func AuditPageOf(rows []auth.AuditRow, next *int64) AuditPageView {
	out := AuditPageView{Rows: make([]AuditRowView, 0, len(rows))}
	for _, r := range rows {
		out.Rows = append(out.Rows, AuditRowOf(r))
	}
	if next != nil {
		out.Next = strconv.FormatInt(*next, 10)
	}
	return out
}
