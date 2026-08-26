// Linux only: it depends on packages that are Linux only.
//go:build linux

package server

import (
	"context"
	"log/slog"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/internal/core"
	"github.com/heavycaffeiner/stowcloud/go/internal/httpapi/handler"
	"github.com/heavycaffeiner/stowcloud/go/internal/task"
)

// shareProbeInterval is how often every share root is re-checked.
//
// Thirty seconds is chosen against what the probe costs and what it is
// racing. It is one statx per share, so a deployment with fifty shares does
// fifty syscalls a minute; what it is racing is a person noticing that a
// folder stopped working, and half a minute is well inside the time it takes
// to open the screen and read it.
const shareProbeInterval = 30 * time.Second

// WatchShares re-probes every share root on a schedule, flipping a share
// between working and broken.
//
// It exists because a share root is an open descriptor, and a descriptor
// survives the directory it names. Unmount the disk under a share and every
// request against it fails, one at a time, with nothing anywhere saying the
// share is the problem: the descriptor is still there, so nothing re-checks
// it. Watching is what turns that into a state somebody can see.
//
// Both directions. A disk that came back has to start working without anybody
// pressing anything, because the ordinary case is a network mount that
// reconnected while nobody was looking.
func WatchShares(ctx context.Context, c *core.Core, health *handler.HealthState, log *slog.Logger) {
	task.Go(ctx, "share probe", func() {
		t := time.NewTicker(shareProbeInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				probeOnce(ctx, c, health, log)
			}
		}
	})
}

// probeOnce runs one pass and reports only what moved.
//
// Only what moved, because a probe that logged its steady state would put a
// line a minute in the log and bury the one that matters.
func probeOnce(ctx context.Context, c *core.Core, health *handler.HealthState, log *slog.Logger) {
	broke, healed := c.ProbeShares(ctx)
	for _, def := range broke {
		log.Error("a share's folder is no longer available; it is listed as broken",
			"share", def.Name, "path", def.Host, "reason", def.BrokenReason)
		health.Degrade(handler.ReasonShareRejected, def.Name+":"+def.BrokenReason)
	}
	for _, def := range healed {
		log.Info("a share's folder came back and it is being served again",
			"share", def.Name, "path", def.Host)
		// Every detail this share was degraded under, because the reason it
		// broke with is not necessarily the one it is carrying now: a mount
		// that went missing and came back on a filesystem this server refuses
		// would have been recorded under two different kinds.
		health.ResolveShare(def.Name)
	}
}
