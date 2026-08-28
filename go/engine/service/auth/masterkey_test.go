package auth_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/service/auth"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/state"
)

// A key in an environment variable is visible to a container inspection and
// to /proc, and somebody who set it believes it is being used.
func TestAKeyInTheEnvironmentIsAHardRefusal(t *testing.T) {
	for _, value := range []string{"", "anything at all"} {
		t.Setenv("SC_MASTER_KEY", value)
		if _, _, err := auth.ResolveKeyFile(t.TempDir()); !errors.Is(err, auth.ErrKeyEnvForbidden) {
			t.Fatalf("SC_MASTER_KEY=%q resolved with %v", value, err)
		}
	}
}

// The default location is inside the data directory, so refusing would leave
// the default deployment unable to start. The warning is what an operator
// acts on when setting up backups.
func TestTheDefaultKeyPathIsInsideTheDataDirectoryAndTheNamedOneNeedNotBe(t *testing.T) {
	dir := t.TempDir()

	t.Setenv("SC_MASTER_KEY_FILE", "")
	path, inside, err := auth.ResolveKeyFile(dir)
	if err != nil {
		t.Fatalf("ResolveKeyFile: %v", err)
	}
	if path != filepath.Join(dir, "master.key") || !inside {
		t.Fatalf("the default resolved to %q, inside %v", path, inside)
	}

	outer := filepath.Join(t.TempDir(), "elsewhere.key")
	t.Setenv("SC_MASTER_KEY_FILE", outer)
	if path, inside, err = auth.ResolveKeyFile(dir); err != nil {
		t.Fatalf("ResolveKeyFile: %v", err)
	}
	if path != outer || inside {
		t.Fatalf("a named path resolved to %q, inside %v", path, inside)
	}
}

// A file of exactly one key's worth of bytes is what a pre-ring deployment
// wrote; reading it as version 1 is what upgrades that deployment in place.
func TestALegacyRawKeyFileReadsAsVersionOne(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "master.key")
	raw := bytes.Repeat([]byte{7}, 32)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("writing the legacy key: %v", err)
	}

	ring, found, err := auth.LoadKeyRing(path)
	if err != nil || !found {
		t.Fatalf("LoadKeyRing returned found=%v, %v", found, err)
	}
	key, ver := ring.Active()
	if ver != 1 || !bytes.Equal(key[:], raw) {
		t.Fatalf("the legacy key read back at version %d", ver)
	}
}

func TestAMalformedKeyFileRefuses(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string][]byte{
		"neither a raw key nor a ring": []byte("hello"),
		"a ring header with no count":  []byte("SCMKEYRNG1\n"),
		"a truncated ring entry":       append([]byte("SCMKEYRNG1\n\x00\x01"), 1, 2, 3),
		"a ring with trailing bytes": append(
			append([]byte("SCMKEYRNG1\n\x00\x01\x00\x00\x00\x01"), bytes.Repeat([]byte{9}, 32)...),
			'x'),
	} {
		path := filepath.Join(dir, "k")
		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
		if _, _, err := auth.LoadKeyRing(path); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
}

// A missing file is not an error: the caller decides whether to generate one.
func TestAMissingKeyFileIsNotAnError(t *testing.T) {
	_, found, err := auth.LoadKeyRing(filepath.Join(t.TempDir(), "absent"))
	if err != nil || found {
		t.Fatalf("LoadKeyRing on a missing file returned found=%v, %v", found, err)
	}
}

// The key file is the operator's own; it must not become readable to a
// neighbour of the data directory.
func TestAGeneratedKeyFileIsPrivate(t *testing.T) {
	f := newFixture(t)
	info, err := os.Stat(filepath.Join(f.dir, "master.key"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("the key file has mode %o", mode)
	}
}

// Serving on a key that cannot open what is on disk would surface as failing
// logins with no common cause.
func TestStartupRefusesAKeyTheDatabaseDoesNotName(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)

	if err := f.store.SetKeyVersion(ctx, 9); err != nil {
		t.Fatalf("SetKeyVersion: %v", err)
	}
	if _, err := f.svc.OpenMasterKey(ctx); !errors.Is(err, auth.ErrKeyVersionMissing) {
		t.Fatalf("a database naming an absent version started with %v", err)
	}
}

func TestAFreshDeploymentEstablishesTheKeyVersion(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)

	ver, err := f.store.KeyVersionState(ctx)
	if err != nil {
		t.Fatalf("KeyVersionState: %v", err)
	}
	if ver != 1 {
		t.Fatalf("the established version is %d", ver)
	}
}

// A rotation re-seals every kind and compacts the ring, and the report says
// how many rows moved rather than only that it finished.
func TestRotationResealsEveryKindAndCompactsTheRing(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	id := f.account(t, "alice")

	secretB32, err := f.svc.GenerateTOTPSecret()
	if err != nil {
		t.Fatalf("GenerateTOTPSecret: %v", err)
	}
	if err = f.svc.EnrollTOTP(ctx, id, secretB32); err != nil {
		t.Fatalf("EnrollTOTP: %v", err)
	}
	if err = f.svc.SetSMBPassword(ctx, id, pw(testPassword)); err != nil {
		t.Fatalf("SetSMBPassword: %v", err)
	}
	blob, ver, err := f.svc.SealConfigSecret("oidc.client_secret", []byte("shhh"))
	if err != nil {
		t.Fatalf("SealConfigSecret: %v", err)
	}
	if err = f.store.WriteConfigSecret(ctx, "oidc.client_secret",
		state.ConfigSecret{Value: blob, KeyVer: ver}); err != nil {
		t.Fatalf("WriteConfigSecret: %v", err)
	}

	rep, err := f.svc.RotateMasterKey(ctx)
	if err != nil {
		t.Fatalf("RotateMasterKey: %v", err)
	}
	if rep.OldVersion != 1 || rep.NewVersion != 2 {
		t.Fatalf("the report names versions %d and %d", rep.OldVersion, rep.NewVersion)
	}
	if rep.SMBBrought != 1 || rep.TOTPBrought != 1 || rep.ConfigSecretsBrought != 1 {
		t.Fatalf("the report counts %+v", rep)
	}

	// Everything still opens, under the new key.
	stored, found, err := f.store.ReadConfigSecret(ctx, "oidc.client_secret")
	if err != nil || !found {
		t.Fatalf("ReadConfigSecret returned found=%v, %v", found, err)
	}
	plain, err := f.svc.OpenConfigSecret("oidc.client_secret", stored.Value, stored.KeyVer)
	if err != nil || string(plain) != "shhh" {
		t.Fatalf("the rotated secret opened as %q, %v", plain, err)
	}
	now := int64(1_700_000_000) * 1_000_000_000
	code := totpCode(t, secretB32, now/int64(30_000_000_000))
	if ok, verr := f.svc.VerifyTOTP(ctx, id, code, now); verr != nil || !ok {
		t.Fatalf("the rotated second factor returned %v, %v", ok, verr)
	}

	// The ring holds only the new key afterwards.
	ring, _, err := auth.LoadKeyRing(filepath.Join(f.dir, "master.key"))
	if err != nil {
		t.Fatalf("LoadKeyRing: %v", err)
	}
	if vs := ring.Versions(); len(vs) != 1 || vs[0] != 2 {
		t.Fatalf("the compacted ring holds %v", vs)
	}
}

// A ciphertext cannot be moved between records or replayed across a
// rotation, because both are bound as additional authenticated data.
func TestASealedValueIsBoundToItsRecordAndVersion(t *testing.T) {
	f := newFixture(t)

	blob, ver, err := f.svc.SealConfigSecret("oidc.client_secret", []byte("shhh"))
	if err != nil {
		t.Fatalf("SealConfigSecret: %v", err)
	}
	if _, err = f.svc.OpenConfigSecret("smtp.password", blob, ver); err == nil {
		t.Fatal("a secret opened under another name")
	}
	if _, err = f.svc.OpenConfigSecret("oidc.client_secret", blob, ver+1); err == nil {
		t.Fatal("a secret opened under another key version")
	}
	plain, err := f.svc.OpenConfigSecret("oidc.client_secret", blob, ver)
	if err != nil || string(plain) != "shhh" {
		t.Fatalf("the secret opened as %q, %v", plain, err)
	}
}

// A blob shorter than a nonce is corruption rather than an authentication
// failure, and the two must stay distinguishable in a log.
func TestATruncatedCiphertextIsNamedAsCorruption(t *testing.T) {
	f := newFixture(t)
	blob, ver, err := f.svc.SealConfigSecret("name", []byte("value"))
	if err != nil {
		t.Fatalf("SealConfigSecret: %v", err)
	}
	if _, err := f.svc.OpenConfigSecret("name", blob[:8], ver); !errors.Is(err, auth.ErrCiphertextTooShort) {
		t.Fatalf("a truncated blob returned %v", err)
	}
}

// Sessions are durable, so a process-random key would strand every live
// session's token on each restart, and a restart is not a security event.
func TestTheCSRFKeyIsStableAcrossRestartsAndDiffersBetweenDeployments(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)

	first, err := f.svc.CSRFKey(ctx)
	if err != nil {
		t.Fatalf("CSRFKey: %v", err)
	}
	if len(first) != 32 {
		t.Fatalf("the key is %d bytes", len(first))
	}
	// Re-opening the same key file is what a restart does.
	if _, err = f.svc.OpenMasterKey(ctx); err != nil {
		t.Fatalf("OpenMasterKey: %v", err)
	}
	second, err := f.svc.CSRFKey(ctx)
	if err != nil {
		t.Fatalf("CSRFKey: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("the key changed across a restart")
	}

	other := newFixture(t)
	otherKey, err := other.svc.CSRFKey(ctx)
	if err != nil {
		t.Fatalf("CSRFKey: %v", err)
	}
	if bytes.Equal(first, otherKey) {
		t.Fatal("two deployments derived the same key")
	}
}

// A capability minted for one purpose must not be presentable as another.
func TestASealedPresentationValueIsPurposeAndVersionBound(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)

	sealed, ver, err := f.svc.SealPresentation(ctx, "content-claim", []byte("claim"))
	if err != nil {
		t.Fatalf("SealPresentation: %v", err)
	}
	if _, err = f.svc.OpenPresentation(ctx, "login-flow-delivery", sealed, ver); err == nil {
		t.Fatal("a content claim opened as a login-flow delivery")
	}
	plain, err := f.svc.OpenPresentation(ctx, "content-claim", sealed, ver)
	if err != nil || string(plain) != "claim" {
		t.Fatalf("the claim opened as %q, %v", plain, err)
	}
	if _, _, err := f.svc.SealPresentation(ctx, "", []byte("x")); err == nil {
		t.Fatal("a value was sealed with no purpose")
	}
}

// A rotation compacts the ring, so a capability sealed under the previous key
// stops opening. That bound is what keeps a leaked one from outliving the
// rotation that was meant to end it.
func TestASealedPresentationValueDoesNotSurviveACompactedRotation(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)

	sealed, ver, err := f.svc.SealPresentation(ctx, "content-claim", []byte("claim"))
	if err != nil {
		t.Fatalf("SealPresentation: %v", err)
	}
	if _, err := f.svc.RotateMasterKey(ctx); err != nil {
		t.Fatalf("RotateMasterKey: %v", err)
	}
	if _, err := f.svc.OpenPresentation(ctx, "content-claim", sealed, ver); !errors.Is(err, auth.ErrKeyVersionMissing) {
		t.Fatalf("a claim from before the rotation returned %v", err)
	}
}
