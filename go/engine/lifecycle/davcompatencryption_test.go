//go:build linux && compat_nc

package lifecycle_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/lifecycle"
)

// The virtual root is where a compatibility client discovers what to mount,
// so an encrypted share has to disappear from it: offering it invites a
// Nextcloud or ownCloud sync client to fill a local folder with ciphertext
// it can never decrypt.
func TestCompatRootHidesAnEncryptedShare(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	if err := f.core.EnableEncryption(context.Background(), testShareOther, encryptionSettingsForTest()); err != nil {
		t.Fatalf("enabling encryption: %v", err)
	}
	m := f.mounted(lifecycle.DavAlias{Prefix: "/remote.php/dav/files", DropSegments: 1})

	w := f.through(m, "PROPFIND", "/remote.php/dav/files/me/", allprop)

	if w.Code != http.StatusMultiStatus {
		t.Fatalf("answered %d, want 207: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "files") {
		t.Errorf("the plain share is missing: %s", body)
	}
	if strings.Contains(body, "safe") {
		t.Errorf("the encrypted share is listed: %s", body)
	}
}

// Hiding the share from a compatibility client must not hide it from this
// server's own mount: rclone reads an encrypted share through /dav directly,
// and that is deliberate. This is the test that stops someone from "fixing"
// the leak above by hiding the share everywhere and breaking rclone.
func TestNativeRootStillListsAnEncryptedShare(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	if err := f.core.EnableEncryption(context.Background(), testShareOther, encryptionSettingsForTest()); err != nil {
		t.Fatalf("enabling encryption: %v", err)
	}

	w := f.through(f.mounted(), "PROPFIND", "/dav/", allprop)

	if w.Code != http.StatusMultiStatus {
		t.Fatalf("answered %d, want 207: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "files") || !strings.Contains(body, "safe") {
		t.Errorf("the native root did not list both shares: %s", body)
	}
}
