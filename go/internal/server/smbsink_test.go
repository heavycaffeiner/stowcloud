package server

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strconv"
	"sync"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/internal/httpapi/handler"
	"github.com/heavycaffeiner/stowcloud/go/internal/smbagent"
	"github.com/heavycaffeiner/stowcloud/go/internal/task"
	"github.com/heavycaffeiner/stowcloud/go/internal/watch"
)

// The SMB sink.
//
// It exists because SMB used to be republished by exactly one thing: an
// administrator pressing apply. A grant revoked reached this server and not
// the daemon, so access stayed live over SMB with nothing saying so. Every
// test here is a property of that fix rather than of the publisher.

func quietLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// The watch fan-out.
//
// One channel with two readers gives each event to exactly one of them, so the
// change channel and the search index would each see about half the changes.
// The fan-out is what makes both see all of them, and this is the property it
// exists for.
func TestTheFanOutGivesEveryEventToBothConsumers(t *testing.T) {
	events := make(chan watch.InvalEvent, 8)
	forward := make(chan watch.InvalEvent, 8)

	var mu sync.Mutex
	var observed int
	done := make(chan struct{})
	task.Go(t.Context(), "fan-out under test", func() {
		defer close(done)
		defer close(forward)
		for ev := range events {
			mu.Lock()
			observed++
			mu.Unlock()
			forward <- ev
		}
	})

	for i := range 3 {
		events <- watch.InvalEvent{Share: 1, Dir: strconv.Itoa(i)}
	}
	close(events)
	<-done

	mu.Lock()
	saw := observed
	mu.Unlock()
	if saw != 3 {
		t.Fatalf("the observer saw %d events, want all 3", saw)
	}
	var forwarded int
	for range forward {
		forwarded++
	}
	if forwarded != 3 {
		t.Fatalf("the hub saw %d events, want all 3", forwarded)
	}
}

// A deployment with no sidecar gets no sink, so the write paths call nothing
// rather than calling something that reports a failure every time.
func TestNoPublisherMeansNoSink(t *testing.T) {
	if sink := smbSink(nil, handler.NewHealthState(), quietLog()); sink != nil {
		t.Fatal("a build with no publisher was given a sink")
	}
}

// The whole point: a write path calls the sink and the publisher runs.
func TestTheSinkPublishes(t *testing.T) {
	calls := 0
	sink := smbSink(func(context.Context) (smbagent.Report, error) {
		calls++
		return smbagent.Report{OK: true}, nil
	}, handler.NewHealthState(), quietLog())

	sink(context.Background())
	if calls != 1 {
		t.Fatalf("the publisher ran %d times, want once", calls)
	}
}

// A sidecar that did not answer is a degradation, not a panic and not a
// silence. The caller cannot be failed: the database write already committed
// and this server is already enforcing it.
func TestAFailedPublishDegradesHealth(t *testing.T) {
	health := handler.NewHealthState()
	sink := smbSink(func(context.Context) (smbagent.Report, error) {
		return smbagent.Report{}, errors.New("the sidecar did not answer")
	}, health, quietLog())

	sink(context.Background())

	if health.Status() != handler.HealthDegraded {
		t.Fatal("a failed publish left the server reporting healthy")
	}
	found := false
	for _, r := range health.Reasons() {
		if r.Kind == handler.ReasonSMBStale {
			found = true
		}
	}
	if !found {
		t.Fatalf("the degradation is %v, want one naming SMB as stale", health.Reasons())
	}
}

// A report that is not OK is the sidecar saying it applied something with a
// problem in it: a share path that does not exist where the daemon runs, or an
// account the import produced no credential for. Both are an operator's to fix
// and neither is the request's failure.
func TestAWarningReportDegradesHealth(t *testing.T) {
	health := handler.NewHealthState()
	sink := smbSink(func(context.Context) (smbagent.Report, error) {
		return smbagent.Report{OK: false, Error: "a share path does not exist"}, nil
	}, health, quietLog())

	sink(context.Background())
	if health.Status() != handler.HealthDegraded {
		t.Fatal("a warning report left the server reporting healthy")
	}
}

// A status that only ever gets worse stops being read. A publish that works
// after one that did not has to clear what the failure recorded.
func TestASuccessfulPublishClearsTheDegradation(t *testing.T) {
	health := handler.NewHealthState()
	fail := true
	sink := smbSink(func(context.Context) (smbagent.Report, error) {
		if fail {
			return smbagent.Report{}, errors.New("down")
		}
		return smbagent.Report{OK: true}, nil
	}, health, quietLog())

	sink(context.Background())
	if health.Status() != handler.HealthDegraded {
		t.Fatal("the failure was not recorded")
	}
	fail = false
	sink(context.Background())
	if health.Status() != handler.HealthOK {
		t.Fatalf("a successful publish left %v behind", health.Reasons())
	}
}

// The publish outlives the request. A browser that navigated away mid-write
// must not cancel a revocation that is halfway to the sidecar.
func TestThePublishIsNotCancelledByTheRequest(t *testing.T) {
	var seen error
	sink := smbSink(func(c context.Context) (smbagent.Report, error) {
		seen = c.Err()
		return smbagent.Report{OK: true}, nil
	}, handler.NewHealthState(), quietLog())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	sink(ctx)

	if seen != nil {
		t.Fatalf("the publisher saw a cancelled context: %v", seen)
	}
}
