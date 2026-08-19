//go:build linux

package upload

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/internal/limits"
)

// The chunk floor's whole job is to distinguish "an admin set this" from "it
// fell back to the config file". Collapsing them makes both the same pair of
// integers and the settings screen has nothing left to report.

func TestChunkSettingsStartUnoverriddenAndSaySo(t *testing.T) {
	f := newFixture(t)
	if f.engine.Settings().Overridden() {
		t.Fatal("a fresh engine reports an admin override it never had")
	}
	minBytes, defaultBytes := f.engine.Settings().Snapshot()
	if minBytes != limits.UploadChunkMinDefault || defaultBytes != limits.UploadChunkSizeDefault {
		t.Fatalf("the seeded settings are %d and %d, want %d and %d",
			minBytes, defaultBytes, limits.UploadChunkMinDefault, limits.UploadChunkSizeDefault)
	}
}

func TestAnAdminWriteIsRememberedAcrossARestart(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	wantMin := uint64(limits.UploadChunkFloor) * 2
	wantDefault := wantMin * 2

	if err := f.engine.SetChunkSettings(ctx, wantMin, wantDefault); err != nil {
		t.Fatalf("SetChunkSettings: %v", err)
	}
	if !f.engine.Settings().Overridden() {
		t.Fatal("an admin write did not record that it happened")
	}

	// A second engine over the same store is what a restart is.
	restarted, err := New(ctx, f.core, f.store.State(), Options{Clock: f.engine.clk})
	if err != nil {
		t.Fatalf("reopening the engine: %v", err)
	}
	gotMin, gotDefault := restarted.Settings().Snapshot()
	if gotMin != wantMin || gotDefault != wantDefault {
		t.Fatalf("after a restart the settings are %d and %d, want %d and %d",
			gotMin, gotDefault, wantMin, wantDefault)
	}
	if !restarted.Settings().Overridden() {
		t.Fatal("after a restart the admin write reads as a config-file fallback")
	}
}

// The floor is compiled in, so no config value and no admin write can go under
// it. That is the difference between a floor and a default.
func TestTheHardFloorRefusesAnAdminWriteAndClampsAConfigSeed(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	err := f.engine.SetChunkSettings(ctx, limits.UploadChunkFloor-1, limits.UploadChunkSizeDefault)
	if !errors.Is(err, ErrBadRequest) {
		t.Fatalf("a write under the floor returned %v, want ErrBadRequest", err)
	}
	if f.engine.Settings().Overridden() {
		t.Fatal("a refused write recorded itself as an admin override")
	}

	// A default under the minimum is the other half of the same rule.
	err = f.engine.SetChunkSettings(ctx, uint64(limits.UploadChunkFloor)*2, limits.UploadChunkFloor)
	if !errors.Is(err, ErrBadRequest) {
		t.Fatalf("a default under the minimum returned %v, want ErrBadRequest", err)
	}

	// A config seed under the floor is clamped rather than refused: a server
	// must still boot on a file somebody typed a small number into.
	e, nerr := New(ctx, f.core, f.store.State(), Options{Clock: f.engine.clk, ChunkMin: 4, ChunkDefault: 8})
	if nerr != nil {
		t.Fatalf("upload.New with a low seed: %v", nerr)
	}
	if got := e.Settings().Min(); got != limits.UploadChunkFloor {
		t.Fatalf("a config seed of four bytes produced a floor of %d, want %d",
			got, limits.UploadChunkFloor)
	}
}

// A session's floor is snapshotted at creation, so an admin raising it cannot
// retroactively refuse a chunk that was legal when the session started.
func TestRaisingTheFloorDoesNotReachASessionAlreadyInFlight(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	s := f.create(t, "file.bin", uint64(limits.UploadChunkFloor)*3, SessionSpec{})

	if err := f.engine.SetChunkSettings(ctx,
		uint64(limits.UploadChunkFloor)*4, uint64(limits.UploadChunkFloor)*4); err != nil {
		t.Fatalf("SetChunkSettings: %v", err)
	}

	// Legal under the floor this session was created with, and under the new
	// live one it would not be.
	body := make([]byte, limits.UploadChunkFloor)
	if _, err := f.engine.PatchAt(ctx, f.root, s.ID, testUser, 0, bytes.NewReader(body), nil); err != nil {
		t.Fatalf("a chunk legal at creation was refused after the floor moved: %v", err)
	}
}
