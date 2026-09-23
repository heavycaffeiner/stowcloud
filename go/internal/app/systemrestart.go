//go:build linux

// Requesting an externally coordinated server restart.
//
// The engine reports product restart intent. The application host owns
// admission, drain, cleanup, and any deployment-specific restart policy.
package app

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/heavycaffeiner/stowcloud/go/internal/feature/admin/settings/runtimecfg"
	"github.com/heavycaffeiner/stowcloud/go/internal/kit/task"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/apierr"
)

// restartGrace is how long the answer has to reach the client before the
// process replaces itself. The response is already written; this covers the
// flush and the socket, not the request.
const restartGrace = 250 * time.Millisecond

// restartRequest is what the endpoint answers with.
//
// The counts are always present rather than omitted when zero: a client that
// has to tell "nothing running" from "the server did not say" gets two
// readings of the same absent field.
type restartResult struct {
	Restarting    bool `json:"restarting"`
	ActiveUploads int  `json:"active_uploads"`
	ActiveJobs    int  `json:"active_jobs"`
}

// systemRestart asks the application host to coordinate a restart after the
// accepted response has had time to reach the client.
func (e *Engine) systemRestart(c *gin.Context) {
	if _, ok := e.admin(c); !ok {
		return
	}
	if e.wouldLoosenHardening(c) {
		refuse(c, apierr.Classified{Class: apierr.Conflict, Key: "system.hardening_cannot_loosen"})
		return
	}
	uploads, jobs := e.activeWork(c)
	writeJSON(c, http.StatusAccepted, restartResult{Restarting: true, ActiveUploads: uploads, ActiveJobs: jobs})
	task.Go(context.WithoutCancel(c.Request.Context()), "restart", e.performRestart)
}

// wouldLoosenHardening reports whether the stored policy is weaker than the one
// this process installed.
func (e *Engine) wouldLoosenHardening(c *gin.Context) bool {
	values := runtimecfg.Load(c.Request.Context(), e.State, runtimecfg.Defaults(), e.logger)
	// The policy values are ordered by strictness, strictest first, so a
	// higher one is a weaker sandbox.
	return values.Hardening > e.hardening
}

// performRestart waits for the answer to land, then hands off to whatever the
// process wired to replace its image.
func (e *Engine) performRestart() {
	// Long enough for the answer to reach the client. A restart that dropped
	// the socket first is indistinguishable from a crash.
	time.Sleep(restartGrace)

	e.restartMu.Lock()
	swap := e.onRestart
	e.restartMu.Unlock()
	if swap == nil {
		e.logger.Error("a restart was asked for with nothing wired to perform it")
		return
	}
	swap()
}

// OnRestart registers the application-host callback that receives product
// restart intent. The engine never replaces the process image itself.
func (e *Engine) OnRestart(fn func()) {
	e.restartMu.Lock()
	e.onRestart = fn
	e.restartMu.Unlock()
}
