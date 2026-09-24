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
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/heavycaffeiner/stowcloud/go/internal/feature/admin/settings/runtimecfg"
	"github.com/heavycaffeiner/stowcloud/go/internal/feature/files"
	"github.com/heavycaffeiner/stowcloud/go/internal/feature/shares/acl"
	task "github.com/heavycaffeiner/stowcloud/go/internal/platform/concurrency"
	"github.com/heavycaffeiner/stowcloud/go/internal/platform/storage/vfs"
	runtimeevents "github.com/heavycaffeiner/stowcloud/go/internal/runtime/events"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/apierr"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/server"
	storage "github.com/stowcloud/storage"
	storagewatch "github.com/stowcloud/storage/watch"
)

// eventQueue is how many invalidations may wait for the broker.
//
// Bounded rather than unbounded, because the producer is the kernel and the
// consumer is a fan-out over sockets. A full queue escalates to a whole-share
// invalidation, which is a correct answer that costs the client one extra
// listing, where an unbounded queue would grow until the process died.
const eventQueue = 1024

// startEvents builds the watcher and the broker over it.
//
// Absence is a degradation rather than a failure. A deployment whose kernel
// refuses an inotify descriptor still serves every request; what it loses is
// the push, and clients fall back to asking again.
func (e *Engine) startEvents(ctx context.Context, cfg watchSettings) {
	events := make(chan storagewatch.Event, eventQueue)

	watcher, err := storagewatch.Start(ctx, storagewatch.Config{
		Backend:        storagewatch.BackendInotify,
		HotSetMax:      cfg.HotSetMax,
		FullThreshold:  cfg.FullThreshold,
		OnCoverageLost: e.searchRuntime.MarkIncomplete,
	}, e.clock, events)
	if err != nil {
		e.searchRuntime.MarkIncomplete()
		e.logger.Warn("change notifications are unavailable; clients fall back to polling",
			"error", err)
		return
	}
	e.watcher = watcher

	// Every share the registry holds, so a change under one is seen without
	// anybody subscribing first.
	for _, def := range e.Core.Shares() {
		e.watchShare(def)
	}

	// Keep the runtime translation synchronous with the app-side broker queue;
	// the watcher input and transport output are the only bounded queues.
	translated := runtimeevents.Adapt(ctx, events, 0,
		func(ctx context.Context, ev runtimeevents.Event) {
			// The cache first. A change this server did not make still moved
			// the bytes, and nothing else in the process knows: without this
			// a folder written over the file-sharing protocol kept answering
			// its old rollup, so the interface reported a folder full of
			// files as empty and a sync client saw an unchanged directory
			// token. Listings are read from disk and were never affected,
			// which is what made the staleness look like a display bug.
			e.invalidateCache(ctx, ev)
			// Offered before the broker sees it: an update that lands the
			// index sooner than the socket fan-out costs nothing, whereas the
			// reverse would leave the index a step behind every client that
			// requeries off the push it just received.
			e.searchRuntime.Offer(ev.Share, ev.Dir, ev.All)
		},
	)
	e.events = server.NewEventHub(ctx, server.EventDeps{
		Resolve:     e.resolveForEvents,
		Subscribe:   e.pinForEvents,
		Unsubscribe: e.unpinForEvents,
		Clock:       e.clock,
		Logger:      e.logger,
	}, eventSources(ctx, translated))
}

// watchShare tells the watcher where a share lives on disk.
//
// A broken share is skipped: its backing did not open, so there is no
// directory to watch. It is picked up whenever the disk comes back and the
// share is re-registered. A non-local share is skipped for the same
// outcome by a different route: it has no host path, since its bytes do
// not live in this server's filesystem, so there is nothing here for
// inotify to watch either.
//
// Safe to call with no watcher, which is a deployment whose kernel refused an
func (e *Engine) watchShare(def core.ShareDef) {
	// Registering the existing shares at startup does not itself create a
	// coverage gap. openSearchIndex already scheduled the restart rebuild.
	if e.watcher == nil || def.BrokenReason != "" || def.Host == "" {
		return
	}
	e.watcher.AddOwner(storagewatch.OwnerID(strconv.FormatUint(uint64(def.ID), 10)), def.Host, false)
}

func (e *Engine) unwatchShare(def core.ShareDef) {
	if e.watcher == nil {
		return
	}
	e.watcher.RemoveOwner(storagewatch.OwnerID(strconv.FormatUint(uint64(def.ID), 10)))
}

// eventSources maps the runtime stream to the transport's opaque event shape.
// The mapping is deliberately kept in app assembly: runtime/events must not
// depend on the HTTP transport, and EventHub remains the transport broker.
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
		// The kernel reported a name this server's rules refuse, which is
		// nothing to mark: no cached row is keyed by a path that cannot be
		// resolved.
		return
	}
	e.Core.InvalidateDir(ctx, share, dir)
}

// resolveForEvents applies the caller's read permission to a path they named.
//
// One answer for every refusal. A path that does not parse, one that names no
// share, and one the caller may not read are the same statement to a client:
// you are not being told about this. Distinguishing them here would let a
// subscribe attempt report whether a directory exists.
func (e *Engine) resolveForEvents(user int64, path string) (server.EventTarget, bool) {
	vp, err := vfs.ParseVpath(path)
	if err != nil {
		return server.EventTarget{}, false
	}
	resolved, rerr := e.Core.Resolve(core.UserID(user), vp, acl.Read)
	if rerr != nil {
		return server.EventTarget{}, false
	}
	return server.EventTarget{
		Share: uint32(resolved.Share()),
		Dir:   resolved.Path().String(),
		// The resolved path itself, so releasing needs no second resolve. A
		// caller who has lost the permission still has to be able to release
		// what they pinned, and a resolve would refuse them.
		Pin: resolved.Path(),
	}, true
}

// pinForEvents holds a directory in the watcher's sticky set.
//
// The pin is what keeps the folder somebody is looking at watched. Without it
// that directory is exactly the one an LRU evicts under load, so notifications
// stop for the folder in front of them while everything else keeps working.
func (e *Engine) pinForEvents(t server.EventTarget) {
	if e.watcher == nil {
		return
	}
	path, ok := t.Pin.(vfs.SafePath)
	if !ok {
		return
	}
	publicPath, err := storage.ParsePath(path.String())
	if err != nil {
		return
	}
	e.watcher.Subscribe(storagewatch.OwnerID(strconv.FormatUint(uint64(t.Share), 10)), publicPath)
}

// unpinForEvents releases one.
func (e *Engine) unpinForEvents(t server.EventTarget) {
	if e.watcher == nil {
		return
	}
	path, ok := t.Pin.(vfs.SafePath)
	if !ok {
		return
	}
	publicPath, err := storage.ParsePath(path.String())
	if err != nil {
		return
	}
	e.watcher.Unsubscribe(storagewatch.OwnerID(strconv.FormatUint(uint64(t.Share), 10)), publicPath)
}

// watchSettings is what the watcher needs out of the settings document.
type watchSettings struct {
	Backend       storagewatch.Backend
	HotSetMax     int
	FullThreshold int
}

// watchSettingsOf reads the watcher's section of the stored document.
//
// The backend is not read from settings and not offered as a choice: this
// build implements one transport, and a setting whose only valid value is the
// default is a knob that can only be set wrong. The document names the bounds
// an operator does adjust.
func watchSettingsOf(ctx context.Context, e *Engine) watchSettings {
	values := runtimecfg.Load(ctx, e.State, runtimecfg.Defaults(), e.logger)
	return watchSettings{
		Backend:       storagewatch.BackendInotify,
		HotSetMax:     values.WatchHotSetMax,
		FullThreshold: values.WatchFullThreshold,
	}
}

// eventsSocket is the handler bound to the change channel.
//
// A deployment with no watcher answers that the surface is absent rather than
// upgrading a socket that would never carry a frame. The client falls back to
// asking again, which is what it does between frames anyway.
func (e *Engine) eventsSocket() gin.HandlerFunc {
	if e.events == nil {
		return func(c *gin.Context) {
			refuse(c, apierr.Classified{
				Class: apierr.SubsystemUnavailable, Key: "events.unavailable",
			})
		}
	}
	return e.events.EventHandler(func(c *gin.Context) (int64, bool) {
		owner, ok := ownerOf(c)
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
	return e.watcher.Stats().Pinned
}
