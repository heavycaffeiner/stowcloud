//go:build linux

package logbook_test

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/task"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/logbook"
)

// stepClock advances a fixed amount per reading, so segment names and record
// stamps are distinct and ordered without depending on the wall clock.
//
// The base is a fixed instant rather than the real one. It used to be the real
// one, because slog stamped every record with the wall clock while the store
// framed its counting window from this clock: a base anywhere else put the
// records outside every window, and a base here that lagged the wall clock by
// one step dropped the newest record intermittently. The store stamps records
// from its own clock now, so both sides read this and the absolute value
// stops mattering.
type stepClock struct {
	mu   sync.Mutex
	ns   int64
	base int64
}

// stepClockBase is an arbitrary real instant, so a stamp taken from anywhere
// else is obvious rather than plausible.
const stepClockBase = int64(1_700_000_000) * 1e9

func newStepClock() *stepClock { return newStepClockAt(stepClockBase) }

func newStepClockAt(base int64) *stepClock {
	return &stepClock{ns: base, base: base}
}

func (c *stepClock) Now() time.Time {
	return time.Unix(0, c.Nanos())
}

func (c *stepClock) Since(t time.Time) time.Duration {
	return time.Duration(c.Nanos() - t.UnixNano())
}

func (c *stepClock) Nanos() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ns += int64(time.Millisecond)
	return c.ns
}

// peek reads the clock without advancing it, for a test that has to bound
// where a stamp could have come from.
func (c *stepClock) peek() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ns
}

func open(t *testing.T, opt logbook.Options) *logbook.Sink {
	t.Helper()
	if opt.Dir == "" {
		opt.Dir = filepath.Join(t.TempDir(), "logs")
	}
	if opt.Clock == nil {
		opt.Clock = newStepClock()
	}
	s, err := logbook.Open(opt)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if cerr := s.Close(); cerr != nil {
			t.Errorf("Close: %v", cerr)
		}
	})
	return s
}

func query(t *testing.T, s *logbook.Sink, q logbook.Query) logbook.Page {
	t.Helper()
	page, err := s.Query(context.Background(), q)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	return page
}

func messages(p logbook.Page) []string {
	out := make([]string, 0, len(p.Records))
	for _, r := range p.Records {
		out = append(out, r.Msg)
	}
	return out
}

// Every level the engine can emit reaches the store, including one below Debug
// that a console would normally drop. The store is what the dashboard filters
// after the fact, so a level it never received is a line nobody can find.
func TestEveryLevelIsStored(t *testing.T) {
	s := open(t, logbook.Options{})
	log := slog.New(s.Handler(slog.Level(-8)))

	log.Log(context.Background(), slog.Level(-8), "below debug")
	log.Debug("debug")
	log.Info("info")
	log.Warn("warn")
	log.Error("error")

	page := query(t, s, logbook.Query{})
	if len(page.Records) != 5 {
		t.Fatalf("the store holds %d records, want 5: %v", len(page.Records), messages(page))
	}
	// Newest first.
	if got := messages(page); got[0] != "error" || got[4] != "below debug" {
		t.Errorf("records are not newest first: %v", got)
	}
}

// A handler reports the levels it will store, so a caller that checks before
// building an expensive record gets a truthful answer.
func TestEnabledFollowsTheLevel(t *testing.T) {
	s := open(t, logbook.Options{})
	h := s.Handler(slog.LevelWarn)

	if h.Enabled(context.Background(), slog.LevelInfo) {
		t.Error("a handler at Warn reports Info as enabled")
	}
	if !h.Enabled(context.Background(), slog.LevelError) {
		t.Error("a handler at Warn reports Error as disabled")
	}
}

// Groups and attributes flatten to dotted names, because a record carries a
// flat map and a filter over a nested shape would be a tree walk per line.
func TestGroupsAndAttributesFlatten(t *testing.T) {
	s := open(t, logbook.Options{})
	log := slog.New(s.Handler(slog.LevelDebug)).
		With("outer", "o").
		WithGroup("req").
		With("method", "PUT")

	log.Info("wrote", slog.Group("file", "path", "/x", "size", 12))

	page := query(t, s, logbook.Query{})
	if len(page.Records) != 1 {
		t.Fatalf("the store holds %d records, want 1", len(page.Records))
	}
	want := map[string]string{
		"outer":         "o",
		"req.method":    "PUT",
		"req.file.path": "/x",
		"req.file.size": "12",
	}
	got := page.Records[0].Attrs
	for k, v := range want {
		if got[k] != v {
			t.Errorf("attr %q is %q, want %q: %v", k, got[k], v, got)
		}
	}
}

// The two filtered names become fields of their own and leave the map, so a
// filter on them is a comparison rather than a scan of every pair.
func TestThePromotedNamesLeaveTheAttributeMap(t *testing.T) {
	s := open(t, logbook.Options{})
	slog.New(s.Handler(slog.LevelDebug)).
		Info("refused", "subsystem", "dav", "request_id", "01J", "path", "/x")

	r := query(t, s, logbook.Query{}).Records[0]
	switch {
	case r.Subsystem != "dav":
		t.Errorf("subsystem is %q", r.Subsystem)
	case r.RequestID != "01J":
		t.Errorf("request id is %q", r.RequestID)
	}
	if _, present := r.Attrs["subsystem"]; present {
		t.Error("subsystem is still in the attribute map")
	}
	if _, present := r.Attrs["request_id"]; present {
		t.Error("request_id is still in the attribute map")
	}
	if r.Attrs["path"] != "/x" {
		t.Errorf("an ordinary attribute was lost: %v", r.Attrs)
	}
}

// A logger built with .With() keeps its attributes on every leg. One leg
// holding them and the other not would write two different lines for one call.
func TestFanoutKeepsAttributesOnEveryLeg(t *testing.T) {
	s := open(t, logbook.Options{})
	var console bytes.Buffer
	text := slog.NewTextHandler(&console, &slog.HandlerOptions{Level: slog.LevelDebug})

	slog.New(logbook.Fanout(text, s.Handler(slog.LevelDebug))).
		With("carried", "yes").
		WithGroup("g").
		Info("both legs", "inner", "1")

	if !strings.Contains(console.String(), `carried=yes`) {
		t.Errorf("the console leg lost the attribute: %s", console.String())
	}
	if !strings.Contains(console.String(), `g.inner=1`) {
		t.Errorf("the console leg lost the group: %s", console.String())
	}
	r := query(t, s, logbook.Query{}).Records[0]
	if r.Attrs["carried"] != "yes" || r.Attrs["g.inner"] != "1" {
		t.Errorf("the store leg lost an attribute: %v", r.Attrs)
	}
}

// Enabled is true when any leg wants the level, so a quiet console does not
// empty the store.
func TestFanoutEnabledIsTheUnion(t *testing.T) {
	s := open(t, logbook.Options{})
	var console bytes.Buffer
	quiet := slog.NewTextHandler(&console, &slog.HandlerOptions{Level: slog.LevelError})

	h := logbook.Fanout(quiet, s.Handler(slog.LevelDebug))
	if !h.Enabled(context.Background(), slog.LevelDebug) {
		t.Fatal("a debug line was refused because one leg did not want it")
	}

	slog.New(h).Debug("kept")
	if got := messages(query(t, s, logbook.Query{})); len(got) != 1 {
		t.Errorf("the store holds %v, want the debug line", got)
	}
	if console.Len() != 0 {
		t.Errorf("the quiet leg wrote %q", console.String())
	}
}

// A failing leg does not stop the others. Losing the store because the
// console broke is the outcome the fanout exists to avoid.
func TestFanoutSurvivesAFailingLeg(t *testing.T) {
	s := open(t, logbook.Options{})
	h := logbook.Fanout(brokenHandler{}, s.Handler(slog.LevelDebug))

	slog.New(h).Info("still stored")

	if got := messages(query(t, s, logbook.Query{})); len(got) != 1 {
		t.Errorf("the store holds %v, want the line the other leg took", got)
	}
}

type brokenHandler struct{}

func (brokenHandler) Enabled(context.Context, slog.Level) bool  { return true }
func (brokenHandler) Handle(context.Context, slog.Record) error { return os.ErrClosed }
func (b brokenHandler) WithAttrs([]slog.Attr) slog.Handler      { return b }
func (b brokenHandler) WithGroup(string) slog.Handler           { return b }

// Passing the size bound rotates, and the retention budget drops the oldest
// segment rather than growing without limit.
func TestRotationAndRetention(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")
	// Small enough that a handful of records crosses it, and a budget of two
	// segments' worth.
	s := open(t, logbook.Options{Dir: dir, MaxSegmentSize: 256, MaxTotalSize: 512})
	log := slog.New(s.Handler(slog.LevelDebug))

	for i := 0; i < 400; i++ {
		log.Info("a line with enough text to compress to something", "i", i,
			"filler", strings.Repeat("x", 64))
	}

	stats := s.Stats()
	if stats.Segments < 2 {
		t.Fatalf("400 records over a 256 byte bound produced %d segments", stats.Segments)
	}
	if stats.StoredBytes > 4*512 {
		t.Errorf("the store holds %d bytes against a 512 byte budget", stats.StoredBytes)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading the directory: %v", err)
	}
	if len(entries) != stats.Segments {
		t.Errorf("Stats reports %d segments; the directory holds %d", stats.Segments, len(entries))
	}
	// The newest records survive whatever retention dropped.
	page := query(t, s, logbook.Query{Limit: 1})
	if len(page.Records) != 1 || page.Records[0].Attrs["i"] != "399" {
		t.Errorf("the newest record is %v, want i=399", page.Records)
	}
}

// Each filter selects on its own, and two together narrow further.
func TestEveryFilterSelects(t *testing.T) {
	s := open(t, logbook.Options{})
	log := slog.New(s.Handler(slog.LevelDebug))

	log.Info("first message", "subsystem", "dav", "request_id", "a", "path", "/alpha")
	log.Warn("second message", "subsystem", "smb", "request_id", "b", "path", "/beta")
	log.Error("third message", "subsystem", "dav", "request_id", "c", "path", "/gamma")

	all := query(t, s, logbook.Query{})
	if len(all.Records) != 3 {
		t.Fatalf("the store holds %d records, want 3", len(all.Records))
	}
	oldest, newest := all.Records[2].TSNs, all.Records[0].TSNs

	for name, tc := range map[string]struct {
		q    logbook.Query
		want []string
	}{
		"level":            {logbook.Query{Levels: []string{"WARN"}}, []string{"second message"}},
		"two levels":       {logbook.Query{Levels: []string{"WARN", "ERROR"}}, []string{"third message", "second message"}},
		"level lower case": {logbook.Query{Levels: []string{"warn"}}, []string{"second message"}},
		"text in message":  {logbook.Query{Text: "third"}, []string{"third message"}},
		"text in attr":     {logbook.Query{Text: "beta"}, []string{"second message"}},
		"text mixed case":  {logbook.Query{Text: "BETA"}, []string{"second message"}},
		"subsystem":        {logbook.Query{Subsystem: "dav"}, []string{"third message", "first message"}},
		"request id":       {logbook.Query{RequestID: "b"}, []string{"second message"}},
		"since":            {logbook.Query{Since: newest}, []string{"third message"}},
		"until":            {logbook.Query{Until: oldest}, []string{"first message"}},
		"two filters":      {logbook.Query{Subsystem: "dav", Levels: []string{"ERROR"}}, []string{"third message"}},
		"no match":         {logbook.Query{Subsystem: "nothing"}, nil},
	} {
		t.Run(name, func(t *testing.T) {
			got := messages(query(t, s, tc.q))
			if len(got) != len(tc.want) {
				t.Fatalf("%v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("%v, want %v", got, tc.want)
				}
			}
		})
	}
}

// Paging walks every record exactly once across a rotation, with no skip and
// no repeat, and stops by handing back an empty cursor.
func TestPagingWalksEveryRecordOnce(t *testing.T) {
	s := open(t, logbook.Options{MaxSegmentSize: 512})
	log := slog.New(s.Handler(slog.LevelDebug))

	const total = 250
	for i := 0; i < total; i++ {
		log.Info(fmt.Sprintf("line %03d", i), "filler", strings.Repeat("y", 40))
	}

	seen := map[string]int{}
	cursor := ""
	for pages := 0; ; pages++ {
		if pages > total {
			t.Fatal("the pager did not terminate")
		}
		page := query(t, s, logbook.Query{Limit: 17, Cursor: cursor})
		for _, r := range page.Records {
			seen[r.Msg]++
		}
		if page.Cursor == "" {
			break
		}
		if len(page.Records) == 0 {
			t.Fatal("a page with no records handed back a cursor")
		}
		cursor = page.Cursor
	}

	if len(seen) != total {
		t.Fatalf("the walk saw %d distinct records, want %d", len(seen), total)
	}
	for msg, n := range seen {
		if n != 1 {
			t.Errorf("%s was returned %d times", msg, n)
		}
	}
}

// A cursor this store did not write is refused rather than restarting the
// walk, which would loop a pager forever.
func TestAMalformedCursorIsRefused(t *testing.T) {
	s := open(t, logbook.Options{})
	slog.New(s.Handler(slog.LevelDebug)).Info("one")

	for _, bad := range []string{"nonsense", "log-x.jsonl.gz:notanumber", ":4", "log-1.jsonl.gz:-2"} {
		if _, err := s.Query(context.Background(), logbook.Query{Cursor: bad}); err == nil {
			t.Errorf("cursor %q was accepted", bad)
		}
	}
}

// An empty store answers with no records and no error. A dashboard opening on
// a fresh deployment is the ordinary case, not a failure.
func TestAnEmptyStoreAnswersEmpty(t *testing.T) {
	s := open(t, logbook.Options{})

	page := query(t, s, logbook.Query{})
	if len(page.Records) != 0 {
		t.Errorf("a fresh store holds %v", messages(page))
	}
	if page.Cursor != "" {
		t.Errorf("a fresh store handed back cursor %q", page.Cursor)
	}
	if stats := s.Stats(); stats.Segments != 1 {
		t.Errorf("a fresh store reports %d segments, want the one it opened", stats.Segments)
	}
}

// A limit past the ceiling is clamped rather than honoured, because the ask
// is a caller-controlled scan.
func TestTheLimitIsBounded(t *testing.T) {
	s := open(t, logbook.Options{})
	log := slog.New(s.Handler(slog.LevelDebug))
	for i := 0; i < 600; i++ {
		log.Info(fmt.Sprintf("line %d", i))
	}

	page := query(t, s, logbook.Query{Limit: 10_000})
	if len(page.Records) != 500 {
		t.Errorf("an unbounded ask returned %d records, want the 500 ceiling", len(page.Records))
	}
}

// Writers and readers run together without a race and without losing a line.
// The engine logs from every request goroutine while the dashboard scans.
func TestConcurrentWritersAndReaders(t *testing.T) {
	s := open(t, logbook.Options{MaxSegmentSize: 1 << 12})
	log := slog.New(s.Handler(slog.LevelDebug))

	const writers, each = 8, 60
	ctx := context.Background()
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		writer := w
		task.Go(ctx, "logbook test writer", func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				log.Info("concurrent", "writer", writer, "i", i)
			}
		})
	}
	for r := 0; r < 4; r++ {
		wg.Add(1)
		task.Go(ctx, "logbook test reader", func() {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				if _, err := s.Query(ctx, logbook.Query{Limit: 25}); err != nil {
					t.Errorf("Query during writes: %v", err)
					return
				}
			}
		})
	}
	wg.Wait()

	if dropped := s.Dropped(); dropped != 0 {
		t.Errorf("%d records were dropped", dropped)
	}

	// Every line is findable afterwards, which is what proves the flush per
	// record and the rotation under load did not lose one.
	seen := 0
	cursor := ""
	for {
		page := query(t, s, logbook.Query{Limit: 100, Cursor: cursor})
		seen += len(page.Records)
		if page.Cursor == "" {
			break
		}
		cursor = page.Cursor
	}
	if seen != writers*each {
		t.Errorf("the store holds %d records, want %d", seen, writers*each)
	}
}

// A query while a write is in flight sees the line that just landed. The
// dashboard is read right after the failure an operator is chasing.
func TestTheNewestLineIsImmediatelyReadable(t *testing.T) {
	s := open(t, logbook.Options{})
	log := slog.New(s.Handler(slog.LevelDebug))

	log.Warn("the write was refused", "subsystem", "dav")

	page := query(t, s, logbook.Query{Text: "refused"})
	if len(page.Records) != 1 {
		t.Fatalf("the line just written is not readable: %v", messages(page))
	}
}

// A directory that cannot be created is a refusal at open, so a deployment
// learns at boot rather than on the first log line.
func TestAnUnusableDirectoryIsRefused(t *testing.T) {
	file := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("writing: %v", err)
	}

	if _, err := logbook.Open(logbook.Options{Dir: filepath.Join(file, "logs")}); err == nil {
		t.Error("a directory under a regular file was accepted")
	}
	if _, err := logbook.Open(logbook.Options{}); err == nil {
		t.Error("a store with no directory was accepted")
	}
}
