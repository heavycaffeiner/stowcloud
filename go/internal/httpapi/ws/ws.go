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
	"fmt"
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

// Frame is a client-to-server message: subscribe or unsubscribe, carrying a
// list of paths.
type Frame struct {
	Op    string   `json:"op"`
	Paths []string `json:"paths"`
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
	c := &conn{hub: h, ws: ws, user: user, pending: map[string]time.Time{}}
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
	pending map[string]time.Time // path -> when the debounce started
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
		switch f.Op {
		case "sub":
			c.hub.watch.Subscribe(resolved.Share(), resolved.Path())
		case "unsub":
			c.hub.watch.Unsubscribe(resolved.Share(), resolved.Path())
		}
	}
}

// invalidate records an event for this connection, debounced per path.
func (c *conn) invalidate(ev watch.InvalEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := fmt.Sprintf("%d/%s", ev.Share, ev.Dir)
	if _, seen := c.pending[key]; seen {
		return
	}
	c.pending[key] = c.hub.clk.Now()
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
func (c *conn) flush() {
	for key := range c.due() {
		if err := c.ws.WriteMessage(websocket.TextMessage, []byte(key)); err != nil {
			c.hub.log.Warn("ws write failed", "key", key, "error", err)
			return
		}
	}
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
