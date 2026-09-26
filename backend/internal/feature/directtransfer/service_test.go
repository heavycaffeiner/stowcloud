//go:build linux

package directtransfer

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"path/filepath"
	"testing"
	"time"

	core "github.com/heavycaffeiner/stowcloud/backend/internal/feature/files"
	"github.com/heavycaffeiner/stowcloud/backend/internal/storage/objstore"
	"github.com/heavycaffeiner/stowcloud/backend/internal/storage/vfs"
	"github.com/heavycaffeiner/stowcloud/backend/internal/store/dbfile"
	"github.com/heavycaffeiner/stowcloud/backend/internal/store/state"
)

// directTransferProviderFake embeds the interface so the presign method, which
// these tests never reach, needs no stub.
type directTransferProviderFake struct {
	objstore.DirectTransferProvider
	listed       []objstore.MultipartPart
	metadataSize uint64
	metadataETag string
	metadataSum  string
	metadataOK   bool
	complete     []objstore.MultipartPart
	aborts       int
}

func (p *directTransferProviderFake) DirectTransfer() bool          { return true }
func (p *directTransferProviderFake) ObjectKey(vfs.SafePath) string { return "objects/test" }
func (p *directTransferProviderFake) BeginMultipart(context.Context, string, int64, string) (string, error) {
	return "upload", nil
}
func (p *directTransferProviderFake) AbortMultipart(context.Context, string, string) error {
	p.aborts++
	return nil
}
func (p *directTransferProviderFake) ListParts(context.Context, string, string) ([]objstore.MultipartPart, error) {
	return append([]objstore.MultipartPart(nil), p.listed...), nil
}
func (p *directTransferProviderFake) CompleteMultipart(_ context.Context, _, _ string, parts []objstore.MultipartPart) (objstore.TransferReceipt, error) {
	p.complete = append([]objstore.MultipartPart(nil), parts...)
	return objstore.TransferReceipt{Size: p.metadataSize, ETag: p.metadataETag, Checksum: p.metadataSum}, nil
}
func (p *directTransferProviderFake) PresignGet(context.Context, string, time.Duration) (string, error) {
	return "https://download.invalid", nil
}
func (p *directTransferProviderFake) ObjectMetadata(context.Context, string) (uint64, string, string, bool, error) {
	return p.metadataSize, p.metadataETag, p.metadataSum, p.metadataOK, nil
}

func openDirectTransferDB(t *testing.T) *state.DB {
	t.Helper()
	f, err := dbfile.Open(context.Background(), state.Spec(filepath.Join(t.TempDir(), "state.db")))
	if err != nil {
		t.Fatalf("opening state: %v", err)
	}
	t.Cleanup(func() {
		if cerr := f.Close(); cerr != nil {
			t.Errorf("closing state: %v", cerr)
		}
	})
	return state.New(f)
}

func seedDirectTransferUser(t *testing.T, d *state.DB, id int64, usage uint64) {
	t.Helper()
	if err := d.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(), `INSERT INTO user(id, name, pw_hash, usage_bytes, created_ns) VALUES (?, ?, '', ?, 0)`, id, "direct-transfer-user", usage)
		return err
	}); err != nil {
		t.Fatalf("seeding user: %v", err)
	}
}

func newDirectTransferRow(id string, expected, prior, reservation uint64) state.DirectTransferReservation {
	return state.DirectTransferReservation{
		ID: id, Owner: 1, Share: 1, Path: "files/object.bin", ObjectKey: "objects/test", UploadID: "upload",
		ExpectedSize: expected, PriorSize: prior, QuotaReservation: reservation,
		CreatedNs: 1, UpdatedNs: 1, ExpiresNs: 1 << 60, State: state.DirectTransferPending,
	}
}

func addDirectTransferRow(t *testing.T, d *state.DB, row state.DirectTransferReservation) {
	t.Helper()
	if err := d.CreateDirectTransfer(context.Background(), row); err != nil {
		t.Fatalf("creating transfer row: %v", err)
	}
}

func serviceForProvider(d *state.DB, p objstore.DirectTransferProvider) *Service {
	return NewService(Dependencies{
		State: d,
		Now:   func() int64 { return 2 },
		ProviderForRow: func(context.Context, state.DirectTransferReservation) (objstore.DirectTransferProvider, bool, error) {
			return p, true, nil
		},
		RevalidateDestination: func(context.Context, state.DirectTransferReservation) error { return nil },
	})
}

func TestCompleteAcceptsHexClientChecksumAndBase64ProviderChecksum(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	d := openDirectTransferDB(t)
	seedDirectTransferUser(t, d, 1, 0)

	digest := make([]byte, 32)
	for i := range digest {
		digest[i] = byte(i + 1)
	}
	hexSum := hex.EncodeToString(digest)
	base64Sum := base64.StdEncoding.EncodeToString(digest)
	size := uint64(32)
	provider := &directTransferProviderFake{
		listed:       []objstore.MultipartPart{{PartNumber: 1, ETag: "etag-1", Size: int64(size), Checksum: base64Sum}},
		metadataSize: size, metadataETag: "object-etag", metadataSum: base64Sum, metadataOK: true,
	}
	row := newDirectTransferRow("checksum", size, 0, size)
	row.ExpectedChecksum = hexSum
	addDirectTransferRow(t, d, row)

	got, _, err := serviceForProvider(d, provider).Complete(ctx, 1, row.ID, func() ([]CompletePart, error) {
		return []CompletePart{{Number: 1, ETag: "etag-1", Checksum: hexSum, Size: size}}, nil
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if got.State != state.DirectTransferComplete {
		t.Fatalf("state = %v, want complete", got.State)
	}
	if len(provider.complete) != 1 || provider.complete[0].Checksum != hexSum {
		t.Fatalf("provider received parts = %+v, want client checksum preserved", provider.complete)
	}
}

func TestCompleteSortsDescendingPartsBeforeProviderCall(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	d := openDirectTransferDB(t)
	seedDirectTransferUser(t, d, 1, 0)
	size := PartSize + 1
	provider := &directTransferProviderFake{
		listed: []objstore.MultipartPart{
			{PartNumber: 1, ETag: "etag-1", Size: int64(PartSize)},
			{PartNumber: 2, ETag: "etag-2", Size: 1},
		},
		metadataSize: size, metadataETag: "object-etag", metadataOK: true,
	}
	row := newDirectTransferRow("order", size, 0, 0)
	addDirectTransferRow(t, d, row)

	if _, _, err := serviceForProvider(d, provider).Complete(ctx, 1, row.ID, func() ([]CompletePart, error) {
		return []CompletePart{
			{Number: 2, ETag: "etag-2", Size: 1},
			{Number: 1, ETag: "etag-1", Size: PartSize},
		}, nil
	}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if len(provider.complete) != 2 || provider.complete[0].PartNumber != 1 || provider.complete[1].PartNumber != 2 {
		t.Fatalf("provider received part order = %+v, want 1,2", provider.complete)
	}
}

func TestCompleteRejectsDuplicatePartBeforeLeavingPending(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	d := openDirectTransferDB(t)
	seedDirectTransferUser(t, d, 1, 0)
	provider := &directTransferProviderFake{}
	row := newDirectTransferRow("duplicate", 32, 0, 0)
	addDirectTransferRow(t, d, row)

	if _, _, err := serviceForProvider(d, provider).Complete(ctx, 1, row.ID, func() ([]CompletePart, error) {
		return []CompletePart{
			{Number: 1, ETag: "etag-1", Size: 32},
			{Number: 1, ETag: "etag-1", Size: 32},
		}, nil
	}); !errors.Is(err, core.ErrUnprocessable) {
		t.Fatalf("duplicate Complete error = %v, want ErrUnprocessable", err)
	}
	got, err := d.GetDirectTransfer(ctx, row.ID)
	if err != nil {
		t.Fatalf("reading row: %v", err)
	}
	if got.State != state.DirectTransferPending {
		t.Fatalf("duplicate changed state to %v, want pending", got.State)
	}
	if len(provider.complete) != 0 {
		t.Fatal("duplicate reached provider completion")
	}
}

func TestCancelDoesNotAbortACompletingTransfer(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	d := openDirectTransferDB(t)
	seedDirectTransferUser(t, d, 1, 0)
	provider := &directTransferProviderFake{}
	row := newDirectTransferRow("race", 32, 0, 0)
	addDirectTransferRow(t, d, row)
	if err := d.BeginDirectTransferCompletion(ctx, row.ID, row.Owner, 2); err != nil {
		t.Fatalf("begin completion: %v", err)
	}

	if err := serviceForProvider(d, provider).Cancel(ctx, 1, row.ID); !errors.Is(err, ErrExpired) {
		t.Fatalf("Cancel error = %v, want ErrExpired", err)
	}
	if provider.aborts != 0 {
		t.Fatalf("Cancel called provider abort %d times", provider.aborts)
	}
	got, err := d.GetDirectTransfer(ctx, row.ID)
	if err != nil {
		t.Fatalf("reading row: %v", err)
	}
	if got.State != state.DirectTransferCompleting {
		t.Fatalf("Cancel changed state to %v, want completing", got.State)
	}
}

func TestCreateRejectsUnsupportedSizes(t *testing.T) {
	t.Parallel()
	svc := NewService(Dependencies{})
	for _, size := range []uint64{0, PartSize*MaxParts + 1} {
		_, err := svc.Create(context.Background(), 1, CreateRequest{Path: "files/object.bin", Size: size})
		if !errors.Is(err, ErrUnsupportedSize) {
			t.Errorf("Create size %d error = %v, want ErrUnsupportedSize", size, err)
		}
	}
}

func TestPublishNewAndReplacementSetOwnerUsageToPublishedSize(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, tc := range []struct {
		name, id           string
		before, prior, new uint64
		partSize           int64
	}{
		{name: "new", id: "quota-new", before: 0, prior: 0, new: 17, partSize: 17},
		{name: "replacement", id: "quota-replace", before: 23, prior: 23, new: 41, partSize: 41},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := openDirectTransferDB(t)
			seedDirectTransferUser(t, d, 1, tc.before)
			if ok, err := state.NewQuota(d).Reserve(ctx, 1, tc.new); err != nil || !ok {
				t.Fatalf("reserve new size: %v (ok %v)", err, ok)
			}
			provider := &directTransferProviderFake{
				listed:       []objstore.MultipartPart{{PartNumber: 1, ETag: "etag-1", Size: tc.partSize}},
				metadataSize: tc.new, metadataETag: "object-etag", metadataOK: true,
			}
			row := newDirectTransferRow(tc.id, tc.new, tc.prior, tc.new)
			addDirectTransferRow(t, d, row)
			if _, _, err := serviceForProvider(d, provider).Complete(ctx, 1, row.ID, func() ([]CompletePart, error) {
				return []CompletePart{{Number: 1, ETag: "etag-1", Size: tc.new}}, nil
			}); err != nil {
				t.Fatalf("Complete: %v", err)
			}
			account, err := d.AccountByID(ctx, 1)
			if err != nil {
				t.Fatalf("reading account: %v", err)
			}
			want := tc.before - tc.prior + tc.new
			if account.UsageBytes != want {
				t.Fatalf("usage = %d, want %d", account.UsageBytes, want)
			}
		})
	}
}
