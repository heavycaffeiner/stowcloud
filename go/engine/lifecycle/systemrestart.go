//go:build linux

// Restarting the server from the settings screen.
//
// Some settings only reach a running process at the next start. Without this
// an operator has to stop and start the container by hand, which in a
// deployment with no restart policy is a decision they may not get to undo.
package lifecycle

import (
	"context"
	"os"
	"syscall"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/apierr"
	"github.com/heavycaffeiner/stowcloud/go/engine/infra/jail"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/task"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/settings/runtimecfg"
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

// systemRestart replaces this process with a fresh image of the same binary.
//
// Exec rather than exit: the engine is its container's only process, so an
// exit takes the container down and whether it returns depends on a restart
// policy the operator may not have set. Exec keeps the pid, so nothing outside
// notices.
func (e *Engine) systemRestart(c *fiber.Ctx) error {
	// Administrator, not merely authenticated. Taking the server down is a
	// denial of service in the hands of any account that can reach it.
	if _, ok, written := e.admin(c); !ok {
		return written
	}

	// A sandbox this process installed cannot be taken off by the image that
	// replaces it: Landlock is stackable and narrowing-only, and a seccomp
	// filter has no removal syscall. Both survive execve. So a restart meant
	// to loosen hardening would come back reporting the looser setting while
	// the old domain still governed it.
	//
	// Refused rather than exited. Exiting would apply the change, and would
	// also leave the deployment down for as long as it takes somebody to
	// notice, which is not a trade to make on an operator's behalf without
	// asking.
	if e.wouldLoosenHardening(c) {
		return refuse(c, apierr.Classified{
			Class: apierr.Conflict,
			Key:   "system.hardening_cannot_loosen",
		})
	}

	uploads, jobs := e.activeWork(c)
	if err := writeJSON(c, fiber.StatusAccepted, restartResult{
		Restarting:    true,
		ActiveUploads: uploads,
		ActiveJobs:    jobs,
	}); err != nil {
		return err
	}

	// After the answer, on its own goroutine: the handler has to return for
	// the response to be written at all. Detached from the request's context,
	// which is cancelled the moment that response is done.
	task.Go(context.WithoutCancel(c.UserContext()), "restart", e.performRestart)
	return nil
}

// wouldLoosenHardening reports whether the stored policy is weaker than the one
// this process installed.
func (e *Engine) wouldLoosenHardening(c *fiber.Ctx) bool {
	values := runtimecfg.Load(c.UserContext(), e.State, runtimecfg.Defaults(), e.logger)
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

// OnRestart registers what performs the process replacement.
//
// The listener and the process image belong to the caller that built them, not
// to this engine: it is mounted on a server it did not create and outlives no
// part of it. So the engine decides that a restart should happen and the
// process decides how.
func (e *Engine) OnRestart(fn func()) {
	e.restartMu.Lock()
	e.onRestart = fn
	e.restartMu.Unlock()
}

// ExecSelf replaces the running process with a fresh image of the same binary.
//
// Exported because the composition root wires it: the engine reports that a
// restart is wanted, and this is what the process does about it.
//
// It does not return on success. A return is a failure, and the caller still
// has a running process to keep serving with.
func ExecSelf() error {
	// Read from the kernel rather than argv[0], which a caller controls and
	// which need not name this binary at all.
	self, err := os.Readlink("/proc/self/exe")
	if err != nil {
		return err
	}

	// The hardening re-exec marks its own image so the sandbox is applied
	// exactly once. Left set, the new image would skip the mount scan and the
	// restrict call and come up believing it had already hardened.
	env := make([]string, 0, len(os.Environ()))
	marker := jail.ReexecMarker() + "="
	for _, kv := range os.Environ() {
		if len(kv) >= len(marker) && kv[:len(marker)] == marker {
			continue
		}
		env = append(env, kv)
	}

	return syscall.Exec(self, os.Args, env) //nolint:gosec // G204 reads a variable path: it is /proc/self/exe, this running image, not anything a caller supplies.
}
