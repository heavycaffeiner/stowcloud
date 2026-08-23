//go:build linux

package store

import (
	"context"
	"path/filepath"
	"testing"
)

// The guard trips, blocks writes and clears again.
//
// dbfile could always refuse a write that grows a file, and nothing sampled
// the volume to decide it should: SetWritesBlocked was called only by tests,
// so the guard could not trip in a running server and the health reason for it
// could not be reported. This is the sampler that closes that.
func TestTheSizeGuardTripsAndClears(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir(), Options{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if cerr := s.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	})

	// A floor above every plausible free space, so it always trips.
	const impossible = 1 << 62
	st, err := s.Sample(ctx, GuardConfig{MinFreeBytes: impossible})
	if err != nil {
		t.Fatalf("sampling: %v", err)
	}
	if !st.Blocked {
		t.Fatal("a floor of 4 exabytes did not trip the guard")
	}
	if !s.WritesBlocked() {
		t.Error("the guard tripped and the databases still accept writes")
	}
	if st.Reason == "" {
		t.Error("a tripped guard names no reason")
	}

	// And a floor of one byte clears it: the sampler has to be able to
	// recover, or a volume that filled once stays blocked until a restart.
	st, err = s.Sample(ctx, GuardConfig{MinFreeBytes: 1})
	if err != nil {
		t.Fatalf("sampling: %v", err)
	}
	if st.Blocked {
		t.Fatal("a floor of one byte kept the guard tripped")
	}
	if s.WritesBlocked() {
		t.Error("the guard cleared and the databases still refuse writes")
	}
}

// The size ceiling is the other bound, and it reads the databases rather than
// the volume.
func TestTheSizeCeilingTrips(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir(), Options{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if cerr := s.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	})

	st, err := s.Sample(ctx, GuardConfig{MaxBytes: 1})
	if err != nil {
		t.Fatalf("sampling: %v", err)
	}
	if !st.Blocked {
		t.Fatalf("a one-byte ceiling did not trip against %d bytes of database", st.StoreBytes)
	}
	if st.StoreBytes == 0 {
		t.Error("the sample reports no database size at all")
	}
}

// Off is off. Neither bound set means the guard never runs and never blocks,
// which is the shipped default and the whole of the fail-open stance for the
// user's own data.
func TestTheGuardIsOffByDefault(t *testing.T) {
	var cfg GuardConfig
	if cfg.Enabled() {
		t.Fatal("a zero GuardConfig reports itself as enabled")
	}

	s, err := Open(t.TempDir(), Options{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if cerr := s.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	})

	// RunGuard returns immediately rather than sampling on a timer, so this is
	// a plain call: a background context and no cancellation, which would hang
	// forever if the disabled case were not the early return it claims to be.
	s.RunGuard(context.Background(), cfg, nil)

	if s.WritesBlocked() {
		t.Error("a disabled guard blocked writes")
	}
}

// The store's own path is what gets sampled, not the working directory.
func TestTheGuardSamplesTheDataDirectory(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, Options{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if cerr := s.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	})

	if s.dir != dir {
		t.Fatalf("the store samples %q, want the data directory %q", s.dir, dir)
	}
	if _, serr := s.Sample(context.Background(), GuardConfig{MinFreeBytes: 1}); serr != nil {
		t.Fatalf("sampling %s: %v", filepath.Base(dir), serr)
	}
}
