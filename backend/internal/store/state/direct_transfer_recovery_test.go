package state_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/heavycaffeiner/stowcloud/backend/internal/store/dbfile"
	"github.com/heavycaffeiner/stowcloud/backend/internal/store/state"
)

// fakeCommitProvider models the only provider result that matters here: the
// commit reaches the provider, then the caller sees a timeout. Metadata after
// restart is the provider's durable evidence; Abort must never run once that
// evidence is observed.
type fakeCommitProvider struct {
	size      uint64
	checksum  string
	committed bool
	aborts    int
}

func (p *fakeCommitProvider) Complete() error {
	p.committed = true
	return context.DeadlineExceeded
}

func (p *fakeCommitProvider) Metadata() (uint64, string, string, bool, error) {
	if !p.committed {
		return 0, "", "", false, nil
	}
	return p.size, "published-etag", p.checksum, true, nil
}

func (p *fakeCommitProvider) Abort() { p.aborts++ }

func TestCommitThenTimeoutSurvivesRestartWithoutAbort(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")

	f, err := dbfile.Open(ctx, state.Spec(path))
	if err != nil {
		t.Fatalf("opening state: %v", err)
	}
	d := state.New(f)
	seedRecoveryUser(t, d, 1)
	row := state.DirectTransferReservation{
		ID: "transfer-recovery", Owner: 1, Share: 1, Path: "files/object.bin",
		ObjectKey: "team/object.bin", UploadID: "upload-recovery", ExpectedSize: 7,
		ExpectedChecksum: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		QuotaReservation: 7, CreatedNs: 1, UpdatedNs: 1, ExpiresNs: 2,
		State: state.DirectTransferPending,
	}
	if createErr := d.CreateDirectTransfer(ctx, row); createErr != nil {
		t.Fatalf("creating transfer: %v", createErr)
	}
	provider := &fakeCommitProvider{size: row.ExpectedSize, checksum: row.ExpectedChecksum}
	if beginErr := d.BeginDirectTransferCompletion(ctx, row.ID, row.Owner, 10); beginErr != nil {
		t.Fatalf("recording publication intent: %v", beginErr)
	}
	if commitErr := provider.Complete(); !errors.Is(commitErr, context.DeadlineExceeded) {
		t.Fatalf("fake commit error = %v", commitErr)
	}
	if closeErr := f.Close(); closeErr != nil {
		t.Fatalf("closing before restart: %v", closeErr)
	}

	f, err = dbfile.Open(ctx, state.Spec(path))
	if err != nil {
		t.Fatalf("reopening state: %v", err)
	}
	d = state.New(f)
	t.Cleanup(func() {
		if closeErr := f.Close(); closeErr != nil {
			t.Errorf("closing after recovery: %v", closeErr)
		}
	})

	size, etag, checksum, found, err := provider.Metadata()
	if err != nil || !found || size != row.ExpectedSize || checksum != row.ExpectedChecksum {
		t.Fatalf("provider metadata after restart = %d,%q,%q,%t,%v", size, etag, checksum, found, err)
	}
	if publishErr := d.PublishDirectTransfer(ctx, row.ID, row.Owner, size, etag, checksum, 11); publishErr != nil {
		t.Fatalf("publishing reconciled transfer: %v", publishErr)
	}
	got, err := d.GetDirectTransfer(ctx, row.ID)
	if err != nil {
		t.Fatalf("reading reconciled transfer: %v", err)
	}
	if got.State != state.DirectTransferComplete || !got.ReceiptRecorded || got.ReceiptSize != row.ExpectedSize || got.ReceiptETag != etag || got.ReceiptChecksum != checksum {
		t.Fatalf("reconciled row = %+v", got)
	}
	expired, err := d.ListExpiredDirectTransfers(ctx, 12, 100)
	if err != nil {
		t.Fatalf("listing expired transfers: %v", err)
	}
	if len(expired) != 0 {
		t.Fatalf("published transfer became sweepable: %+v", expired)
	}
	if provider.aborts != 0 {
		t.Fatalf("provider aborts after published recovery: %d", provider.aborts)
	}
}

func seedRecoveryUser(t *testing.T, d *state.DB, id int64) {
	t.Helper()
	if err := d.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(), `INSERT INTO user(id, name, pw_hash, created_ns) VALUES (?, ?, '', 0)`, id, "recovery-user")
		return err
	}); err != nil {
		t.Fatalf("seeding user: %v", err)
	}
}
