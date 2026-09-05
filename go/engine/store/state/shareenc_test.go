package state_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/store/state"
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

func TestShareEncryptionRoundTripsTheVerifierByteForByte(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	share := seedNamedShare(t, d, "vault")

	// Bytes with a NUL and a high byte in them: the column has to carry an
	// opaque blob, not a string the driver might truncate or re-encode.
	verifier := []byte("RCLONE\x00\x00\x00\xff\xfe ciphertext")
	want := state.ShareEncryptionRow{
		Share:    share,
		Scheme:   "rclone-crypt-v1",
		Salt:     "Zm9vYmFyYmF6cXV1eDEyMw",
		Verifier: verifier,
		Created:  42,
	}
	if err := d.WriteShareEncryption(ctx, want); err != nil {
		t.Fatalf("WriteShareEncryption: %v", err)
	}

	got, ok, err := d.ReadShareEncryption(ctx, share)
	if err != nil || !ok {
		t.Fatalf("ReadShareEncryption: ok=%v err=%v", ok, err)
	}
	if got.Scheme != want.Scheme || got.Salt != want.Salt || got.Created != want.Created {
		t.Errorf("read back %+v, want %+v", got, want)
	}
	if !bytes.Equal(got.Verifier, verifier) {
		t.Errorf("the verifier came back as %x, want %x", got.Verifier, verifier)
	}
}

func TestAnUnencryptedShareHasNoRowAndNoError(t *testing.T) {
	d, _ := open(t)
	share := seedNamedShare(t, d, "plain")
	_, ok, err := d.ReadShareEncryption(context.Background(), share)
	if err != nil {
		t.Fatalf("ReadShareEncryption: %v", err)
	}
	if ok {
		t.Error("a share that was never encrypted reports settings")
	}
}

func TestWritingTwiceReplacesRatherThanFailing(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	share := seedNamedShare(t, d, "rotated")

	first := state.ShareEncryptionRow{
		Share: share, Scheme: "rclone-crypt-v1", Salt: "first", Verifier: []byte("one"), Created: 1,
	}
	second := state.ShareEncryptionRow{
		Share: share, Scheme: "rclone-crypt-v1", Salt: "second", Verifier: []byte("two"), Created: 2,
	}
	for _, r := range []state.ShareEncryptionRow{first, second} {
		if err := d.WriteShareEncryption(ctx, r); err != nil {
			t.Fatalf("WriteShareEncryption(%q): %v", r.Salt, err)
		}
	}
	got, _, err := d.ReadShareEncryption(ctx, share)
	if err != nil {
		t.Fatalf("ReadShareEncryption: %v", err)
	}
	if got.Salt != second.Salt {
		t.Errorf("salt is %q, want the second write %q", got.Salt, second.Salt)
	}
}

func TestListingAnswersEveryEncryptedShareOrdered(t *testing.T) {
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

// Removing settings that are not there is not an error, so a delete path can
// drop them without first asking whether any exist.
func TestDeletingAbsentSettingsSucceeds(t *testing.T) {
	d, _ := open(t)
	if err := d.DeleteShareEncryption(context.Background(), 4242); err != nil {
		t.Errorf("DeleteShareEncryption on an unencrypted share: %v", err)
	}
}
