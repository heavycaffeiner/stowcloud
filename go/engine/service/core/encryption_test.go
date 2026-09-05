//go:build linux

package core

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A salt of the right shape: 22 characters of base64url, which is 128 bits
// unpadded. The value itself is irrelevant to the server, which never
// derives anything; only the shape is checkable here.
const testSalt = "Zm9vYmFyYmF6cXV1eDEyMw"

// testVerifier is a verifier of the exact size and magic the server checks:
// rclone's 32-byte header, one 16-byte tag and the 19-byte known plaintext.
// Its contents are never decrypted here, because the passphrase that would
// open it never reaches this server.
func testVerifier() []byte {
	v := make([]byte, 32+16+19)
	copy(v, "RCLONE\x00\x00")
	return v
}

func testEncryption() Encryption {
	return Encryption{Scheme: SchemeRcloneCrypt, Salt: testSalt, Verifier: testVerifier()}
}

func encryptedShare(t *testing.T) (*Core, ShareID, string) {
	t.Helper()
	c, _ := newCore(t)
	host := t.TempDir()
	sh, err := c.CreateShare(context.Background(), ShareSpec{Name: "vault", Host: host})
	if err != nil {
		t.Fatalf("CreateShare: %v", err)
	}
	return c, sh.ID, host
}

func TestEnablingEncryptionStoresWhatTheClientSent(t *testing.T) {
	ctx := context.Background()
	c, id, _ := encryptedShare(t)

	want := testEncryption()
	if err := c.EnableEncryption(ctx, id, want); err != nil {
		t.Fatalf("EnableEncryption: %v", err)
	}

	got, ok, err := c.EncryptionOf(ctx, id)
	if err != nil || !ok {
		t.Fatalf("EncryptionOf: ok=%v err=%v", ok, err)
	}
	if got.Scheme != want.Scheme || got.Salt != want.Salt {
		t.Errorf("read back scheme %q salt %q, want %q and %q",
			got.Scheme, got.Salt, want.Scheme, want.Salt)
	}
	if !bytes.Equal(got.Verifier, want.Verifier) {
		t.Errorf("the verifier came back as %x, want %x", got.Verifier, want.Verifier)
	}
	if got.Created == 0 {
		t.Error("the row carries no creation time")
	}
}

// The whole of the boundary validation, since nothing stored here is
// decryptable by this server: a wrong format name, a salt that is not 22
// base64url characters, and a verifier that is not exactly one rclone crypt
// file of the agreed size.
func TestSettingsThatCouldNotHaveComeFromTheProcedureAreRefused(t *testing.T) {
	ctx := context.Background()
	c, id, _ := encryptedShare(t)

	cases := map[string]Encryption{
		"no scheme":    {Scheme: "", Salt: testSalt, Verifier: testVerifier()},
		"other scheme": {Scheme: "age-v1", Salt: testSalt, Verifier: testVerifier()},
		"short salt":   {Scheme: SchemeRcloneCrypt, Salt: "tooshort", Verifier: testVerifier()},
		"long salt":    {Scheme: SchemeRcloneCrypt, Salt: testSalt + "x", Verifier: testVerifier()},
		"salt not b64url": {
			Scheme: SchemeRcloneCrypt, Salt: "Zm9vYmFyYmF6cXV1eDEy+w", Verifier: testVerifier(),
		},
		"no verifier":    {Scheme: SchemeRcloneCrypt, Salt: testSalt, Verifier: nil},
		"short verifier": {Scheme: SchemeRcloneCrypt, Salt: testSalt, Verifier: testVerifier()[:66]},
		"long verifier": {
			Scheme: SchemeRcloneCrypt, Salt: testSalt, Verifier: append(testVerifier(), 0),
		},
		"wrong magic": {
			Scheme: SchemeRcloneCrypt, Salt: testSalt,
			Verifier: append([]byte("PK\x03\x04\x00\x00\x00\x00"), testVerifier()[8:]...),
		},
	}
	for name, bad := range cases {
		if err := c.EnableEncryption(ctx, id, bad); !errors.Is(err, ErrUnprocessable) {
			t.Errorf("%s returned %v, want ErrUnprocessable", name, err)
		}
	}
	if stored(t, c, id) {
		t.Error("a refused enable still wrote settings")
	}
}

// stored reads back whether a share has settings, failing the test on a read
// error rather than folding it into "not stored": a query that broke and a
// share that is plain are different facts, and treating them alike is how an
// assertion passes for the wrong reason.
func stored(t *testing.T, c *Core, id ShareID) bool {
	t.Helper()
	_, ok, err := c.EncryptionOf(context.Background(), id)
	if err != nil {
		t.Fatalf("EncryptionOf: %v", err)
	}
	return ok
}

// The salt's length is what makes its entropy claim enforceable, so the
// refusal has to name it rather than fail generically: a client that
// truncated the salt would otherwise look like a client that sent nothing.
func TestTheSaltRefusalNamesTheLength(t *testing.T) {
	c, id, _ := encryptedShare(t)
	err := c.EnableEncryption(context.Background(), id, Encryption{
		Scheme: SchemeRcloneCrypt, Salt: "short", Verifier: testVerifier(),
	})
	if err == nil || !strings.Contains(err.Error(), "22") {
		t.Errorf("the refusal is %v, want it to name the expected length", err)
	}
}

func TestEncryptionCannotBeTurnedOnOverExistingFiles(t *testing.T) {
	ctx := context.Background()
	c, id, host := encryptedShare(t)

	if err := os.WriteFile(filepath.Join(host, "already-here.txt"), []byte("plaintext"), 0o600); err != nil {
		t.Fatalf("seeding a file: %v", err)
	}

	err := c.EnableEncryption(ctx, id, testEncryption())
	if !errors.Is(err, ErrUnprocessable) {
		t.Fatalf("EnableEncryption over a populated share returned %v, want ErrUnprocessable", err)
	}
	if stored(t, c, id) {
		t.Error("the refused enable still wrote settings")
	}
}

func TestEncryptionCannotBeTurnedOffOverExistingFiles(t *testing.T) {
	ctx := context.Background()
	c, id, host := encryptedShare(t)

	if err := c.EnableEncryption(ctx, id, testEncryption()); err != nil {
		t.Fatalf("EnableEncryption: %v", err)
	}
	// Whatever a client uploaded here is ciphertext this server cannot read,
	// so dropping the salt it derives with would destroy the only thing that
	// could.
	if err := os.WriteFile(filepath.Join(host, "sealed.bin"), []byte("ciphertext"), 0o600); err != nil {
		t.Fatalf("seeding a file: %v", err)
	}

	if err := c.DisableEncryption(ctx, id); !errors.Is(err, ErrUnprocessable) {
		t.Fatalf("DisableEncryption over a populated share returned %v, want ErrUnprocessable", err)
	}
	if !stored(t, c, id) {
		t.Error("the refused disable dropped the settings anyway")
	}
}

// A share whose visible tree is empty but whose trash still holds a file
// must be refused by both toggles: disabling would strand the ciphertext in
// the trash unreadable, and enabling would leave the plaintext there for a
// later restore to drop into an encrypted share.
func TestATrashedFileBlocksBothEncryptionToggles(t *testing.T) {
	ctx := context.Background()

	c, id, host := encryptedShare(t)
	seedTrash(t, host)
	err := c.EnableEncryption(ctx, id, testEncryption())
	if !errors.Is(err, ErrUnprocessable) || !strings.Contains(err.Error(), "trash") {
		t.Errorf("EnableEncryption over a share with a trashed file returned %v, want ErrUnprocessable naming the trash", err)
	}
	if stored(t, c, id) {
		t.Error("the refused enable still wrote settings")
	}

	c2, id2, host2 := encryptedShare(t)
	if err = c2.EnableEncryption(ctx, id2, testEncryption()); err != nil {
		t.Fatalf("EnableEncryption on an empty share: %v", err)
	}
	seedTrash(t, host2)
	err = c2.DisableEncryption(ctx, id2)
	if !errors.Is(err, ErrUnprocessable) || !strings.Contains(err.Error(), "trash") {
		t.Errorf("DisableEncryption over a share with a trashed file returned %v, want ErrUnprocessable naming the trash", err)
	}
	if !stored(t, c2, id2) {
		t.Error("the refused disable dropped the settings anyway")
	}
}

// seedTrash writes one file directly under a share's trash directory,
// bypassing the delete path since this only needs the trash occupied, not a
// real deletion history.
func seedTrash(t *testing.T, host string) {
	t.Helper()
	trash := filepath.Join(host, ".sctrash")
	if err := os.MkdirAll(trash, 0o700); err != nil {
		t.Fatalf("seeding the trash directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(trash, "deadbeef-cGxhaW50ZXh0"), []byte("ciphertext"), 0o600); err != nil {
		t.Fatalf("seeding a trashed file: %v", err)
	}
}

func TestDisablingIsIdempotentAndDisablingAnUnencryptedShareSucceeds(t *testing.T) {
	ctx := context.Background()
	c, id, _ := encryptedShare(t)

	if err := c.DisableEncryption(ctx, id); err != nil {
		t.Fatalf("disabling a share that was never encrypted: %v", err)
	}
	if err := c.EnableEncryption(ctx, id, testEncryption()); err != nil {
		t.Fatalf("EnableEncryption: %v", err)
	}
	for i := range 2 {
		if err := c.DisableEncryption(ctx, id); err != nil {
			t.Fatalf("DisableEncryption call %d: %v", i+1, err)
		}
	}
	if ok, err := c.ShareEncrypted(ctx, id); err != nil || ok {
		t.Errorf("ShareEncrypted after disable is %v (err %v), want false", ok, err)
	}
}

func TestAPartFileDoesNotCountAsContent(t *testing.T) {
	ctx := context.Background()
	c, id, host := encryptedShare(t)

	// This server's own control names are bookkeeping, not content whose
	// encryption state would become ambiguous, so they must not block the
	// toggle: an abandoned upload would otherwise make a share permanently
	// unencryptable.
	if err := os.WriteFile(filepath.Join(host, ".scpart-abandoned"), []byte("x"), 0o600); err != nil {
		t.Fatalf("seeding a part file: %v", err)
	}
	if err := c.EnableEncryption(ctx, id, testEncryption()); err != nil {
		t.Fatalf("EnableEncryption with only a part file present: %v", err)
	}
}

func TestTheEncryptedSetNamesOnlyTheEncryptedShares(t *testing.T) {
	ctx := context.Background()
	c, _ := newCore(t)

	var ids []ShareID
	for _, name := range []string{"one", "two", "three"} {
		sh, err := c.CreateShare(ctx, ShareSpec{Name: name, Host: t.TempDir()})
		if err != nil {
			t.Fatalf("CreateShare(%q): %v", name, err)
		}
		ids = append(ids, sh.ID)
	}
	if err := c.EnableEncryption(ctx, ids[1], testEncryption()); err != nil {
		t.Fatalf("EnableEncryption: %v", err)
	}

	set, err := c.EncryptedShares(ctx)
	if err != nil {
		t.Fatalf("EncryptedShares: %v", err)
	}
	if len(set) != 1 || set[0] != ids[1] {
		t.Errorf("the encrypted set is %v, want exactly [%d]", set, ids[1])
	}
}

func TestAnUnknownShareIsNotFound(t *testing.T) {
	c, _ := newCore(t)
	err := c.EnableEncryption(context.Background(), ShareID(999999), testEncryption())
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("EnableEncryption on an unknown share returned %v, want ErrNotFound", err)
	}
}
