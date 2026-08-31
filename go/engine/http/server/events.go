// Linux only, because it serves a Linux-only engine.
//go:build linux

// The change channel: a socket over which a browser tab stays subscribed to
// the directories it is looking at.
//
// One rule shapes every frame. An invalidation names a path and carries
// nothing else: no content, no etag, no metadata. The client re-fetches, and
// that re-fetch is what applies the permission the caller holds now rather
// than the one they held when they subscribed.
//
// Permission is checked twice for the same reason. Once at subscribe, so a
// path the caller cannot read is never pinned, and again at delivery, so a
// grant revoked in between drops the event and the subscription with it.
package server

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/gofiber/contrib/websocket"
	"github.com/gofiber/fiber/v2"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/handler"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/clock"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/task"
)

// The bounds one connection is held to.
const (
	// EventDebounce is how long events for one path coalesce. A recursive copy
	// produces thousands of events for one directory and a tab needs one.
	EventDebounce = 200 * time.Millisecond

	// maxFrameBytes bounds a client frame before it is decoded. A frame is a
	// verb and a path list; anything larger is not one.
	maxFrameBytes = 64 << 10

	// maxPathsPerFrame bounds one subscribe. Each path costs a resolve, so the
	// bound is what stops one frame buying an unbounded amount of work.
	maxPathsPerFrame = 256

	// maxSubscriptions bounds what one connection may hold at once. Each
	// subscription pins a directory into the watcher's sticky set, which is a
	// kernel watch that nothing else can evict.
	//
	// No test reaches it, and the mutation removing it is absorbed: doing so
	// takes a client that subscribes to more than a thousand directories that
	// all resolve, which is a fixture costing more than the line is worth. It
	// stays because the watcher's pinned half is the one nothing reclaims, so
	// the alternative to a bound here is a bound nowhere.
	maxSubscriptions = 1024

	// pongWait is how long a peer has to answer before its connection is
	// closed. A half-open socket holds subscriptions and pins watches, so a
	// peer that stopped answering has to be noticed rather than waited on.
	pongWait = 60 * time.Second

	// pingInterval is comfortably inside pongWait, so a peer gets more than
	// one chance to answer before the deadline it is measured against.
	pingInterval = 25 * time.Second

	// writeWait bounds one write. A peer that has stopped reading must not
	// block the writer that serves it.
	writeWait = 10 * time.Second
)

// EventSource is one change, in the terms this package matches on.
//
// Declared here rather than imported: the watcher that produces these lives
// below the presentation tier, and the assembly adapts its events into this
// shape. The share is an opaque number and the directory an opaque string,
// because matching is all this package does with either.
type EventSource struct {
	// Share identifies the share the change happened in. The width is the
	// one the id actually has, so nothing here converts it back and forth.
	Share uint32
	// Dir is the share-relative directory, empty when All.
	Dir string
	// All says events were lost and the whole share is stale. What was missed
	// is exactly what is unknown, so there is nothing finer to report.
	All bool
}

// EventTarget is one resolved subscription: what a path turned out to name.
//
// The hub never parses or resolves a path itself. It hands the caller's own
// spelling to Resolve and stores what comes back, so the rules about what a
// path means stay in the one place that owns them.
type EventTarget struct {
	// Share and Dir are what a change is matched against.
	Share uint32
	Dir   string
	// Pin is handed back to Unsubscribe. It is opaque here on purpose: the
	// watcher owns what a pin is, and this package only has to return it.
	Pin any
}

// EventDeps is what the hub needs, supplied rather than reached for.
type EventDeps struct {
	// Resolve applies the caller's read permission to a path the client named,
	// and reports what it resolves to. The false return covers every reason a
	// path is not deliverable: it does not parse, it does not exist, or the
	// caller may not read it. They are one answer here because the caller is
	// told the same thing by each.
	//
	// It is the same gate every other read goes through, which is what keeps
	// this surface from becoming a second answer to the same question.
	Resolve func(user int64, path string) (EventTarget, bool)

	// Subscribe and Unsubscribe pin and release a directory in the watcher.
	// A subscription is why the folder somebody is looking at stays watched
	// under load rather than being evicted like any other recent entry.
	Subscribe   func(EventTarget)
	Unsubscribe func(EventTarget)

	Clock  clock.Clock
	Logger *slog.Logger
}

// EventHub is the broker. One per process; the route handler joins connections
// to it.
type EventHub struct {
	deps EventDeps

	mu    sync.Mutex
	conns map[*eventConn]struct{}
	// closed stops a late upgrade from joining a hub that is shutting down,
	// which would otherwise leave a connection nothing ever closes.
	closed bool
}

// NewEventHub builds the broker over a change stream and starts its fan-out.
//
// The stream is read by one goroutine and handed to each connection, which
// records it and returns. Delivery happens on the connection's own writer, so
// a slow peer costs its own connection rather than the fan-out everyone shares.
func NewEventHub(ctx context.Context, deps EventDeps, events <-chan EventSource) *EventHub {
	if deps.Clock == nil {
		deps.Clock = clock.System()
	}
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	h := &EventHub{deps: deps, conns: map[*eventConn]struct{}{}}
	task.Go(ctx, "event fan-out", func() { h.fanOut(events) })
	return h
}

// fanOut hands every change to every connection.
func (h *EventHub) fanOut(events <-chan EventSource) {
	for ev := range events {
		h.mu.Lock()
		for c := range h.conns {
			c.record(ev)
		}
		h.mu.Unlock()
	}
}

// Close drops every connection and releases what they held.
//
// Releasing is the point. Closing the socket alone leaves the watcher pinning
// a directory for a connection that no longer exists, and a pinned entry is
// the one thing the hot set never evicts on its own.
func (h *EventHub) Close() {
	h.mu.Lock()
	h.closed = true
	conns := make([]*eventConn, 0, len(h.conns))
	for c := range h.conns {
		conns = append(conns, c)
	}
	h.conns = map[*eventConn]struct{}{}
	h.mu.Unlock()

	for _, c := range conns {
		c.releaseAll()
		c.shut()
	}
}

// Connections reports how many sockets are open, for a test and for a probe.
func (h *EventHub) Connections() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.conns)
}

// EventHandler is the fiber handler for the upgrade.
//
// The credential is read from the request rather than from the socket, and it
// is read before the upgrade: the middleware chain has already run by then, so
// a request that reached here carries one. The adapter's own origin check is
// not the boundary; the chain's host and origin rules are, and they ran first.
func (h *EventHub) EventHandler(owner func(*fiber.Ctx) (int64, bool)) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if !websocket.IsWebSocketUpgrade(c) {
			// A plain GET on this route is a client that has not upgraded.
			// Saying so beats leaving the request open on a stream that will
			// never carry a frame.
			return fiber.ErrUpgradeRequired
		}
		user, ok := owner(c)
		if !ok {
			return fiber.ErrUnauthorized
		}
		// Copied out of the context before the socket outlives the request.
		// Everything fiber lends is reused once the handler returns.
		return websocket.New(func(ws *websocket.Conn) {
			h.serve(ws, user)
		})(c)
	}
}

// serve runs one connection until the peer goes away.
func (h *EventHub) serve(ws *websocket.Conn, user int64) {
	c := &eventConn{
		hub:     h,
		ws:      ws,
		user:    user,
		pending: map[string]time.Time{},
		subs:    map[string]eventSub{},
		done:    make(chan struct{}),
	}

	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		c.shut()
		return
	}
	h.conns[c] = struct{}{}
	h.mu.Unlock()

	// The writer runs alongside; the reader owns this goroutine and returns
	// when the peer stops. One writer owns the socket, which is what the
	// protocol requires: two would interleave frames.
	writerDone := make(chan struct{})
	task.Go(context.Background(), "event writer", func() {
		defer close(writerDone)
		c.writeLoop()
	})

	c.readLoop()
	close(c.done)
	<-writerDone

	h.mu.Lock()
	delete(h.conns, c)
	h.mu.Unlock()

	// Every subscription this connection held, released. The old
	// implementation removed the socket and left these pinned, so a tab that
	// closed kept a kernel watch alive for the rest of the process.
	c.releaseAll()
	c.shut()
}

// eventSub is one path this connection watches.
type eventSub struct {
	target EventTarget
}

// eventConn is one open socket.
type eventConn struct {
	hub  *EventHub
	ws   *websocket.Conn
	user int64

	// writeMu serializes writes. The protocol allows one writer, and the ping
	// ticker and the flush both write.
	writeMu sync.Mutex

	mu      sync.Mutex
	pending map[string]time.Time
	subs    map[string]eventSub

	done chan struct{}
}

// readLoop consumes client frames until the peer closes or misbehaves.
func (c *eventConn) readLoop() {
	c.ws.SetReadLimit(maxFrameBytes)
	if err := c.ws.SetReadDeadline(c.hub.deps.Clock.Now().Add(pongWait)); err != nil {
		return
	}
	c.ws.SetPongHandler(func(string) error {
		// Every answer buys the peer another window. A peer that stops
		// answering runs out and the read fails, which is what closes a
		// half-open connection rather than holding its subscriptions forever.
		return c.ws.SetReadDeadline(c.hub.deps.Clock.Now().Add(pongWait))
	})

	for {
		_, raw, err := c.ws.ReadMessage()
		if err != nil {
			return
		}
		frame, perr := handler.ParseWSFrame(raw, maxFrameBytes, maxPathsPerFrame)
		if perr != nil {
			// A frame this server cannot read is the end of the conversation.
			// Continuing would be answering a peer whose next frame is just as
			// likely to be unreadable.
			return
		}
		switch frame.Type {
		case handler.WSPing:
			if !c.send(handler.WSFrame{Type: handler.WSPong}) {
				return
			}
		case handler.WSPong:
			// The deadline moved in the pong handler above. A pong arriving as
			// a text frame rather than a control frame is the same statement.
		case handler.WSSubscribe:
			c.subscribe(frame.Paths)
		case handler.WSUnsubscribe:
			c.unsubscribe(frame.Paths)
		}
	}
}

// subscribe resolves each path with Read and pins what the caller may see.
//
// A path that does not resolve is skipped rather than refused. One unreadable
// path in a list of twenty is a tab asking about a folder it lost access to,
// not a reason to drop the other nineteen.
//
// This check is redundant against delivery, measured: removing it alone
// changes no answer, because the recheck in flush refuses the same path before
// anything is sent. Removing both is caught. It stays because the two answer
// different questions: this one decides whether to pin a directory into the
// watcher's sticky set, and pinning on behalf of a caller who may not read it
// spends a kernel watch that nothing will ever deliver from.
func (c *eventConn) subscribe(paths []string) {
	for _, raw := range paths {
		target, ok := c.hub.deps.Resolve(c.user, raw)
		if !ok {
			continue
		}

		c.mu.Lock()
		_, already := c.subs[raw]
		if !already && len(c.subs) >= maxSubscriptions {
			c.mu.Unlock()
			continue
		}
		c.subs[raw] = eventSub{target: target}
		c.mu.Unlock()

		if already {
			// Pinning twice for one path would take a second reference the
			// unsubscribe below releases only once.
			continue
		}
		c.hub.deps.Subscribe(target)
	}
}

// unsubscribe releases the pin and forgets the path.
//
// It works from what this connection recorded rather than resolving again: a
// caller who has since lost the permission still has to be able to release
// what they pinned, and a resolve would refuse them.
func (c *eventConn) unsubscribe(paths []string) {
	for _, raw := range paths {
		c.mu.Lock()
		sub, held := c.subs[raw]
		delete(c.subs, raw)
		delete(c.pending, raw)
		c.mu.Unlock()

		if held {
			c.hub.deps.Unsubscribe(sub.target)
		}
	}
}

// releaseAll drops every pin this connection took.
func (c *eventConn) releaseAll() {
	c.mu.Lock()
	subs := make([]eventSub, 0, len(c.subs))
	for _, sub := range c.subs {
		subs = append(subs, sub)
	}
	c.subs = map[string]eventSub{}
	c.pending = map[string]time.Time{}
	c.mu.Unlock()

	for _, sub := range subs {
		c.hub.deps.Unsubscribe(sub.target)
	}
}

// record notes a change against the paths this connection asked about.
//
// Only a path the connection named is recorded. A change in a directory
// nobody is looking at costs nothing, and no client is told about a path it
// never subscribed to.
func (c *eventConn) record(ev EventSource) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for raw, sub := range c.subs {
		if sub.target.Share != ev.Share {
			continue
		}
		if !ev.All && sub.target.Dir != ev.Dir {
			continue
		}
		if _, seen := c.pending[raw]; seen {
			// Already inside a debounce window. The window is what turns a
			// burst into one frame.
			continue
		}
		c.pending[raw] = c.hub.deps.Clock.Now()
	}
}

// writeLoop delivers debounced changes and keeps the connection proven alive.
func (c *eventConn) writeLoop() {
	flush := time.NewTicker(EventDebounce)
	defer flush.Stop()
	ping := time.NewTicker(pingInterval)
	defer ping.Stop()

	for {
		select {
		case <-c.done:
			return
		case <-flush.C:
			if !c.flush() {
				return
			}
		case <-ping.C:
			if !c.ping() {
				return
			}
		}
	}
}

// flush sends one frame per path whose window has elapsed.
//
// Every path is rechecked here rather than only at subscribe. A grant revoked
// in between must not deliver the next event, and checking once at subscribe
// is exactly what makes a revoked grant keep serving.
func (c *eventConn) flush() bool {
	for _, raw := range c.due() {
		if _, ok := c.hub.deps.Resolve(c.user, raw); !ok {
			// The caller can no longer read it. The event is dropped and the
			// subscription goes with it, pin included.
			c.unsubscribe([]string{raw})
			continue
		}
		if !c.send(handler.InvalidationFrame(raw)) {
			return false
		}
	}
	return true
}

// due takes the paths whose debounce window has elapsed.
//
// Separated from the delivery so the coalescing rule can be tested without a
// socket behind it.
func (c *eventConn) due() []string {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.hub.deps.Clock.Now()
	var ready []string
	for raw, since := range c.pending {
		if now.Sub(since) >= EventDebounce {
			ready = append(ready, raw)
			delete(c.pending, raw)
		}
	}
	return ready
}

// send writes one frame, reporting whether the socket is still usable.
func (c *eventConn) send(frame handler.WSFrame) bool {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	if err := c.ws.SetWriteDeadline(c.hub.deps.Clock.Now().Add(writeWait)); err != nil {
		return false
	}
	return c.ws.WriteJSON(frame) == nil
}

// ping asks the peer to prove it is still there.
func (c *eventConn) ping() bool {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	if err := c.ws.SetWriteDeadline(c.hub.deps.Clock.Now().Add(writeWait)); err != nil {
		return false
	}
	return c.ws.WriteMessage(websocket.PingMessage, nil) == nil
}

// shut breaks the socket. The peer is going away either way, so a failure to
// close has nowhere to be reported.
func (c *eventConn) shut() {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_ = c.ws.Close() //nolint:errcheck // see above.
}
