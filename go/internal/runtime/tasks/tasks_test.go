//go:build linux

package tasks

import (
	"context"
	"log/slog"
	"testing"
	"time"
)

func TestTaskRunsImmediatelyAndStops(t *testing.T) {
	ran := make(chan struct{}, 1)
	runner := New(slog.New(slog.DiscardHandler))
	runner.Start([]Task{{
		Name:  "test.sweep",
		Every: time.Hour,
		Run: func(context.Context) error {
			ran <- struct{}{}
			return nil
		},
	}})

	select {
	case <-ran:
	case <-time.After(time.Second):
		t.Fatal("the periodic task did not run at startup")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := runner.Stop(ctx); err != nil {
		t.Fatalf("the periodic task did not stop: %v", err)
	}
}
