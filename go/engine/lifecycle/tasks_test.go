//go:build linux

package lifecycle

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/server"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/task"
)

// A recurring task runs once at startup and returns promptly when shutdown
// cancels it. Waiting a full interval for the first pass leaves old upload
// parts untouched after every restart.
func TestAPeriodicTaskRunsImmediatelyAndStops(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	var group task.Group
	ran := make(chan struct{}, 1)
	periodic := server.PeriodicTask{
		Name:  "test.sweep",
		Every: time.Hour,
		Run: func(context.Context) error {
			ran <- struct{}{}
			return nil
		},
	}
	group.Go(ctx, "periodic test", func() {
		runPeriodic(ctx, periodic, slog.New(slog.DiscardHandler))
	})

	select {
	case <-ran:
	case <-time.After(time.Second):
		t.Fatal("the periodic task did not run at startup")
	}
	cancel()
	deadline, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	if err := group.Wait(deadline); err != nil {
		t.Fatalf("the periodic task did not stop: %v", err)
	}
}
