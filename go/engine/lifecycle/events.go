//go:build linux

// Wiring the change channel: a watcher below, a broker above, and the one
// permission gate between them.
//
// The two halves cannot see each other. The watcher is a sensor that reports
// directories and knows nothing about accounts; the broker serves sockets and
// may not name a filesystem type. Assembly is where a path the client wrote
// becomes a share and a directory, and where the account's own permission is
// applied to that translation.
package lifecycle

import (
	"context"
	"github.com/gofiber/fiber/v2"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/apierr"
	"github.com/heavycaffeiner/stowcloud/go/engine/http/server"
	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/task"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/settings/runtimecfg"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/watch"
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
	events := make(chan watch.InvalEvent, eventQueue)

	watcher, err := watch.Start(ctx, watch.Config{
		Backend:       cfg.Backend,
		HotSetMax:     cfg.HotSetMax,
		FullThreshold: cfg.FullThreshold,
	}, e.clock, events)
	if err != nil {
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

	e.events = server.NewEventHub(ctx, server.EventDeps{
		Resolve:     e.resolveForEvents,
		Subscribe:   e.pinForEvents,
		Unsubscribe: e.unpinForEvents,
		Clock:       e.clock,
		Logger:      e.logger,
	}, e.adaptEvents(ctx, events))
}

// watchShare tells the watcher where a share lives on disk.
//
// A broken share is skipped: its backing did not open, so there is no
// directory to watch. It is picked up whenever the disk comes back and the
// share is re-registered.
//
// Safe to call with no watcher, which is a deployment whose kernel refused an
// inotify descriptor. Every caller would otherwise repeat the same check.
func (e *Engine) watchShare(def core.ShareDef) {
	if e.watcher == nil || def.BrokenReason != "" {
		return
	}
	e.watcher.AddShare(def.ID, def.Host, false)
}

// adaptEvents translates the watcher's events into the broker's shape.
//
// A translation rather than one shared type, because the two packages sit on
// opposite sides of a layer boundary that exists for a reason: the broker must
// not name a filesystem type, and the watcher must not know what a socket is.
//
// The goroutine ends when the source channel closes, which is what the
// watcher's own shutdown does.
func (e *Engine) adaptEvents(ctx context.Context, in <-chan watch.InvalEvent) <-chan server.EventSource {
	out := make(chan server.EventSource, eventQueue)
	task.Go(ctx, "event translation", func() {
		defer close(out)
		for ev := range in {
			// Offered before the broker sees it: an update that lands the
			// index sooner than the socket fan-out costs nothing, whereas the
			// reverse would leave the index a step behind every client that
			// requeries off the push it just received.
			e.offerToSearchUpdater(uint32(ev.Share), ev.Dir, ev.All)
			select {
			case out <- server.EventSource{
				Share: uint32(ev.Share),
				Dir:   ev.Dir,
				All:   ev.All,
			}:
			case <-ctx.Done():
				return
			}
		}
	})
	return out
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
	e.watcher.Subscribe(vfs.ShareID(t.Share), path)
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
	e.watcher.Unsubscribe(vfs.ShareID(t.Share), path)
}

// watchSettings is what the watcher needs out of the settings document.
type watchSettings struct {
	Backend       watch.Backend
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
		Backend:       watch.BackendInotify,
		HotSetMax:     values.WatchHotSetMax,
		FullThreshold: values.WatchFullThreshold,
	}
}

// eventsSocket is the handler bound to the change channel.
//
// A deployment with no watcher answers that the surface is absent rather than
// upgrading a socket that would never carry a frame. The client falls back to
// asking again, which is what it does between frames anyway.
func (e *Engine) eventsSocket() fiber.Handler {
	if e.events == nil {
		return func(c *fiber.Ctx) error {
			return refuse(c, apierr.Classified{
				Class: apierr.SubsystemUnavailable, Key: "events.unavailable",
			})
		}
	}
	return e.events.EventHandler(func(c *fiber.Ctx) (int64, bool) {
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
