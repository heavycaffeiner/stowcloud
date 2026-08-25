// Linux only: it depends on packages that are Linux only.
//go:build linux

package handler

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/internal/search/service"
	"github.com/heavycaffeiner/stowcloud/go/internal/store/state"
	"github.com/heavycaffeiner/stowcloud/go/internal/task"
)

// Building the name index.
//
// Always a job, never an inline answer: a build walks every share by
// definition, so there is no small case to answer directly the way a copy of
// one file has one. The response is the job id and the client polls it.

// AdminIndexBuild answers POST /api/admin/index/build.
func AdminIndexBuild(d Deps) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		uid, err := requireAdmin(r, d.Auth)
		if err != nil {
			return err
		}
		if d.Search == nil {
			return notImplemented("search.index_build_unavailable")
		}

		// No item list: a build walks every share, so there is no set of paths
		// the request named and nothing for an interrupted one to hand back.
		id, cerr := d.State.CreateOp(r.Context(), int64(uid), state.OpIndexBuild, 0, d.Clock.Nanos(), nil)
		if cerr != nil {
			return cerr
		}

		// The request's own context ends when the response is written, and
		// this outlives it by design: the client is told the job id and comes
		// back for the result.
		ctx := context.WithoutCancel(r.Context())
		sources := d.Core.ScanSources()

		task.Go(ctx, "index build", func() {
			// The gate is read from the operation row, so a cancel request
			// reaches a build that is already running rather than only
			// stopping one that has not started.
			gate := func() bool {
				op, _, gerr := d.State.GetOp(ctx, id)
				if gerr != nil {
					// A row that cannot be read is not a reason to keep
					// walking every share: the build stops and what it wrote
					// stays, which a query falls back around.
					return false
				}
				return !op.Cancellation
			}

			startedNs := d.Clock.Nanos()
			progress, berr := d.Search.Build(ctx, sources, gate, func(p service.BuildProgress) {
				if perr := d.State.SetOpProgress(ctx, id, p.Files, ""); perr != nil {
					d.Log.Warn("the index build's progress could not be recorded", "error", perr)
				}
			})

			now := d.Clock.Nanos()
			if berr != nil {
				d.Log.Warn("the index build failed", "error", berr, "files", progress.Files)
				if ferr := d.State.FinishOp(ctx, id, state.OpFailed, progress.Files, berr.Error(), now, nil); ferr != nil {
					d.Log.Warn("the index build's failure could not be recorded", "error", ferr)
				}
				return
			}

			// A build that stopped at its bound is done, and says which:
			// a query for what it did not reach falls back to a walk, so the
			// index being short of the corpus is a slower answer rather than
			// a wrong one.
			message := ""
			if progress.Partial {
				message = "the corpus is larger than one build covers; a query beyond it falls back to a walk"
			}
			// The rate this build actually got, stored so the next estimate is
			// derived from this corpus on this disk. The estimate used to come
			// from a compiled-in constant nobody had ever timed.
			//
			// A build too short to time is not recorded: dividing by a fraction
			// of a second produces a rate no later build will match.
			elapsedNs := now - startedNs
			if elapsedNs >= int64(time.Second) && progress.Files > 0 {
				rate := uint64(progress.Files) * uint64(time.Second) / uint64(elapsedNs) //nolint:gosec // both are positive by the guard directly above.
				if rerr := d.State.SetIndexBuildRate(ctx, rate); rerr != nil {
					d.Log.Warn("the index build's rate could not be recorded", "error", rerr)
				}
			}

			d.Log.Info("built the name index", "files", progress.Files, "dirs", progress.Dirs, "partial", progress.Partial)
			if ferr := d.State.FinishOp(ctx, id, state.OpDone, progress.Files, message, now, nil); ferr != nil {
				d.Log.Warn("the index build's completion could not be recorded", "error", ferr)
			}
		})

		// The id is a string, like every other job id this surface hands out.
		// A number here reached the client as one and the tray polled a path
		// built from it that never matched.
		return writeJSON(w, http.StatusAccepted, map[string]any{"job": strconv.FormatInt(id, 10)})
	})
}
