//go:build linux

package store

import (
	"context"
	"time"

	"golang.org/x/sys/unix"
)

// GuardConfig, GuardState and the default interval sit in sizeguard_config.go,
// which carries no build tag: the settings document names them on every
// target, and only the sampling below needs statfs.

// The size guard: the thing that samples free space and trips the block.
//
// dbfile owns the flag and refuses the writes that grow a file; it never
// touches the filesystem, and its own comment says the caller is "whatever
// samples free space on the volume this file sits on". Nothing was. The flag
// existed, the refusal existed, the health reason existed, and only the tests
// ever set it: a guard that could not trip and a degradation that could not be
// reported.
//
// Off unless configured, which is deliberate rather than an oversight: an
// instance that stops accepting writes because a cache grew is worse than one
// that uses more disk than expected. Hardening fails closed because a sandbox
// that silently did not apply is a lie about the product; the user's own data
// fails open because refusing writes turns an inconvenience into an outage.

// Sample measures the volume and the databases and applies the bounds.
//
// Both files are moved together: a guard that blocked one database and not the
// others would leave the store half-writable, which is a shape nothing above
// it is written to handle.
func (s *Store) Sample(ctx context.Context, cfg GuardConfig) (GuardState, error) {
	var st GuardState

	if cfg.MinFreeBytes > 0 {
		var sfs unix.Statfs_t
		if err := unix.Statfs(s.dir, &sfs); err != nil {
			// A probe that could not run is not a reason to block: refusing
			// every write because statfs failed is the outage this guard is
			// meant to prevent.
			return st, err
		}
		// Bavail, not Bfree: the blocks the filesystem reserves for root are
		// not ours to write into, and counting them promises room that ENOSPC
		// refuses.
		st.AvailableBytes = sfs.Bavail * uint64(sfs.Bsize) //nolint:gosec // Bsize is a positive block size from the kernel.
		if st.AvailableBytes < cfg.MinFreeBytes {
			st.Blocked = true
			st.Reason = "free space below the floor"
		}
	}

	if cfg.MaxBytes > 0 {
		total, err := s.sizeBytes(ctx)
		if err != nil {
			return st, err
		}
		st.StoreBytes = total
		if total > cfg.MaxBytes {
			st.Blocked = true
			st.Reason = "the databases exceed the ceiling"
		}
	}

	s.setBlocked(st.Blocked)
	return st, nil
}

// sizeBytes is the three databases together.
func (s *Store) sizeBytes(ctx context.Context) (uint64, error) {
	var total uint64
	for _, f := range s.files() {
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

// setBlocked moves every file together.
func (s *Store) setBlocked(blocked bool) {
	for _, f := range s.files() {
		f.SetWritesBlocked(blocked)
	}
}

// WritesBlocked reports the guard, for the health surface.
func (s *Store) WritesBlocked() bool {
	for _, f := range s.files() {
		if f.WritesBlocked() {
			return true
		}
	}
	return false
}

// RunGuard samples until the context ends. It returns immediately when neither
// bound is set, which is the default.
//
// Each sample's result goes to onChange only when the blocked state moves, so
// a caller can log a transition and report health without being told the same
// thing every interval.
func (s *Store) RunGuard(ctx context.Context, cfg GuardConfig, onChange func(GuardState)) {
	if !cfg.Enabled() {
		return
	}
	interval := cfg.Interval
	if interval <= 0 {
		interval = defaultGuardInterval
	}
	t := time.NewTicker(interval)
	defer t.Stop()

	was := false
	for {
		// Sampled once before the first tick, so a server started on a volume
		// that is already full blocks immediately rather than after one
		// interval of accepting writes it cannot keep.
		st, err := s.Sample(ctx, cfg)
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
