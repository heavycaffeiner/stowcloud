// Linux only, because what it tests is.
//go:build linux

package server

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/internal/clock"

	"github.com/heavycaffeiner/stowcloud/go/internal/httpapi/ws"
)

// The change channel's upgrade has to reach the hub.
//
// It did not: the hub was built at startup, passed into the options, and never
// put where the handler looks, so every client asking for the change channel
// got "not implemented in this build" from a build that had one. The route was
// mounted and the handler was correct; the wire between them was missing, which
// is the failure a test of either half alone does not see.

func TestTheEventsSurfaceReachesAWiredHub(t *testing.T) {
	if eventsHandler(nil) != nil {
		t.Error("a build with no hub reported one, so the surface would answer with a panic")
	}

	hub := ws.NewHub(context.Background(), nil, nil, clock.System(), slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	t.Cleanup(hub.Close)

	if eventsHandler(hub) == nil {
		t.Fatal("a wired hub did not reach the change-channel surface, so every client is told this build has none")
	}
}
