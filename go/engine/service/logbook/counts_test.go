//go:build linux

package logbook_test

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/task"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/logbook"
)

// counts is the walk under test, with the failure handling every case shares.
func counts(t *testing.T, s *logbook.Sink, q logbook.Query, width int64) ([]logbook.Bucket, int64, bool) {
	t.Helper()
	buckets, got, truncated, err := s.Counts(context.Background(), q, width)
	if err != nil {
		t.Fatalf("Counts: %v", err)
	}
	return buckets, got, truncated
}

// total is every level in one bucket added up.
func total(b logbook.Bucket) int {
	n := 0
	for _, v := range b.Levels {
		n += v
	}
	return n
}

// The counts are the window's, not the page's. A graph added up from a loaded
// page would rise and fall with what the reader scrolled to.
func TestCountsCoverEveryRecordInTheWindow(t *testing.T) {
	clk := newStepClock()
	s := open(t, logbook.Options{Clock: clk})
	log := slog.New(s.Handler(slog.LevelDebug))

	// More records than any page returns, so a walk that paged would undercount.
	const written = 700
	for i := 0; i < written; i++ {
		log.Info("a line")
	}

	// Framed on the records rather than on the clock's own reading. The two
	// agree on a real server, since both are the wall clock, but a fake clock
	// that steps once per reading falls behind the times slog stamps records
	// with, and the window would then end before the last of them.
	page, err := s.Query(context.Background(), logbook.Query{Limit: 1})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	newest := page.Records[0].TSNs
	buckets, _, truncated := counts(t, s, logbook.Query{
		Since: newest - int64(time.Hour), Until: newest,
	}, int64(time.Minute))
	if truncated {
		t.Error("a walk over 700 records reports itself truncated")
	}
	seen := 0
	for _, b := range buckets {
		seen += total(b)
	}
	if seen != written {
		t.Errorf("the counts total %d, want every one of the %d records", seen, written)
	}
}

// Every level is counted under its own name, so a chart can draw one series
// per level rather than one bar per bucket.
func TestCountsAreKeyedByLevel(t *testing.T) {
	s := open(t, logbook.Options{})
	log := slog.New(s.Handler(slog.LevelDebug))

	log.Debug("d")
	log.Info("i")
	log.Info("i again")
	log.Warn("w")
	log.Error("e")

	buckets, _, _ := counts(t, s, logbook.Query{}, int64(time.Hour))
	got := map[string]int{}
	for _, b := range buckets {
		for level, n := range b.Levels {
			got[level] += n
		}
	}
	for level, want := range map[string]int{"DEBUG": 1, "INFO": 2, "WARN": 1, "ERROR": 1} {
		if got[level] != want {
			t.Errorf("level %s counted %d, want %d: %v", level, got[level], want, got)
		}
	}
}

// The frame covers the window with no holes: an interval with no records is
// still an interval, so a renderer never has to tell a gap from a zero.
func TestEveryBucketInTheWindowIsPresent(t *testing.T) {
	clk := newStepClock()
	s := open(t, logbook.Options{Clock: clk})
	slog.New(s.Handler(slog.LevelDebug)).Info("one")

	// A window with a known width and a known span: six one-minute buckets.
	now := clk.Nanos()
	since := now - int64(5*time.Minute)
	buckets, width, _ := counts(t, s,
		logbook.Query{Since: since, Until: now}, int64(time.Minute))

	if width != int64(time.Minute) {
		t.Fatalf("the width is %d, want the minute asked for", width)
	}
	if len(buckets) < 5 {
		t.Fatalf("a five minute window produced %d one-minute buckets", len(buckets))
	}
	empty := 0
	for _, b := range buckets {
		if b.Levels == nil {
			t.Fatal("a bucket carries a nil map, which a caller would have to test for")
		}
		if total(b) == 0 {
			empty++
		}
	}
	if empty == 0 {
		t.Error("no bucket came back empty, so the frame is not covering the window")
	}
	// Oldest first, because a chart draws left to right.
	for i := 1; i < len(buckets); i++ {
		if buckets[i].StartNs <= buckets[i-1].StartNs {
			t.Fatalf("bucket %d starts at or before its predecessor", i)
		}
		if buckets[i].StartNs-buckets[i-1].StartNs != width {
			t.Fatalf("bucket %d is %dns after its predecessor, want %d",
				i, buckets[i].StartNs-buckets[i-1].StartNs, width)
		}
	}
}

// The filters are the list's filters. A graph that counted what the list
// excluded would describe a different query than the one on screen.
func TestCountsHonourEveryFilter(t *testing.T) {
	s := open(t, logbook.Options{})
	log := slog.New(s.Handler(slog.LevelDebug))

	log.Warn("refused", "subsystem", "dav", "request_id", "a")
	log.Info("served", "subsystem", "api", "request_id", "b")
	log.Error("failed", "subsystem", "dav", "request_id", "c")

	for name, tc := range map[string]struct {
		q    logbook.Query
		want int
	}{
		"level":      {logbook.Query{Levels: []string{"WARN"}}, 1},
		"two levels": {logbook.Query{Levels: []string{"WARN", "ERROR"}}, 2},
		"subsystem":  {logbook.Query{Subsystem: "dav"}, 2},
		"request id": {logbook.Query{RequestID: "b"}, 1},
		"text":       {logbook.Query{Text: "refused"}, 1},
		"two of them": {logbook.Query{
			Subsystem: "dav", Levels: []string{"ERROR"},
		}, 1},
		"no match": {logbook.Query{Subsystem: "nothing"}, 0},
	} {
		t.Run(name, func(t *testing.T) {
			buckets, _, _ := counts(t, s, tc.q, int64(time.Hour))
			seen := 0
			for _, b := range buckets {
				seen += total(b)
			}
			if seen != tc.want {
				t.Errorf("the counts total %d, want %d", seen, tc.want)
			}
		})
	}
}

// A width that would need more bars than a chart can carry is replaced rather
// than honoured, because the alternative is a document nobody can draw.
func TestAnUnreasonableWidthIsReplaced(t *testing.T) {
	clk := newStepClock()
	s := open(t, logbook.Options{Clock: clk})
	slog.New(s.Handler(slog.LevelDebug)).Info("one")

	now := clk.Nanos()
	// A month of one-second buckets is over two million bars.
	since := now - int64(30*24*time.Hour)
	buckets, width, _ := counts(t, s,
		logbook.Query{Since: since, Until: now}, int64(time.Second))

	if width == int64(time.Second) {
		t.Fatal("a month of one-second buckets was honoured")
	}
	if len(buckets) > 240 {
		t.Errorf("the answer carries %d buckets, past the ceiling", len(buckets))
	}
	if width <= 0 {
		t.Errorf("the replaced width is %d", width)
	}
}

// An open-ended window is the last day, which is what a dashboard opens on.
func TestAnOpenWindowIsTheLastDay(t *testing.T) {
	clk := newStepClock()
	s := open(t, logbook.Options{Clock: clk})
	slog.New(s.Handler(slog.LevelDebug)).Info("one")

	buckets, width, _ := counts(t, s, logbook.Query{}, 0)
	if len(buckets) == 0 {
		t.Fatal("an open window produced no buckets")
	}
	span := buckets[len(buckets)-1].StartNs + width - buckets[0].StartNs
	day := int64(24 * time.Hour)
	// Within one bucket of a day, since the frame is aligned to the width.
	if span < day-width || span > day+width {
		t.Errorf("the open window spans %dns, want about %dns", span, day)
	}
}

// An empty store counts to nothing without failing. A dashboard opening on a
// fresh deployment is the ordinary case.
func TestCountsOverAnEmptyStore(t *testing.T) {
	s := open(t, logbook.Options{})

	buckets, width, truncated := counts(t, s, logbook.Query{}, int64(time.Hour))
	if truncated {
		t.Error("an empty store reports itself truncated")
	}
	if width <= 0 {
		t.Errorf("the width is %d", width)
	}
	for _, b := range buckets {
		if total(b) != 0 {
			t.Errorf("an empty store counted %d records in a bucket", total(b))
		}
	}
}

// The counting walk does not block a log line, and neither one loses a record
// while the other runs.
func TestCountsRunBesideWrites(t *testing.T) {
	s := open(t, logbook.Options{MaxSegmentSize: 1 << 12})
	log := slog.New(s.Handler(slog.LevelDebug))
	ctx := context.Background()

	const writers, each = 4, 50
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		task.Go(ctx, "logbook counts writer", func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				log.Info("concurrent")
			}
		})
	}
	for r := 0; r < 3; r++ {
		wg.Add(1)
		task.Go(ctx, "logbook counts reader", func() {
			defer wg.Done()
			for i := 0; i < 10; i++ {
				if _, _, _, err := s.Counts(ctx, logbook.Query{}, int64(time.Minute)); err != nil {
					t.Errorf("Counts during writes: %v", err)
					return
				}
			}
		})
	}
	wg.Wait()

	if dropped := s.Dropped(); dropped != 0 {
		t.Errorf("%d records were dropped", dropped)
	}
	buckets, _, _ := counts(t, s, logbook.Query{}, int64(time.Hour))
	seen := 0
	for _, b := range buckets {
		seen += total(b)
	}
	if seen != writers*each {
		t.Errorf("the counts total %d, want %d", seen, writers*each)
	}
}
