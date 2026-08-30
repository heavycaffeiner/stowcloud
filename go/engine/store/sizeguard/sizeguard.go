//go:build linux

// Package sizeguard samples free space and trips the write block.
//
// dbfile owns the flag and refuses the writes that would grow a file. It never
// touches the filesystem itself, and its own comment names the missing half as
// "whatever samples free space on the volume this file sits on". This is that
// half: without it the flag exists, the refusal exists, the health reason
// exists, and nothing can ever set it.
//
// Off unless configured, which is deliberate. Hardening fails closed because a
// sandbox that silently did not apply is a lie about the product. This fails
// open, because refusing writes over a cache that grew turns an inconvenience
// into an outage.
package sizeguard

import (
	"context"
	"time"

	"golang.org/x/sys/unix"
)

// DefaultInterval is a compromise: often enough that a volume filling during a
// large upload is noticed before writes start failing with ENOSPC, rare enough
// that it is not a statfs per second forever.
const DefaultInterval = 30 * time.Second

// Config is what the guard is asked for.
//
// Plain numbers rather than the settings type that carries them. This package
// is in the store tier and the settings live in the service tier, so taking
// that type would be an import the wrong way up.
type Config struct {
	// MinFreeBytes fires once the volume beneath the databases reports less
	// writable space than this. Unset, and therefore inactive, at zero.
	MinFreeBytes uint64
	// MaxBytes trips it when the databases together exceed this. Zero is off.
	// It bounds this server's own footprint, where MinFreeBytes bounds the
	// volume it shares with everything else.
	MaxBytes uint64
	// Interval is how often the volume is sampled. Zero takes DefaultInterval.
	Interval time.Duration
}

// Enabled answers whether any bound was configured at all.
func (c Config) Enabled() bool { return c.MinFreeBytes > 0 || c.MaxBytes > 0 }

// State is one sample, for the caller that reports health.
type State struct {
	// Blocked says whether the databases are currently turning writes away.
	Blocked bool
	// AvailableBytes records the writable space seen by this sample.
	AvailableBytes uint64
	// StoreBytes is what the databases occupied at the last sample.
	StoreBytes uint64
	// Reason names which bound tripped, and is empty when neither did.
	Reason string
}

// File is one database the guard measures and blocks.
//
// An interface so this package does not depend on the database types, and so
// a test can drive the decision without a real file.
type File interface {
	SizeBytes(ctx context.Context) (int64, error)
	SetWritesBlocked(blocked bool)
	WritesBlocked() bool
}

// Guard samples a directory and the databases inside it.
type Guard struct {
	dir   string
	files []File
	// avail reports the volume's writable free space. Replaced only by a
	// test: a real filesystem cannot be asked to report a specific figure,
	// and the reserve that separates Bavail from Bfree is zero on the volumes
	// a test runs on, so nothing else can tell the two apart.
	avail func(dir string) (uint64, error)
}

// New builds a guard over a data directory and the databases it holds.
func New(dir string, files []File) *Guard {
	return &Guard{dir: dir, files: files, avail: availableBytes}
}

// availableBytes is the volume's free space as this guard counts it.
//
// Bavail, not Bfree: the blocks a filesystem reserves for root are not ours to
// write into. Counting them promises room that ENOSPC then refuses, which is
// the guard failing to fire on exactly the volume it was configured for.
func availableBytes(dir string) (uint64, error) {
	var sfs unix.Statfs_t
	if err := unix.Statfs(dir, &sfs); err != nil {
		return 0, err
	}
	return availableFrom(sfs.Bsize, sfs.Bfree, sfs.Bavail), nil
}

// availableFrom is the arithmetic, apart from the syscall that feeds it.
//
// It takes both block counts although it uses only one, because which one it
// uses is the whole correctness of the floor. No volume a test runs on reserves
// anything, so the two are equal there and reading the wrong one is invisible;
// taking both lets a test hand over a volume that does reserve.
func availableFrom(blockSize int64, blocksFree, blocksAvail uint64) uint64 {
	// blocksAvail, not blocksFree. The blocks a filesystem holds back for root
	// are not ours to write into, and counting them promises room that ENOSPC
	// then refuses, which is the guard failing to fire on the volume it was
	// configured for.
	return blocksAvail * uint64(blockSize) //nolint:gosec // a block size from the kernel is positive.
}

// Sample reads the volume and the databases, then applies both bounds.
//
// The decision reaches every file at once. Blocking a subset would leave the
// store partly writable, and no layer above this one is built to cope with
// that arrangement.
func (g *Guard) Sample(ctx context.Context, cfg Config) (State, error) {
	var st State

	if cfg.MinFreeBytes > 0 {
		available, err := g.avail(g.dir)
		if err != nil {
			// A probe that could not run is not a reason to block. Refusing
			// every write because statfs failed is the outage this exists to
			// prevent.
			return st, err
		}
		st.AvailableBytes = available
		if st.AvailableBytes < cfg.MinFreeBytes {
			st.Blocked = true
			st.Reason = "free space below the floor"
		}
	}

	if cfg.MaxBytes > 0 {
		total, err := g.sizeBytes(ctx)
		if err != nil {
			return st, err
		}
		st.StoreBytes = total
		if total > cfg.MaxBytes {
			st.Blocked = true
			st.Reason = "the databases exceed the ceiling"
		}
	}

	g.setBlocked(st.Blocked)
	return st, nil
}

// Blocked reports whether any file is currently refusing writes.
//
// Any rather than all: the files move together, so a disagreement means a
// sample failed partway, and the safe reading of that is blocked.
func (g *Guard) Blocked() bool {
	for _, f := range g.files {
		if f.WritesBlocked() {
			return true
		}
	}
	return false
}

// Run keeps sampling until the context is done, and returns straight away when
// nothing was configured, which is how a deployment runs by default.
//
// onChange is called only when the blocked state moves, so a caller can log a
// transition without being told the same thing every interval.
func (g *Guard) Run(ctx context.Context, cfg Config, onChange func(State)) {
	if !cfg.Enabled() {
		return
	}
	interval := cfg.Interval
	if interval <= 0 {
		interval = DefaultInterval
	}
	t := time.NewTicker(interval)
	defer t.Stop()

	was := false
	for {
		// Sampled before the first tick, so a server started on a volume that
		// is already full blocks immediately rather than after one interval of
		// accepting writes it cannot keep.
		st, err := g.Sample(ctx, cfg)
		if err == nil && st.Blocked != was {
			was = st.Blocked
			if onChange != nil {
				onChange(st)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// sizeBytes is every database together.
func (g *Guard) sizeBytes(ctx context.Context) (uint64, error) {
	var total uint64
	for _, f := range g.files {
		n, err := f.SizeBytes(ctx)
		if err != nil {
			return 0, err
		}
		if n > 0 {
			total += uint64(n)
		}
	}
	return total, nil
}

// setBlocked applies one decision across the whole set.
func (g *Guard) setBlocked(blocked bool) {
	for _, f := range g.files {
		f.SetWritesBlocked(blocked)
	}
}
