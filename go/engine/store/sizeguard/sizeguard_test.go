//go:build linux

package sizeguard_test

import (
	"context"
	"errors"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/task"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/sizeguard"
)

// fakeFile stands in for a database. The sampling decision is what is under
// test here, and a real file cannot be made to report an arbitrary size
// without writing gigabytes.
type fakeFile struct {
	mu      sync.Mutex
	size    int64
	sizeErr error
	blocked bool
	// sets counts how many times the flag was written, so a test can tell a
	// file that was moved from one that happened to already be right.
	sets int
}

func (f *fakeFile) SizeBytes(context.Context) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.size, f.sizeErr
}

func (f *fakeFile) SetWritesBlocked(b bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.blocked = b
	f.sets++
}

func (f *fakeFile) WritesBlocked() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.blocked
}

// An unconfigured guard blocks nothing. This is the default, and it is the
// difference between an instance that uses more disk than expected and one
// that stops accepting writes.
func TestAnUnconfiguredGuardNeverBlocks(t *testing.T) {
	t.Parallel()

	f := &fakeFile{size: 1 << 40}
	g := sizeguard.New(t.TempDir(), []sizeguard.File{f})

	st, err := g.Sample(context.Background(), sizeguard.Config{})
	if err != nil {
		t.Fatalf("sampling: %v", err)
	}
	if st.Blocked || f.WritesBlocked() {
		t.Error("an unconfigured guard blocked writes")
	}
}

// The ceiling trips on the databases' own size, and says which bound it was.
func TestTheCeilingTripsOnTheDatabaseSize(t *testing.T) {
	t.Parallel()

	f := &fakeFile{size: 5000}
	g := sizeguard.New(t.TempDir(), []sizeguard.File{f})

	st, err := g.Sample(context.Background(), sizeguard.Config{MaxBytes: 4096})
	if err != nil {
		t.Fatalf("sampling: %v", err)
	}
	if !st.Blocked {
		t.Fatal("a store over its ceiling was not blocked")
	}
	if !f.WritesBlocked() {
		t.Error("the file was not moved")
	}
	if st.Reason == "" {
		t.Error("the state names no reason")
	}
	if st.StoreBytes != 5000 {
		t.Errorf("the sample reports %d bytes, want 5000", st.StoreBytes)
	}
}

// Under the ceiling nothing is blocked, and a guard that had tripped releases.
// Without the release a volume that recovered stays refusing writes forever.
func TestRecoveringReleasesTheBlock(t *testing.T) {
	t.Parallel()

	f := &fakeFile{size: 5000}
	g := sizeguard.New(t.TempDir(), []sizeguard.File{f})
	ctx := context.Background()
	cfg := sizeguard.Config{MaxBytes: 4096}

	if _, err := g.Sample(ctx, cfg); err != nil {
		t.Fatalf("sampling: %v", err)
	}
	if !f.WritesBlocked() {
		t.Fatal("the guard did not trip")
	}

	f.mu.Lock()
	f.size = 100
	f.mu.Unlock()

	if _, err := g.Sample(ctx, cfg); err != nil {
		t.Fatalf("sampling: %v", err)
	}
	if f.WritesBlocked() {
		t.Error("the guard stayed tripped after the store shrank")
	}
}

// The floor trips on free space. A real volume is never nearly full on
// demand, so the bound is set past what any filesystem reports.
func TestTheFloorTripsOnFreeSpace(t *testing.T) {
	t.Parallel()

	f := &fakeFile{}
	g := sizeguard.New(t.TempDir(), []sizeguard.File{f})

	st, err := g.Sample(context.Background(), sizeguard.Config{MinFreeBytes: math.MaxUint64 / 2})
	if err != nil {
		t.Fatalf("sampling: %v", err)
	}
	if !st.Blocked {
		t.Fatal("a floor above the whole volume did not trip")
	}
	if st.AvailableBytes == 0 {
		t.Error("the sample reports no available space at all")
	}
}

// The floor is measured against writable space, and the boundary is exact.
//
// The probe is replaced because no volume a test runs on has a root reserve:
// there Bavail and Bfree are the same number, so counting the wrong one is
// invisible. On a volume that does reserve blocks, counting them promises room
// ENOSPC then refuses, and the guard fails to fire on the volume it was
// configured for.
func TestTheFloorIsMeasuredAgainstWritableSpace(t *testing.T) {
	t.Parallel()

	const writable = 1000

	for _, c := range []struct {
		name  string
		floor uint64
		want  bool
	}{
		{"below the floor", writable + 1, true},
		{"exactly at the floor", writable, false},
		{"above the floor", writable - 1, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f := &fakeFile{}
			g := sizeguard.New(t.TempDir(), []sizeguard.File{f})
			g.SetAvailable(func(string) (uint64, error) { return writable, nil })

			st, err := g.Sample(context.Background(), sizeguard.Config{MinFreeBytes: c.floor})
			if err != nil {
				t.Fatalf("sampling: %v", err)
			}
			if st.Blocked != c.want {
				t.Errorf("blocked=%v with %d writable against a floor of %d, want %v",
					st.Blocked, writable, c.floor, c.want)
			}
			if st.AvailableBytes != writable {
				t.Errorf("the sample reports %d available, want %d", st.AvailableBytes, writable)
			}
		})
	}
}

// The probe counts writable blocks and leaves the reserve alone.
//
// Handed a volume that holds blocks back for root, which no filesystem a test
// runs on actually does: there the two counts are equal and reading the wrong
// one cannot be seen. Comparing two live statfs calls instead would race the
// machine's own free space, which is a flake rather than a check.
func TestTheProbeCountsWritableBlocksOnly(t *testing.T) {
	t.Parallel()

	const (
		blockSize   = 4096
		blocksFree  = 1000 // what the filesystem has spare
		blocksAvail = 900  // what an unprivileged writer may use
	)

	got := sizeguard.AvailableFrom(blockSize, blocksFree, blocksAvail)

	if want := uint64(blocksAvail * blockSize); got != want {
		t.Errorf("the probe reports %d, want the writable %d", got, want)
	}
	if reserved := uint64(blocksFree * blockSize); got == reserved {
		t.Errorf("the probe counted the %d bytes reserved for root", reserved-uint64(blocksAvail*blockSize))
	}
}

// The real probe agrees with the filesystem it was pointed at.
//
// One statfs, not two: reading the volume twice and comparing the figures races
// whatever else is writing to it.
func TestTheProbeReadsTheVolumeItWasGiven(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	got, err := sizeguard.AvailableBytes(dir)
	if err != nil {
		t.Fatalf("probing: %v", err)
	}
	if got == 0 {
		t.Error("the probe reports no space at all on a writable directory")
	}
}

// A failed free-space probe does not block, the same as a failed size read.
func TestAFailedFreeSpaceProbeDoesNotBlock(t *testing.T) {
	t.Parallel()

	f := &fakeFile{}
	g := sizeguard.New(t.TempDir(), []sizeguard.File{f})
	g.SetAvailable(func(string) (uint64, error) { return 0, errors.New("the volume vanished") })

	if _, err := g.Sample(context.Background(), sizeguard.Config{MinFreeBytes: 1}); err == nil {
		t.Error("a failed probe was reported as a successful sample")
	}
	if f.WritesBlocked() {
		t.Error("a failed probe blocked writes")
	}
}

// Every file moves together. One blocked database and one writable is a
// half-writable store, which nothing above this is written to handle.
func TestEveryFileMovesTogether(t *testing.T) {
	t.Parallel()

	a := &fakeFile{size: 3000}
	b := &fakeFile{size: 3000}
	g := sizeguard.New(t.TempDir(), []sizeguard.File{a, b})

	if _, err := g.Sample(context.Background(), sizeguard.Config{MaxBytes: 4096}); err != nil {
		t.Fatalf("sampling: %v", err)
	}
	if !a.WritesBlocked() || !b.WritesBlocked() {
		t.Errorf("the files disagree: a=%v b=%v", a.WritesBlocked(), b.WritesBlocked())
	}
}

// The ceiling is the sum. Two files under it individually and over it together
// is exactly the case a per-file check would miss.
func TestTheCeilingIsTheSumNotTheLargest(t *testing.T) {
	t.Parallel()

	a := &fakeFile{size: 3000}
	b := &fakeFile{size: 3000}
	g := sizeguard.New(t.TempDir(), []sizeguard.File{a, b})

	st, err := g.Sample(context.Background(), sizeguard.Config{MaxBytes: 4096})
	if err != nil {
		t.Fatalf("sampling: %v", err)
	}
	if st.StoreBytes != 6000 {
		t.Errorf("the total is %d, want 6000", st.StoreBytes)
	}
	if !st.Blocked {
		t.Error("two files over the ceiling together were not blocked")
	}
}

// A probe that could not run does not block. Refusing every write because a
// measurement failed is the outage this guard exists to prevent.
func TestAFailedMeasurementDoesNotBlock(t *testing.T) {
	t.Parallel()

	f := &fakeFile{sizeErr: errors.New("the file is gone")}
	g := sizeguard.New(t.TempDir(), []sizeguard.File{f})

	if _, err := g.Sample(context.Background(), sizeguard.Config{MaxBytes: 1}); err == nil {
		t.Error("a failed measurement was reported as a successful sample")
	}
	if f.WritesBlocked() {
		t.Error("a failed measurement blocked writes")
	}
}

// Run over an unconfigured guard returns rather than ticking forever.
func TestRunReturnsWhenUnconfigured(t *testing.T) {
	t.Parallel()

	g := sizeguard.New(t.TempDir(), []sizeguard.File{&fakeFile{}})
	done := make(chan struct{})
	task.Go(context.Background(), "sizeguard-unconfigured", func() {
		g.Run(context.Background(), sizeguard.Config{}, nil)
		close(done)
	})

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Error("an unconfigured guard did not return")
	}
}

// Run samples once before its first tick, so a server started on a volume that
// is already full blocks immediately rather than after one interval of
// accepting writes it cannot keep.
func TestRunSamplesBeforeTheFirstTick(t *testing.T) {
	t.Parallel()

	f := &fakeFile{size: 5000}
	g := sizeguard.New(t.TempDir(), []sizeguard.File{f})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	changed := make(chan sizeguard.State, 1)
	task.Go(ctx, "sizeguard-first-sample", func() {
		g.Run(ctx, sizeguard.Config{MaxBytes: 4096, Interval: time.Hour}, func(st sizeguard.State) {
			select {
			case changed <- st:
			default:
			}
		})
	})

	select {
	case st := <-changed:
		if !st.Blocked {
			t.Error("the first sample reported not blocked")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no sample arrived before the first tick")
	}
}

// onChange fires on a transition and not on every sample, so a caller can log
// it without being told the same thing every interval.
func TestOnChangeFiresOnlyOnATransition(t *testing.T) {
	t.Parallel()

	f := &fakeFile{size: 5000}
	g := sizeguard.New(t.TempDir(), []sizeguard.File{f})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var mu sync.Mutex
	var calls int
	task.Go(ctx, "sizeguard-transitions", func() {
		g.Run(ctx, sizeguard.Config{MaxBytes: 4096, Interval: 10 * time.Millisecond}, func(sizeguard.State) {
			mu.Lock()
			calls++
			mu.Unlock()
		})
	})

	time.Sleep(200 * time.Millisecond)
	cancel()

	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Errorf("onChange fired %d times across many samples, want 1", calls)
	}
}

// Blocked reports the files' own state, which is what the health surface asks.
func TestBlockedReportsTheFiles(t *testing.T) {
	t.Parallel()

	f := &fakeFile{size: 5000}
	g := sizeguard.New(t.TempDir(), []sizeguard.File{f})

	if g.Blocked() {
		t.Error("a fresh guard reports blocked")
	}
	if _, err := g.Sample(context.Background(), sizeguard.Config{MaxBytes: 1}); err != nil {
		t.Fatalf("sampling: %v", err)
	}
	if !g.Blocked() {
		t.Error("a tripped guard reports writable")
	}
}
