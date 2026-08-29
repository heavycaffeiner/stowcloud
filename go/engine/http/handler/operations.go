// Linux only, for the same reason as the rest of this package.
//go:build linux

// The long-operation family: list, get and cancel.
//
// The interface is declared here, beside its only consumer, rather than in a
// shared assembly struct. A family that reaches services through a grab bag
// gains access to everything in it, and the compiler stops saying what this
// one actually needs.
package handler

import (
	"context"
	"strconv"

	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
)

// OperationsAPI is what this family needs from the service layer.
type OperationsAPI interface {
	ListOperations(ctx context.Context, owner core.UserID, limit int) ([]core.Operation, error)
	Operation(ctx context.Context, owner core.UserID, id core.OperationID) (core.Operation, error)
	CancelOperation(ctx context.Context, owner core.UserID, id core.OperationID) error
}

// OperationView is one job as a client reads it.
//
// The id and the two counters are decimal strings. They are int64 on this side
// and a JavaScript number loses exactness past 2^53, so a job id that a client
// round-trips would come back as a different id.
type OperationView struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	State    string `json:"state"`
	Progress string `json:"progress"`
	Total    string `json:"total"`

	// Message is the failure line, absent while running or on success.
	Message string `json:"message,omitempty"`

	// Results is present once the job is terminal. Nothing streams during a
	// run, so a client polls and reads this when the state says to.
	Results []OperationItemView `json:"results,omitempty"`

	// Attempting is what the runner had started and never recorded an outcome
	// for. Only a job whose process died has any, and whether those items
	// landed is genuinely unknown, which is why they are not folded in with
	// the ones nothing touched.
	Attempting []string `json:"attempting,omitempty"`

	// Pending is what the job never reached. Untouched, so re-running exactly
	// these is safe, which is what lets a client offer them rather than only
	// counting them.
	Pending []string `json:"pending,omitempty"`
}

// OperationItemView is one item's outcome.
type OperationItemView struct {
	Index  string `json:"index"`
	Path   string `json:"path"`
	OK     bool   `json:"ok"`
	Reason string `json:"reason,omitempty"`
	Text   string `json:"text,omitempty"`
}

// TerminalStateName reports whether a state name means the job has finished.
//
// Over the name rather than the stored number, because this tier may not
// import the tier that owns the numbers. core.Operation.Terminal answers the
// same question on the service side; this is the one a client's own polling
// loop is written against, and both are checked against the same list of
// names.
func TerminalStateName(state string) (terminal, known bool) {
	switch state {
	case "done", "failed", "cancelled", "interrupted":
		return true, true
	case "running":
		return false, true
	default:
		// An unrecognised state counts as finished, because a client polling
		// forever on a state this build does not know is worse than one that
		// stops and shows what it has. It is reported as unknown as well: a
		// fallback that is indistinguishable from a listed answer makes the
		// list itself untestable, since dropping an entry would change
		// nothing observable.
		return true, false
	}
}

// OperationsOf projects a list of jobs.
//
// An empty list encodes as [] rather than null, so a client iterating the
// field does not have to test for it first.
func OperationsOf(ops []core.Operation) []OperationView {
	out := make([]OperationView, 0, len(ops))
	for _, op := range ops {
		out = append(out, OperationOf(op))
	}
	return out
}

// OperationOf projects one job.
func OperationOf(op core.Operation) OperationView {
	v := OperationView{
		ID:       strconv.FormatInt(int64(op.ID), 10),
		Kind:     op.KindName(),
		State:    op.StateName(),
		Progress: strconv.FormatInt(op.Progress, 10),
		Total:    strconv.FormatInt(op.Total, 10),
		Message:  op.Message,
	}
	if items := op.Items(); len(items) > 0 {
		v.Results = make([]OperationItemView, 0, len(items))
		for _, r := range items {
			v.Results = append(v.Results, OperationItemView{
				Index:  strconv.FormatInt(r.Index, 10),
				Path:   r.Path,
				OK:     r.OK,
				Reason: r.Reason,
				Text:   r.Text,
			})
		}
	}
	v.Attempting = append([]string(nil), op.Attempting...)
	v.Pending = append([]string(nil), op.Pending...)
	return v
}
