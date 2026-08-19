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

type operationResponse struct {
	ID      int64  `json:"id"`
	Kind    string `json:"kind"`
	State   string `json:"state"`
	Results []struct {
		Path   string `json:"path,omitempty"`
		Status string `json:"status"`
	} `json:"results,omitempty"`
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

// OperationCancel answers POST /api/jobs/{id}/cancel.
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
	out := operationResponse{ID: int64(op.ID), Kind: opKindString(op.Kind), State: opStateString(op.State)}
	for _, res := range op.Results {
		out.Results = append(out.Results, struct {
			Path   string `json:"path,omitempty"`
			Status string `json:"status"`
		}{Path: res.Path, Status: opResultStatus(res.OK, res.Reason)})
	}
	return out
}

func opStateString(s state.OpState) string {
	switch s {
	case state.OpRunning:
		return "running"
	case state.OpDone:
		return "done"
	case state.OpFailed:
		return "failed"
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
	}
	return "unknown"
}

func opResultStatus(ok bool, reason state.OpResultReason) string {
	if ok {
		return "ok"
	}
	switch reason {
	case state.ReasonItemDenied:
		return "denied"
	case state.ReasonItemNotFound:
		return "not_found"
	case state.ReasonItemConflict:
		return "conflict"
	case state.ReasonItemSkipped:
		return "skipped"
	}
	return "failed"
}
