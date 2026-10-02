package e2e_test

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/heavycaffeiner/stowcloud/backend/internal/auth"
	"github.com/heavycaffeiner/stowcloud/backend/internal/db/dbfile"
	"github.com/heavycaffeiner/stowcloud/backend/internal/db/state"
	"github.com/heavycaffeiner/stowcloud/backend/internal/platform/security/secret"
)

func TestReleaseV0191StateAndKeyRingUpgrade(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbData, err := os.ReadFile(filepath.Join("..", "..", "internal", "db", "state", "testdata", "release-v0.19.1", "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	dbCopy, err := os.CreateTemp(dir, "state-*.db")
	if err != nil {
		t.Fatal(err)
	}
	if n, writeErr := dbCopy.Write(dbData); writeErr != nil || n != len(dbData) {
		t.Fatalf("copying the release database: %d bytes, %v", n, writeErr)
	}
	if closeErr := dbCopy.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	keyData, err := os.ReadFile(filepath.Join("..", "..", "internal", "db", "state", "testdata", "release-v0.19.1", "master.key"))
	if err != nil {
		t.Fatal(err)
	}
	keyCopy, err := os.CreateTemp(dir, "master-*.key")
	if err != nil {
		t.Fatal(err)
	}
	if n, writeErr := keyCopy.Write(keyData); writeErr != nil || n != len(keyData) {
		t.Fatalf("copying the release key ring: %d bytes, %v", n, writeErr)
	}
	if closeErr := keyCopy.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	t.Setenv("SC_MASTER_KEY_FILE", keyCopy.Name())

	file, err := dbfile.Open(ctx, state.Spec(dbCopy.Name()))
	if err != nil {
		t.Fatalf("opening the release database: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := file.Close(); closeErr != nil {
			t.Errorf("closing the release database: %v", closeErr)
		}
	})
	version, err := file.Version(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if want := len(state.Spec("unused").Migrations); version != want || version <= 19 {
		t.Fatalf("migrated version = %d, want current version %d above release version 19", version, want)
	}

	store := state.New(file)
	authSvc := auth.New(auth.Config{Store: store, StoreDir: dir, Logger: slog.New(slog.DiscardHandler)})
	if _, keyErr := authSvc.OpenMasterKey(ctx); keyErr != nil {
		t.Fatalf("opening the release key ring: %v", keyErr)
	}
	admin, err := store.AccountByName(ctx, "fixtureadmin")
	if err != nil || !admin.IsAdmin() {
		t.Fatalf("reading the release administrator: %+v, %v", admin, err)
	}
	ok, err := authSvc.VerifyAccountPassword(ctx, admin.ID, secret.New([]byte("fixture password only")))
	if err != nil || !ok {
		t.Fatalf("verifying the release password: %t, %v", ok, err)
	}
	if _, totpErr := store.TOTPSecretOf(ctx, 2); totpErr != nil {
		t.Fatalf("reading the release second factor: %v", totpErr)
	}
	sealed, found, err := store.ReadConfigSecret(ctx, "fixture")
	if err != nil || !found {
		t.Fatalf("reading the release configuration secret: found=%t, err=%v", found, err)
	}
	plain, err := authSvc.OpenConfigSecret("fixture", sealed.Value, sealed.KeyVer)
	if err != nil || !bytes.Equal(plain, []byte("synthetic configuration value")) {
		t.Fatalf("opening the release configuration secret: %v", err)
	}

	id := []byte("fixture-upload-01")
	upload, err := store.ReadUploadSession(ctx, id)
	if err != nil {
		t.Fatalf("reading the release upload session: %v", err)
	}
	if upload.User != admin.ID || upload.Dest != "fixture.bin" || upload.TotalLen == nil || *upload.TotalLen != 12 || upload.ChunkSize != 4 {
		t.Fatalf("release upload session changed: %+v", upload)
	}
	runs, err := store.ReadUploadIntervals(ctx, id)
	if err != nil || len(runs) != 1 || runs[0] != [2]uint64{0, 4} {
		t.Fatalf("release upload intervals changed: %v, %v", runs, err)
	}
}
