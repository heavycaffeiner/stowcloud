//go:build linux

package lifecycle_test

import (
	"context"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/secret"
	"github.com/heavycaffeiner/stowcloud/go/engine/lifecycle"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
)

// testEncryption is settings EnableEncryption accepts: the right scheme, a
// salt of the right length and alphabet, and a verifier of the right size
// carrying rclone's file magic.
//
// Nothing here has to decrypt. EnableEncryption checks shape only, because
// the passphrase that would open the verifier never reaches this server, and
// every guard this file exercises refuses before any content is read.
func testEncryption() core.Encryption {
	verifier := make([]byte, 32+16+19)
	copy(verifier, "RCLONE\x00\x00")
	return core.Encryption{
		Scheme:   core.SchemeRcloneCrypt,
		Salt:     "AAAAAAAAAAAAAAAAAAAAAA",
		Verifier: verifier,
	}
}

// encryptedShare serves an engine holding one share with encryption turned
// on and nothing in it yet, since turning encryption on requires an empty
// share.
//
// The caller uploads whatever content the test needs afterward, through the
// ordinary write route: content arriving for an encrypted share is written
// exactly as sent, which is the point of the feature and lets a test use
// plain, decodable bytes to tell "refused" apart from "attempted and
// failed".
//
// previewWorker wires the jailed decoder when a test needs one; an empty
// string leaves thumbnails off, which is fine for every guard but the
// thumbnail one.
func encryptedShare(t *testing.T, perms acl.Perms, previewWorker string) (
	base string, sess session, share string, e *lifecycle.Engine, owner int64,
) {
	t.Helper()
	ctx := context.Background()

	e, err := lifecycle.Open(ctx, lifecycle.Options{DataDir: t.TempDir(), PreviewWorker: previewWorker})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	t.Cleanup(func() {
		if cerr := e.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	})
	if previewWorker != "" && e.Preview == nil {
		t.Skip("this host builds no decoder pool, so there is nothing to drive")
	}

	owner, err = e.Auth.CreateUser(ctx, "alice", "Alice", secret.New([]byte("a-long-enough-password")))
	if err != nil {
		t.Fatal(err)
	}

	host := t.TempDir()
	sh, err := e.Core.CreateShare(ctx, core.ShareSpec{Name: "vault", Host: host})
	if err != nil {
		t.Fatal(err)
	}

	if eerr := e.Core.EnableEncryption(ctx, sh.ID, testEncryption()); eerr != nil {
		t.Fatalf("enabling encryption: %v", eerr)
	}

	if _, gerr := e.Core.CreateGrant(ctx, core.GrantSpec{
		User: &owner, Share: sh.ID, Allow: perms, Inherit: true, Label: sh.Name,
	}); gerr != nil {
		t.Fatal(gerr)
	}

	base = serve(t, e)
	sess = signIn(t, base, "alice", "a-long-enough-password")
	return base, sess, sh.Name, e, owner
}
