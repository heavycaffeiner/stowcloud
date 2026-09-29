package state_test

import (
	"context"
	"testing"

	"github.com/heavycaffeiner/stowcloud/backend/internal/store/state"
)

// seedNamedShare inserts a share so an encryption row names something real.
// The package's own seedShare always uses one name, and these cases need
// several distinct rows.
func seedNamedShare(t *testing.T, d *state.DB, name string) int64 {
	t.Helper()
	id, err := d.InsertShare(context.Background(), state.ShareRow{
		Name:          name,
		Host:          "/srv/" + name,
		SymlinkPolicy: "deny",
		Backend:       "local",
	}, 1)
	if err != nil {
		t.Fatalf("seeding share %q: %v", name, err)
	}
	return id
}

func TestListingAnswersEveryEncryptedShareOrdered(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	d, _ := open(t)
	a := seedNamedShare(t, d, "a")
	b := seedNamedShare(t, d, "b")
	seedNamedShare(t, d, "c")

	for _, id := range []int64{b, a} {
		if err := d.WriteShareEncryption(ctx, state.ShareEncryptionRow{
			Share: id, Scheme: "rclone-crypt-v1", Salt: "s", Verifier: []byte("k"), Created: 1,
		}); err != nil {
			t.Fatalf("WriteShareEncryption(%d): %v", id, err)
		}
	}
	rows, err := d.ListShareEncryption(ctx)
	if err != nil {
		t.Fatalf("ListShareEncryption: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("listed %d rows, want the two that were written", len(rows))
	}
	if rows[0].Share != a || rows[1].Share != b {
		t.Errorf("listed shares %d,%d, want %d,%d ascending", rows[0].Share, rows[1].Share, a, b)
	}
}

func TestDeletingSettingsLeavesTheShareItself(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	d, _ := open(t)
	share := seedNamedShare(t, d, "doomed")
	if err := d.WriteShareEncryption(ctx, state.ShareEncryptionRow{
		Share: share, Scheme: "rclone-crypt-v1", Salt: "s", Verifier: []byte("k"), Created: 1,
	}); err != nil {
		t.Fatalf("WriteShareEncryption: %v", err)
	}
	if err := d.DeleteShareEncryption(ctx, share); err != nil {
		t.Fatalf("DeleteShareEncryption: %v", err)
	}
	rows, err := d.ListShareEncryption(ctx)
	if err != nil {
		t.Fatalf("ListShareEncryption: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("the settings survived their removal: %+v", rows)
	}
	// The share is untouched: turning encryption off is not deleting the
	// folder.
	shares, err := d.ListShares(ctx)
	if err != nil {
		t.Fatalf("ListShares: %v", err)
	}
	if len(shares) != 1 {
		t.Errorf("the share rows are %+v, want the one share still there", shares)
	}
}
