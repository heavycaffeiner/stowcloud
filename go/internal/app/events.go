//go:build linux

// Wiring the change channel: a watcher below, a broker above, and the one
// permission gate between them.
//
// The two halves cannot see each other. The watcher is a sensor that reports
// directories and knows nothing about accounts; the broker serves sockets and
// may not name a filesystem type. Assembly is where a path the client wrote
// becomes a share and a directory, and where the account's own permission is
// applied to that translation.
package app

import (
	"context"

	"github.com/gin-gonic/gin"
	"github.com/heavycaffeiner/stowcloud/go/internal/feature/admin/settings/runtimecfg"
	core "github.com/heavycaffeiner/stowcloud/go/internal/feature/files"
	"github.com/heavycaffeiner/stowcloud/go/internal/feature/shares/acl"
	task "github.com/heavycaffeiner/stowcloud/go/internal/platform/concurrency"
	"github.com/heavycaffeiner/stowcloud/go/internal/platform/storage/vfs"
	runtimeevents "github.com/heavycaffeiner/stowcloud/go/internal/runtime/events"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/apierr"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/handler"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/server"
	storagewatch "github.com/stowcloud/storage/watch"
)

const eventQueue = 1024

// startEvents builds the watcher and the broker over it.
//
// Absence is a degradation rather than a failure. A deployment whose kernel
// refuses an inotify descriptor still serves every request; what it loses is
// the push, and clients fall back to asking again.
func (e *Engine) startEvents(ctx context.Context, cfg watchSettings) {
	defs := e.Core.Shares()
	shares := make([]runtimeevents.Share, 0, len(defs))
	for _, def := range defs {
		shares = append(shares, runtimeevents.Share{
			ID: uint32(def.ID), Host: def.Host, Broken: def.BrokenReason != "",
		})
	}
	watcher, err := runtimeevents.Start(ctx, runtimeevents.Config{
		Backend:        cfg.Backend,
		HotSetMax:      cfg.HotSetMax,
		FullThreshold:  cfg.FullThreshold,
		Clock:          e.clock,
		OnCoverageLost: e.searchRuntime.MarkIncomplete,
		OnEvent: func(ctx context.Context, ev runtimeevents.Event) {
			e.invalidateCache(ctx, ev)
			e.searchRuntime.Offer(ev.Share, ev.Dir, ev.All)
		},
	}, shares)
	if err != nil {
		e.searchRuntime.MarkIncomplete()
		e.logger.Warn("change notifications are unavailable; clients fall back to polling",
			"error", err)
		return
	}
	e.watcher = watcher
	e.events = server.NewEventHub(ctx, server.EventDeps{
		Resolve:     e.resolveForEvents,
		Subscribe:   e.pinForEvents,
		Unsubscribe: e.unpinForEvents,
		Clock:       e.clock,
		Logger:      e.logger,
	}, eventSources(ctx, watcher.Events()))
}

// watchShare tells the watcher where a share lives on disk.
func (e *Engine) watchShare(def core.ShareDef) {
	if e.watcher == nil {
		return
	}
	e.watcher.WatchShare(runtimeevents.Share{
		ID: uint32(def.ID), Host: def.Host, Broken: def.BrokenReason != "",
	})
}

func (e *Engine) unwatchShare(def core.ShareDef) {
	if e.watcher == nil {
		return
	}
	e.watcher.UnwatchShare(uint32(def.ID))
}

// eventSources maps the runtime stream to the transport's opaque event shape.
func eventSources(ctx context.Context, in <-chan runtimeevents.Event) <-chan server.EventSource {
	out := make(chan server.EventSource, eventQueue)
	task.Go(ctx, "event source mapping", func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-in:
				if !ok {
					return
				}
				select {
				case out <- server.EventSource{Share: ev.Share, Dir: ev.Dir, All: ev.All}:
				case <-ctx.Done():
					return
				}
			}
		}
	})
	return out
}

// invalidateCache clears the cached rollups one watcher event covers.
//
// A dropped batch names nothing, so the whole share's generation advances,
// which is one row however much was missed. An event that does name a
// directory marks that directory and its ancestors, leaving the rest of the
// share's cache alone.
func (e *Engine) invalidateCache(ctx context.Context, ev runtimeevents.Event) {
	share := core.ShareID(ev.Share)
	if ev.All {
		if err := e.Core.InvalidateShare(ctx, share); err != nil {
			e.logger.Warn("a share could not be invalidated after dropped events; folder sizes may read stale",
				"share", uint64(share), "error", err)
		}
		return
	}
	dir, err := vfs.ParseSafePath(ev.Dir)
	if err != nil {
		return
	}
	e.Core.InvalidateDir(ctx, share, dir)
}

// resolveForEvents applies the caller's read permission to a path they named.
func (e *Engine) resolveForEvents(user int64, path string) (server.EventTarget, bool) {
	vp, err := vfs.ParseVpath(path)
	if err != nil {
		return server.EventTarget{}, false
	}
	resolved, rerr := e.Core.Resolve(core.UserID(user), vp, acl.Read)
	if rerr != nil {
		return server.EventTarget{}, false
	}
	return server.EventTarget{Share: uint32(resolved.Share()), Dir: resolved.Path().String(), Pin: resolved.Path()}, true
}

func (e *Engine) pinForEvents(t server.EventTarget) {
	if e.watcher == nil {
		return
	}
	path, ok := t.Pin.(vfs.SafePath)
	if !ok {
		return
	}
	e.watcher.Subscribe(t.Share, path.String())
}

func (e *Engine) unpinForEvents(t server.EventTarget) {
	if e.watcher == nil {
		return
	}
	path, ok := t.Pin.(vfs.SafePath)
	if !ok {
		return
	}
	e.watcher.Unsubscribe(t.Share, path.String())
}

type watchSettings struct {
	Backend       storagewatch.Backend
	HotSetMax     int
	FullThreshold int
}

func watchSettingsOf(ctx context.Context, e *Engine) watchSettings {
	values := runtimecfg.Load(ctx, e.State, runtimecfg.Defaults(), e.logger)
	return watchSettings{Backend: storagewatch.BackendInotify, HotSetMax: values.WatchHotSetMax, FullThreshold: values.WatchFullThreshold}
}

// eventsSocket is the handler bound to the change channel.
//
// A deployment with no watcher answers that the surface is absent rather than
// upgrading a socket that would never carry a frame. The client falls back to
// asking again, which is what it does between frames anyway.
func (e *Engine) eventsSocket() gin.HandlerFunc {
	if e.events == nil {
		return func(c *gin.Context) {
			handler.Refuse(c, apierr.Classified{
				Class: apierr.SubsystemUnavailable, Key: "events.unavailable",
			})
		}
	}
	return e.events.EventHandler(func(c *gin.Context) (int64, bool) {
		owner, ok := handler.Owner(c)
		return int64(owner), ok
	})
}

// closeEvents drops every socket and stops the watcher.
//
// The hub goes first. Closing the watcher first would leave connections
// holding pins against a watcher that is already gone, and the release would
// then run against a closed descriptor.
func (e *Engine) closeEvents() {
	if e.events != nil {
		e.events.Close()
		e.events = nil
	}
	if e.watcher != nil {
		if err := e.watcher.Close(); err != nil {
			e.logger.Warn("the change watcher did not close cleanly", "error", err)
		}
		e.watcher = nil
	}
}

// PinnedDirectoriesForTest reports how many directories a subscriber holds
// pinned in the watcher.
//
// Exported for a test because the property has no other observer. A pin sits
// in the half nothing evicts, so a disconnect that dropped the socket and left
// its pins behind looks identical from outside while costing a kernel watch
// per closed tab. The registered count does not show it: releasing a pin moves
// the key to the evictable half rather than unregistering it.
//
// Zero when this deployment has no watcher.
func (e *Engine) PinnedDirectoriesForTest() int {
	if e.watcher == nil {
		return 0
	}
	return e.watcher.Pinned()
}
