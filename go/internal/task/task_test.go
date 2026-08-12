package task

import (
	"context"
	"testing"
	"time"
)

func TestGoRunsTheFunction(t *testing.T) {
	done := make(chan struct{})
	Go(context.Background(), "runs", func() { close(done) })
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("fn did not run")
	}
}

// A panic in a goroutine with no recover takes the process down and every
// request in flight with it. This is the whole reason the package exists.
func TestGoRecoversAPanic(t *testing.T) {
	survived := make(chan struct{})
	Go(context.Background(), "panics", func() {
		defer func() { close(survived) }()
		panic("decoder gave up")
	})
	select {
	case <-survived:
	case <-time.After(5 * time.Second):
		t.Fatal("the panicking task never reached its own defer")
	}
	// Reaching here at all means the recover in Go ran: an unrecovered panic
	// in that goroutine would have ended the test binary. Waiting on a second
	// task gives the first one's unwinding somewhere to land first.
	after := make(chan struct{})
	Go(context.Background(), "after", func() { close(after) })
	select {
	case <-after:
	case <-time.After(5 * time.Second):
		t.Fatal("the process did not survive the panic")
	}
}
