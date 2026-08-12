package cache_test

import (
	"context"
	"database/sql"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/internal/clock"
	"github.com/heavycaffeiner/stowcloud/go/internal/num"
	"github.com/heavycaffeiner/stowcloud/go/internal/store/cache"
	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
)

// The measurement the driver choice is allowed to be reversed by: a cold walk
// populating node for a multi-million-file tree, then the steady-state
// invalidation a watcher feeds in behind it.
//
// It is skipped unless SC_MEASURE_ROWS names a row count, because it is
// minutes of work and not a unit test. Cross-compile the package's test binary
// and run it in the Linux guest, which is the only place a number from it
// means anything:
//
//	CGO_ENABLED=0 GOOS=linux go test -c ./internal/store/cache
//	SC_MEASURE_ROWS=2000000 ./cache.test -test.run TestDriverMeasurement -test.v

// chainDepth is how many directories sit above a file, and so how many
// aggregates one write invalidates.
const chainDepth = 8

func TestDriverMeasurement(t *testing.T) {
	rows := envRows(t, "SC_MEASURE_ROWS")
	if rows == 0 {
		t.Skip("set SC_MEASURE_ROWS to a row count to run the driver measurement")
	}
	ctx := context.Background()

	// Elapsed time comes from the one clock this tree has, which measures a
	// duration monotonically: a wall-clock subtraction moves when NTP steps
	// the clock, and a measurement that runs for minutes is exactly where that
	// would land.
	clk := clock.System()

	// One row per transaction, which is the shape the implementation this
	// replaces has: its allocation takes a pooled connection and commits on
	// its own.
	perRow := openPair(t, t.TempDir(), 0)
	chain := makeChain(t, perRow.cache)
	start := clk.Now()
	populateMeasured(t, perRow.cache, chain[len(chain)-1], 0, rows, 1)
	perRowElapsed := clk.Since(start)
	report(t, "cold populate, one transaction per file", rows, perRowElapsed)

	// One transaction per batch, which is the shape a walker has: it commits
	// what it has read so far and carries on.
	batched := openPair(t, t.TempDir(), 0)
	batchChain := makeChain(t, batched.cache)
	start = clk.Now()
	populateMeasured(t, batched.cache, batchChain[len(batchChain)-1], 0, rows, 10_000)
	report(t, "cold populate, 10,000 files per transaction", rows, clk.Since(start))

	// Steady state, measured the way it actually happens: a change arrives,
	// the file's ancestors are invalidated and its directory's aggregate is
	// stored again, one transaction per event because that is what an event
	// is. This is the second half of the threshold, and the walk feeding it is
	// the one above.
	events := rows / 10
	if events > 200_000 {
		events = 200_000
	}
	start = clk.Now()
	for i := range events {
		if err := perRow.cache.Write(ctx, func(tx *sql.Tx) error {
			if err := perRow.cache.MarkDirty(ctx, tx, testShare, chain); err != nil {
				return err
			}
			return perRow.cache.PutDirEtag(ctx, tx, testShare, chain[len(chain)-1],
				cache.Aggregate{Etag: "e" + strconv.Itoa(i), RSize: 1, RCount: 1}, 0)
		}); err != nil {
			t.Fatalf("invalidating: %v", err)
		}
	}
	report(t, "steady-state invalidation", events, clk.Since(start))

	// The threshold with a number in it. The comparison is against the
	// implementation this replaces on the same tree, so the number comes from
	// running that one first and is passed in rather than guessed at.
	rust := envRows(t, "SC_MEASURE_RUST_SECONDS")
	if rust == 0 {
		t.Log("MEASURE no SC_MEASURE_RUST_SECONDS given, so the 3x threshold is not checked here")
		return
	}
	limit := float64(rust) * 3
	t.Logf("MEASURE cold populate %.2fs against %ds, which the threshold caps at %.2fs",
		perRowElapsed.Seconds(), rust, limit)
	if perRowElapsed.Seconds() > limit {
		t.Errorf("the cold populate is over three times the implementation this replaces")
	}
}

// makeChain builds the directories above the files, and returns them
// root-first so the last one is the parent every file goes under.
func makeChain(t *testing.T, c *cache.DB) []cache.FileID {
	t.Helper()
	ctx := context.Background()
	chain := make([]cache.FileID, 0, chainDepth)
	if err := c.Write(ctx, func(tx *sql.Tx) error {
		parent := cache.RootID
		for i := range chainDepth {
			b := int64(i)
			ino, nerr := num.Narrow[uint64](i + 1)
			if nerr != nil {
				return nerr
			}
			st := vfs.Stat{Dev: testDev, Ino: ino, BtimeNs: &b, Kind: vfs.KindDir}
			id, err := c.Upsert(ctx, tx, testShare, parent, "d"+strconv.Itoa(i), st)
			if err != nil {
				return err
			}
			chain = append(chain, id)
			parent = id
		}
		return nil
	}); err != nil {
		t.Fatalf("building the directory chain: %v", err)
	}
	return chain
}

// populateMeasured inserts rows files under parent, committing every batch.
func populateMeasured(t *testing.T, c *cache.DB, parent cache.FileID, from, count, batch int) {
	t.Helper()
	ctx := context.Background()
	for start := from; start < from+count; start += batch {
		end := start + batch
		if end > from+count {
			end = from + count
		}
		if err := c.Write(ctx, func(tx *sql.Tx) error {
			for i := start; i < end; i++ {
				b := int64(i)
				ino, nerr := num.Narrow[uint64](chainDepth + 1 + i)
				if nerr != nil {
					return nerr
				}
				size, nerr := num.Narrow[uint64](i)
				if nerr != nil {
					return nerr
				}
				st := vfs.Stat{
					Dev:     testDev,
					Ino:     ino,
					BtimeNs: &b,
					MtimeNs: b,
					Size:    size,
					Kind:    vfs.KindFile,
				}
				if _, err := c.Upsert(ctx, tx, testShare, parent, "f"+strconv.Itoa(i), st); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			t.Fatalf("populating at %d: %v", start, err)
		}
	}
}

// report prints one line per phase, prefixed so that the run's output can be
// read without the surrounding test noise.
func report(t *testing.T, what string, n int, elapsed time.Duration) {
	t.Helper()
	t.Logf("MEASURE %-42s %9d in %8.2fs = %9.0f/s", what, n, elapsed.Seconds(), rate(n, elapsed))
}

func rate(n int, elapsed time.Duration) float64 {
	if elapsed <= 0 {
		return 0
	}
	return float64(n) / elapsed.Seconds()
}

func envRows(t *testing.T, name string) int {
	t.Helper()
	raw := os.Getenv(name)
	if raw == "" {
		return 0
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		t.Fatalf("%s=%q is not a row count", name, raw)
	}
	return n
}
