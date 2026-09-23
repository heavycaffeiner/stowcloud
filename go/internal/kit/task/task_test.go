package task

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// syncBuffer guards a bytes.Buffer with a mutex so a test goroutine can read
// it safely while a task goroutine writes a log line into it concurrently.
// The first write closes written, which is what a reader waits on: polling
// would need a wall clock, and this package's tree may not read one.
type syncBuffer struct {
	mu      sync.Mutex
	buf     bytes.Buffer
	once    sync.Once
	written chan struct{}
}

func newSyncBuffer() *syncBuffer {
	return &syncBuffer{written: make(chan struct{})}
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, err := s.buf.Write(p)
	s.once.Do(func() { close(s.written) })
	return n, err
}

// awaitWrite blocks until something has been logged, or fails the test.
func (s *syncBuffer) awaitWrite(t *testing.T) {
	t.Helper()
	select {
	case <-s.written:
	case <-time.After(5 * time.Second):
		t.Fatal("nothing was logged")
	}
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

func (s *syncBuffer) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Len()
}

func TestGoRunsFn(t *testing.T) {
	done := make(chan struct{})
	Go(context.Background(), "plain", func() { close(done) })
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Go never ran fn")
	}
}

func TestGoRecoversAPanicAndLogsStackAndName(t *testing.T) {
	buf := newSyncBuffer()
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, nil)))
	defer slog.SetDefault(prev)

	unwound := make(chan struct{})
	Go(context.Background(), "boom-task", func() {
		defer close(unwound)
		panic("something broke")
	})

	select {
	case <-unwound:
	case <-time.After(5 * time.Second):
		t.Fatal("panicking goroutine never finished unwinding")
	}

	// The deferred Recover writes after the panicking goroutine's own defers
	// have run, so the close of unwound above does not imply the log line.
	buf.awaitWrite(t)

	out := buf.String()
	if !strings.Contains(out, "boom-task") {
		t.Errorf("log output missing task name: %s", out)
	}
	if !strings.Contains(out, "something broke") {
		t.Errorf("log output missing panic value: %s", out)
	}
	if !strings.Contains(out, "goroutine") {
		t.Errorf("log output missing a stack trace: %s", out)
	}

	// The process is still alive to run this assertion at all, which is the
	// point: an unrecovered panic here would have ended the test binary.
	afterward := make(chan struct{})
	Go(context.Background(), "afterward", func() { close(afterward) })
	select {
	case <-afterward:
	case <-time.After(5 * time.Second):
		t.Fatal("process did not survive the panic")
	}
}

func TestRecoverDeferredDirectlyBehavesTheSame(t *testing.T) {
	buf := newSyncBuffer()
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, nil)))
	defer slog.SetDefault(prev)

	done := make(chan struct{})
	go func() {
		defer close(done)
		defer Recover(context.Background(), "manual-goroutine")
		panic("manual panic")
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("goroutine using a direct Recover defer never finished")
	}

	buf.awaitWrite(t)

	out := buf.String()
	if !strings.Contains(out, "manual-goroutine") {
		t.Errorf("log output missing task name: %s", out)
	}
	if !strings.Contains(out, "manual panic") {
		t.Errorf("log output missing panic value: %s", out)
	}
}

func TestRecoverWithNoPanicIsANoop(t *testing.T) {
	buf := newSyncBuffer()
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, nil)))
	defer slog.SetDefault(prev)

	func() {
		defer Recover(context.Background(), "quiet")
	}()

	if buf.Len() != 0 {
		t.Fatalf("Recover with no panic logged something: %s", buf.String())
	}
}
