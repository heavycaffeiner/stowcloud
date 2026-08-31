//go:build linux

package upload

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/limits"
)

// The wire spelling round-trips, an unknown algorithm refuses rather than
// defaulting, and a digest that cannot be the algorithm's output refuses at
// parse: a short one would compare against a truncation of the real digest
// and pass.
func TestChecksumParsing(t *testing.T) {
	sum, err := Sum(AlgoCRC32C, []byte("hello"))
	if err != nil {
		t.Fatalf("Sum: %v", err)
	}
	wire := Checksum{Algo: AlgoCRC32C, Digest: sum}.String()
	back, err := ParseChecksum(wire)
	if err != nil {
		t.Fatalf("ParseChecksum(%q): %v", wire, err)
	}
	if back.Algo != AlgoCRC32C || !bytes.Equal(back.Digest, sum) {
		t.Fatalf("the checksum round-tripped as %+v", back)
	}

	if _, aerr := ParseAlgo("md5"); !errors.Is(aerr, ErrUnknownAlgo) {
		t.Fatalf("an unoffered algorithm returned %v", aerr)
	}
	for name, wire := range map[string]string{
		"no digest":                  "crc32c",
		"not base64":                 "crc32c ...",
		"a short digest":             "crc32c AAA=",
		"a blake3 length for crc32c": "crc32c " + b64Of(make([]byte, 32)),
		"a crc32c length for blake3": "blake3 " + b64Of(make([]byte, 4)),
	} {
		if _, perr := ParseChecksum(wire); perr == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
}

// Known answers, so a change to either algorithm is caught against a fixed
// vector rather than against this tree's own arithmetic.
func TestKnownDigests(t *testing.T) {
	crc, err := Sum(AlgoCRC32C, []byte("123456789"))
	if err != nil {
		t.Fatalf("Sum: %v", err)
	}
	// The check value the Castagnoli polynomial is specified with.
	if got := bytesToHex(crc); got != "e3069283" {
		t.Fatalf("the crc32c check value is %s, want e3069283", got)
	}

	b3, err := Sum(AlgoBLAKE3, []byte(""))
	if err != nil {
		t.Fatalf("Sum: %v", err)
	}
	const emptyBlake3 = "af1349b9f5f9a1a6a0404dea36dcc9499bcb25c9adc112b7cc9a93cae41f3262"
	if got := bytesToHex(b3); got != emptyBlake3 {
		t.Fatalf("the blake3 of the empty string is %s", got)
	}
}

func TestFinalizePublishesThroughTheCore(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	const chunk = limits.UploadChunkFloor
	body := chunkOf(0, chunk)
	s := f.create(t, "done.bin", uint64(chunk), SessionSpec{})
	f.patch(t, s.ID, 0, body)

	entry, err := f.engine.Finalize(ctx, f.resolve(t, "done.bin"), s.ID)
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if entry.Name != "done.bin" || entry.Size != uint64(chunk) {
		t.Fatalf("the published entry is %+v", entry)
	}
	if got := readPublished(t, f, "done.bin", len(body)); !bytes.Equal(got, body) {
		t.Fatal("the published bytes are not what was sent")
	}

	// The row, the handle and the bookkeeping lock are all gone.
	if _, gerr := f.engine.Get(ctx, s.ID, testUser); !errors.Is(gerr, ErrNotFound) {
		t.Fatalf("the session survived the publish: %v", gerr)
	}
	if n := f.engine.rowLockCount(); n != 0 {
		t.Fatalf("%d bookkeeping locks survived the publish", n)
	}
	// The part file is gone with it: publication is a rename, so nothing is
	// left beside the destination.
	part, perr := vfs.RootPath().JoinControl(partName(s.ID))
	if perr != nil {
		t.Fatalf("naming the part file: %v", perr)
	}
	if _, serr := f.root(t).Stat(part); !errors.Is(serr, vfs.ErrNotFound) {
		t.Fatalf("the part file survived the publish: %v", serr)
	}
}

// A session is bound to where it was created for. A finalize resolved
// somewhere else is refused before anything is touched.
func TestFinalizeRefusesAnotherDestinationBeforeAnyEffect(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	const chunk = limits.UploadChunkFloor
	s := f.create(t, "intended.bin", uint64(chunk), SessionSpec{})
	f.patch(t, s.ID, 0, chunkOf(0, chunk))

	_, err := f.engine.Finalize(ctx, f.resolve(t, "elsewhere.bin"), s.ID)
	if !errors.Is(err, ErrBadRequest) {
		t.Fatalf("a finalize at another destination returned %v", err)
	}
	// Nothing moved: the part file is still there and nothing was published.
	part, perr := vfs.RootPath().JoinControl(partName(s.ID))
	if perr != nil {
		t.Fatalf("naming the part file: %v", perr)
	}
	if _, serr := f.root(t).Stat(part); serr != nil {
		t.Fatalf("the part file is gone after a refused finalize: %v", serr)
	}
	elsewhere, perr := vfs.ParseSafePath("elsewhere.bin")
	if perr != nil {
		t.Fatalf("parsing: %v", perr)
	}
	if _, serr := f.root(t).Stat(elsewhere); !errors.Is(serr, vfs.ErrNotFound) {
		t.Fatal("the refused finalize published something")
	}
}

// An incomplete set refuses and names the holes, so the client resends the
// ranges rather than the file.
func TestFinalizeRefusesHolesAndNamesThem(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	const chunk = limits.UploadChunkFloor
	s := f.create(t, "holed.bin", uint64(chunk*3), SessionSpec{RandomAccess: true})
	f.patch(t, s.ID, 0, chunkOf(0, chunk))
	f.patch(t, s.ID, uint64(chunk*2), chunkOf(uint64(chunk*2), chunk))

	_, err := f.engine.Finalize(ctx, f.resolve(t, "holed.bin"), s.ID)
	var incomplete *IncompleteError
	if !errors.As(err, &incomplete) {
		t.Fatalf("a finalize over a hole returned %v", err)
	}
	if len(incomplete.Missing) != 1 || incomplete.Missing[0] != (Range{uint64(chunk), uint64(chunk * 2)}) {
		t.Fatalf("the refusal names %v", incomplete.Missing)
	}
}

// Verification failure leaves the part file in place and the session
// recoverable: the client knows whether to resend a range or start again, and
// discarding its bytes here would decide that for it.
func TestAFailedWholeFileVerificationLeavesTheSessionResumable(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	const chunk = limits.UploadChunkFloor
	body := chunkOf(0, chunk)

	wrong, err := Sum(AlgoBLAKE3, []byte("a different file"))
	if err != nil {
		t.Fatalf("Sum: %v", err)
	}
	s := f.create(t, "verified.bin", uint64(chunk), SessionSpec{
		Meta: Meta{Verify: &Verify{Algo: AlgoBLAKE3, Digest: wrong}},
	})
	f.patch(t, s.ID, 0, body)

	if _, ferr := f.engine.Finalize(ctx, f.resolve(t, "verified.bin"), s.ID); !errors.Is(ferr, ErrVerify) {
		t.Fatalf("a wrong whole-file digest returned %v", ferr)
	}
	// The part file is still there and the session still exists.
	part, perr := vfs.RootPath().JoinControl(partName(s.ID))
	if perr != nil {
		t.Fatalf("naming the part file: %v", perr)
	}
	if _, serr := f.root(t).Stat(part); serr != nil {
		t.Fatalf("the part file is gone after a failed verification: %v", serr)
	}
	got, gerr := f.engine.Get(ctx, s.ID, testUser)
	if gerr != nil {
		t.Fatalf("the session is gone after a failed verification: %v", gerr)
	}
	if got.State != StateFinalizing {
		t.Fatalf("the session reads as state %d, want finalizing", got.State)
	}
	// Nothing was published.
	dest, perr := vfs.ParseSafePath("verified.bin")
	if perr != nil {
		t.Fatalf("parsing: %v", perr)
	}
	if _, serr := f.root(t).Stat(dest); !errors.Is(serr, vfs.ErrNotFound) {
		t.Fatal("a failed verification published the file")
	}
}

func TestAMatchingWholeFileDigestPublishes(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	const chunk = limits.UploadChunkFloor
	body := chunkOf(0, chunk)
	digest, err := Sum(AlgoBLAKE3, body)
	if err != nil {
		t.Fatalf("Sum: %v", err)
	}

	s := f.create(t, "good.bin", uint64(chunk), SessionSpec{
		Meta: Meta{Verify: &Verify{Algo: AlgoBLAKE3, Digest: digest}},
	})
	f.patch(t, s.ID, 0, body)
	if _, ferr := f.engine.Finalize(ctx, f.resolve(t, "good.bin"), s.ID); ferr != nil {
		t.Fatalf("Finalize: %v", ferr)
	}
}

// A finalizing session is not receiving, and the sweep leaves it alone: a
// long assembly must not be collected halfway through its own publish.
func TestAFinalizingSessionSurvivesTheSweep(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	const chunk = limits.UploadChunkFloor
	body := chunkOf(0, chunk)
	wrong, err := Sum(AlgoBLAKE3, []byte("elsewhere"))
	if err != nil {
		t.Fatalf("Sum: %v", err)
	}
	s := f.create(t, "slow.bin", uint64(chunk), SessionSpec{
		Meta: Meta{Verify: &Verify{Algo: AlgoBLAKE3, Digest: wrong}},
	})
	f.patch(t, s.ID, 0, body)
	// The failed verification leaves the session in the finalizing state.
	if _, ferr := f.engine.Finalize(ctx, f.resolve(t, "slow.bin"), s.ID); !errors.Is(ferr, ErrVerify) {
		t.Fatalf("Finalize returned %v", ferr)
	}

	f.clk.advance(limits.UploadSessionTTL * 2)
	rep, serr := f.engine.Sweep(ctx)
	if serr != nil {
		t.Fatalf("Sweep: %v", serr)
	}
	if rep.ExpiredSessions != 0 {
		t.Fatalf("the sweep took %d finalizing sessions", rep.ExpiredSessions)
	}
	if _, gerr := f.engine.Get(ctx, s.ID, testUser); gerr != nil {
		t.Fatalf("the finalizing session was swept: %v", gerr)
	}
}

// The ladder: the compiled-in floor beats an administrator's override, which
// beats the configuration seed, and a nil leaves that half alone.
func TestTheSettingsLadder(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)

	below := uint64(limits.UploadChunkFloor / 2)
	if err := f.engine.ApplySettings(ctx, &below, nil); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("a minimum below the floor returned %v", err)
	}

	minimum := uint64(limits.UploadChunkFloor * 2)
	smallDefault := uint64(limits.UploadChunkFloor)
	if err := f.engine.ApplySettings(ctx, &minimum, &smallDefault); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("a default below the minimum returned %v", err)
	}

	if err := f.engine.ApplySettings(ctx, &minimum, nil); err != nil {
		t.Fatalf("ApplySettings: %v", err)
	}
	gotMin, gotDefault := f.engine.Settings().Snapshot()
	if gotMin != minimum {
		t.Fatalf("the minimum is %d, want %d", gotMin, minimum)
	}
	// The default was pulled up to the new minimum rather than left below it:
	// a default the server then refuses is a configuration nobody can use.
	if gotDefault < gotMin {
		t.Fatalf("the default is %d, below the minimum %d", gotDefault, gotMin)
	}
	if !f.engine.Settings().Overridden() {
		t.Fatal("an applied setting does not read as an override")
	}
}

// The per-account bounds refuse with the limit they refused on: "resource
// exhausted" without the name is a refusal an operator cannot act on.
func TestTheAccountSessionBoundRefusesByName(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)

	for i := 0; i < limits.UploadSessionsPerUser; i++ {
		if _, err := f.engine.Create(ctx, f.resolve(t, "file"+itoa(i)+".bin"),
			SessionSpec{}); err != nil {
			t.Fatalf("Create %d: %v", i, err)
		}
	}
	_, err := f.engine.Create(ctx, f.resolve(t, "one-too-many.bin"), SessionSpec{})
	var exhausted *ExhaustedError
	if !errors.As(err, &exhausted) {
		t.Fatalf("the session past the bound returned %v", err)
	}
	if exhausted.Limit == "" {
		t.Fatal("the refusal names no limit")
	}
}

func TestTheReservedBytesBoundRefusesByName(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)

	huge := uint64(limits.UploadReservedBytesPerUser) + 1
	_, err := f.engine.Create(ctx, f.resolve(t, "huge.bin"), SessionSpec{TotalLen: &huge})
	var exhausted *ExhaustedError
	if !errors.As(err, &exhausted) {
		t.Fatalf("a declared length past the account bound returned %v", err)
	}
}
