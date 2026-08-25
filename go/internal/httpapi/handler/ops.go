// Linux only: it depends on packages that are Linux only.
//go:build linux

// Package handler is the REST surface's handlers: one file per resource, and
// every handler returns an error the ErrorMapper renders rather than choosing
// a status itself. The only status a handler names is on the success path.
package handler

import (
	"net/http"
	"strconv"

	"github.com/heavycaffeiner/stowcloud/go/internal/apierr"
	"github.com/heavycaffeiner/stowcloud/go/internal/core"
	"github.com/heavycaffeiner/stowcloud/go/internal/store/state"
)

// The operation surface: /api/jobs answers a long operation's status, and
// /api/jobs/{id}/cancel requests its cancellation. Status and cancel are the
// only two things a client does with an operation; the work itself is
// owned by the core.

// operationResponse is a job as the tray reads it.
//
// The id is a string because every other id on this surface is, and the client
// hands it straight back in a path. done/total drive the progress bar, errors
// is what the failure dialogue reads, and each result carries a full error
// envelope rather than a bare status word, because the screen branches on the
// code (a conflict opens the overwrite dialogue, a quota failure does not).
type operationResponse struct {
	ID         string      `json:"id"`
	Kind       string      `json:"kind"`
	State      string      `json:"state"`
	Done       int64       `json:"done"`
	Total      int64       `json:"total"`
	Current    *string     `json:"current"`
	Errors     []string    `json:"errors"`
	Results    []batchItem `json:"results"`
	Attempting []string    `json:"attempting"`
	Pending    []string    `json:"pending"`
}

// Operation answers GET /api/jobs/{id}.
func Operation(d Deps) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		uid, cerr := userOf(r)
		if cerr != nil {
			return cerr
		}
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			return apierr.BadRequest("jobs.id", "id")
		}
		op, err := d.Core.Operation(r.Context(), uid, core.OperationID(id))
		if err != nil {
			return err
		}
		return writeJSON(w, http.StatusOK, operationToJSON(op))
	})
}

// OperationCancel answers POST /api/jobs/{id}/cancel and DELETE
// /api/jobs/{id}.
//
// Two spellings because the client uses the second and this server mounted
// only the first, so cancelling answered "method not allowed" from a route
// that exists. One handler, so they cannot drift.
func OperationCancel(d Deps) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		uid, cerr := userOf(r)
		if cerr != nil {
			return cerr
		}
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			return apierr.BadRequest("jobs.id", "id")
		}
		if err := d.Core.CancelOperation(r.Context(), uid, core.OperationID(id)); err != nil {
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	})
}

func operationToJSON(op core.Operation) operationResponse {
	out := operationResponse{
		ID:      strconv.FormatInt(int64(op.ID), 10),
		Kind:    opKindString(op.Kind),
		State:   opStateString(op.State),
		Done:    op.Progress,
		Total:   op.Total,
		Errors:  []string{},
		Results: make([]batchItem, 0, len(op.Results)),
		// Never nil: the client iterates both without checking, and these were
		// hardcoded empty here while the store had no record to fill them from,
		// so an interrupted job could not say what it had left undone.
		Attempting: emptyIfNil(op.Attempting),
		Pending:    emptyIfNil(op.Pending),
	}
	if op.Message != "" {
		out.Errors = append(out.Errors, op.Message)
	}
	for _, res := range op.Results {
		item := batchItem{Path: res.Path, OK: res.OK}
		if !res.OK {
			item.Error = opResultError(res)
			if res.Text != "" {
				out.Errors = append(out.Errors, res.Text)
			}
		}
		out.Results = append(out.Results, item)
	}
	return out
}

// opResultError renders one failed item as the same envelope a refused request
// carries, so the screen branches on one vocabulary rather than two.
func opResultError(res state.OpResult) *apierr.Wire {
	var e *apierr.Error
	switch res.Reason {
	case state.ReasonItemDenied:
		e = apierr.NewError(apierr.CodeACLDenied, "permission denied", "")
	case state.ReasonItemNotFound:
		e = apierr.MapNotFound()
	case state.ReasonItemConflict:
		e = apierr.NewError(apierr.CodeFsConflict, "state conflict", "")
	case state.ReasonItemSkipped:
		e = apierr.NewError(apierr.CodeInvalidRequest, "skipped", "jobs.item_skipped")
	case state.ReasonItemOk, state.ReasonItemFailed:
		e = apierr.Internal()
	default:
		e = apierr.Internal()
	}
	w := e.Wire()
	return &w
}

// emptyIfNil keeps a JSON array from encoding as null.
func emptyIfNil(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

func opStateString(s state.OpState) string {
	switch s {
	case state.OpRunning:
		return "running"
	case state.OpDone:
		return "done"
	case state.OpFailed:
		// "error" is the terminal state the client knows. It was "failed",
		// which matched no branch there, so a failed job was polled until the
		// client's own timeout fired and reported a hung server.
		return "error"
	case state.OpCancelled:
		return "cancelled"
	case state.OpInterrupted:
		return "interrupted"
	}
	return "unknown"
}

func opKindString(k state.OpKind) string {
	switch k {
	case state.OpCopy:
		return "copy"
	case state.OpDelete:
		return "delete"
	case state.OpArchive:
		return "archive"
	case state.OpIndexBuild:
		// Underscore, which is the spelling the client switches on. It was a
		// hyphen here and matched nothing.
		return "index_build"
	}
	return "unknown"
}

// Operations answers GET /api/jobs: what this account has in flight.
func Operations(d Deps) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		uid, cerr := userOf(r)
		if cerr != nil {
			return cerr
		}
		ops, err := d.Core.ListOperations(r.Context(), uid, jobListLimit)
		if err != nil {
			return err
		}
		out := make([]operationResponse, 0, len(ops))
		for _, op := range ops {
			out = append(out, operationToJSON(op))
		}
		return writeJSON(w, http.StatusOK, map[string]any{"jobs": out})
	})
}

// jobListLimit bounds the listing. The table grows with every batch anyone
// runs, and the screen shows what is running and what just finished.
const jobListLimit = 100
