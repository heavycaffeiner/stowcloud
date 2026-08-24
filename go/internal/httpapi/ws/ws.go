// Linux only: it names types that are openat2 handles underneath.
//go:build linux

// Package ws is the change channel: a WebSocket over which a browser tab stays
// subscribed to the directories it is looking at.
//
// The channel is bidirectional and stateful. A tab sends sub and unsub
// frames carrying lists of paths, and a READ check is applied when it
// subscribes, so subscribing to a path the caller cannot read is refused
// rather than pinned. A 200 ms debounce coalesces events per path, and
// subscribing pins the directory into the watcher's sticky set while
// unsubscribing releases it: the subscription is why the folder a user is
// looking at stays watched.
package ws

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/heavycaffeiner/stowcloud/go/internal/acl"
	"github.com/heavycaffeiner/stowcloud/go/internal/clock"
	"github.com/heavycaffeiner/stowcloud/go/internal/core"
	"github.com/heavycaffeiner/stowcloud/go/internal/task"
	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
	"github.com/heavycaffeiner/stowcloud/go/internal/watch"
)

// debounce is how long a burst of events for one path is held before one
// summary is sent. A recursive copy produces thousands of events for one
// directory and the tab needs one.
const debounce = 200 * time.Millisecond

// Frame is a client-to-server message: subscribe, unsubscribe, or a keepalive.
//
// The discriminator is "t" and the values are the ones the client already
// sends. Two spellings of one wire is one implementation talking to itself.
type Frame struct {
	T     string   `json:"t"`
	Paths []string `json:"paths"`
}

// The client-to-server frame kinds.
const (
	frameSub   = "sub"
	frameUnsub = "unsub"
	framePing  = "ping"
)

// serverMsg is one server-to-client message. The discriminator is the same
// field, and every kind the client understands carries its own payload.
type serverMsg struct {
	T string `json:"t"`
	// Inval: the path whose contents changed, and its new directory token.
	Path string `json:"path,omitempty"`
	ETag string `json:"etag,omitempty"`
}

// Hub is the shared broker. One instance for the process; the route handler
// upgrades connections into it.
type Hub struct {
	core  *core.Core
	watch *watch.Watcher
	clk   clock.Clock
	log   *slog.Logger
	up    *websocket.Upgrader

	mu    sync.Mutex
	conns map[*conn]struct{}
}

// NewHub builds the broker over the watcher's invalidation channel. The
// dispatch loop is a task, the one spawn this tree allows.
func NewHub(ctx context.Context, c *core.Core, w *watch.Watcher, clk clock.Clock, log *slog.Logger, events <-chan watch.InvalEvent) *Hub {
	h := &Hub{
		core: c, watch: w, clk: clk, log: log,
		up: &websocket.Upgrader{
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
			CheckOrigin:     func(r *http.Request) bool { return true },
		},
		conns: map[*conn]struct{}{},
	}
	task.Go(ctx, "ws dispatch", func() { h.dispatch(events) })
	return h
}

// Upgrade binds one connection for an authenticated user.
func (h *Hub) Upgrade(w http.ResponseWriter, r *http.Request, user core.UserID) {
	ws, err := h.up.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	c := &conn{
		hub: h, ws: ws, user: user,
		pending: map[string]time.Time{},
		subs:    map[string]subscription{},
	}
	h.mu.Lock()
	h.conns[c] = struct{}{}
	h.mu.Unlock()
	task.Go(r.Context(), "ws reader", c.readLoop)
	task.Go(r.Context(), "ws writer", c.writeLoop)
}

// dispatch fans each invalidation event out to every connection.
func (h *Hub) dispatch(events <-chan watch.InvalEvent) {
	for ev := range events {
		h.mu.Lock()
		for c := range h.conns {
			c.invalidate(ev)
		}
		h.mu.Unlock()
	}
}

// Close drops every connection, which is what a shutdown or a revoke needs.
func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.conns {
		_ = c.ws.Close() //nolint:errcheck // the peer is going away either way.
	}
}

// conn is one open WebSocket.
type conn struct {
	hub  *Hub
	ws   *websocket.Conn
	user core.UserID

	mu      sync.Mutex
	pending map[string]time.Time // client path -> when the debounce started
	// subs is what this connection asked for, keyed by the path the client
	// used, because that is the path the client has to be told about.
	subs map[string]subscription
}

// subscription is one path this connection watches, in the terms the recheck
// and the event stream use.
type subscription struct {
	share vfs.ShareID
	dir   string
}

// readLoop consumes client frames until the peer closes, and shuts the
// connection down on the way out so the writer task does not leak.
func (c *conn) readLoop() {
	defer c.close()
	for {
		_, raw, err := c.ws.ReadMessage()
		if err != nil {
			return
		}
		var f Frame
		if err := json.Unmarshal(raw, &f); err != nil {
			return
		}
		if f.T == framePing {
			c.write(serverMsg{T: "pong"})
			continue
		}
		c.apply(f)
	}
}

// close removes the connection from the hub and breaks the socket.
func (c *conn) close() {
	c.hub.mu.Lock()
	delete(c.hub.conns, c)
	c.hub.mu.Unlock()
	_ = c.ws.Close() //nolint:errcheck // the peer is going away.
}

// apply handles one sub or unsub frame. Every path is resolved and checked
// against the caller's grants first.
func (c *conn) apply(f Frame) {
	for _, p := range f.Paths {
		vp, err := vfs.ParseVpath(p)
		if err != nil {
			continue
		}
		resolved, err := c.hub.core.Resolve(c.user, vp, acl.Read)
		if err != nil {
			continue
		}
		switch f.T {
		case frameSub:
			c.hub.watch.Subscribe(resolved.Share(), resolved.Path())
			c.mu.Lock()
			c.subs[p] = subscription{share: resolved.Share(), dir: resolved.Path().String()}
			c.mu.Unlock()
		case frameUnsub:
			c.hub.watch.Unsubscribe(resolved.Share(), resolved.Path())
			c.mu.Lock()
			delete(c.subs, p)
			c.mu.Unlock()
		}
	}
}

// invalidate records an event for this connection, debounced per path.
func (c *conn) invalidate(ev watch.InvalEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// The event names a share and a share-relative directory; the client names
	// paths its own way. Only a path this connection actually asked for is
	// recorded, so an event for a directory nobody is looking at costs nothing
	// and no client is told about a path it never named.
	for path, sub := range c.subs {
		if sub.share != ev.Share {
			continue
		}
		if !ev.All && sub.dir != ev.Dir {
			continue
		}
		if _, seen := c.pending[path]; seen {
			continue
		}
		c.pending[path] = c.hub.clk.Now()
	}
}

// writeLoop drains the debounced events and the peer's own traffic. A channel
// event older than the debounce window is sent once per path.
func (c *conn) writeLoop() {
	// A stopped ticker releases its channel; this loop runs until the socket
	// breaks, which the read loop signals by closing the underlying conn.
	ticker := time.NewTicker(debounce)
	defer ticker.Stop()
	for range ticker.C {
		c.flush()
	}
}

// flush sends one change summary per path whose debounce has elapsed.
//
// Every path is rechecked here, not only when it was subscribed to. A grant
// revoked between the two must not leak the next event, and checking only at
// subscribe time is exactly what makes a revoked grant keep delivering.
func (c *conn) flush() {
	for path := range c.due() {
		vp, err := vfs.ParseVpath(path)
		if err != nil {
			continue
		}
		if _, rerr := c.hub.core.Resolve(c.user, vp, acl.Read); rerr != nil {
			// The caller can no longer read it, so the event is dropped and
			// the subscription goes with it.
			c.mu.Lock()
			delete(c.subs, path)
			c.mu.Unlock()
			continue
		}
		// No token accompanies the summary. Producing one means listing the
		// directory, which is the work the client is about to do anyway when
		// it revalidates, and doing it here would do it once per subscriber
		// rather than once per interested tab. The client treats the summary
		// as "this changed, ask again", which is what it does with a token it
		// does not recognise either.
		if !c.write(serverMsg{T: "inval", Path: path}) {
			return
		}
	}
}

// write sends one message, reporting whether the socket is still usable.
func (c *conn) write(msg serverMsg) bool {
	raw, err := json.Marshal(msg)
	if err != nil {
		c.hub.log.Warn("ws message could not be encoded", "kind", msg.T, "error", err)
		return true
	}
	if err := c.ws.WriteMessage(websocket.TextMessage, raw); err != nil {
		c.hub.log.Warn("ws write failed", "kind", msg.T, "error", err)
		return false
	}
	return true
}

// due returns the paths whose debounce window has elapsed, removing them from
// the pending set. It is the pure half of flush, separated so the coalescing
// rule is testable without a socket.
func (c *conn) due() map[string]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.hub.clk.Now()
	ready := map[string]string{}
	for key, when := range c.pending {
		if now.Sub(when) >= debounce {
			ready[key] = key
			delete(c.pending, key)
		}
	}
	return ready
}
