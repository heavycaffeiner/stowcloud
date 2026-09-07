//go:build linux && compat_nc

package lifecycle_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/lifecycle"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
)

// The Nextcloud app has only the OCS shares listing to reach an overview of
// published links: there is no separate admin screen it knows how to call.
// An administrator asking with all_links=true must see every account's
// links, an ordinary account asking with the same flag must see only its
// own, and a link into an encrypted share must stay hidden from both, the
// same as the ordinary listing already guarantees.
func TestCompatListSharesAllLinksIsAdminOnly(t *testing.T) {
	t.Parallel()
	e, openErr := lifecycle.Open(context.Background(), lifecycle.Options{DataDir: t.TempDir()})
	if openErr != nil {
		t.Fatalf("opening engine: %v", openErr)
	}
	t.Cleanup(func() {
		if cerr := e.Close(); cerr != nil {
			t.Errorf("closing engine: %v", cerr)
		}
	})
	ctx := context.Background()

	plainDir := t.TempDir()
	if rerr := e.Core.RegisterShare(ctx, core.ShareDef{
		ID: 1, Name: "openshare", Host: plainDir, Policy: vfs.DefaultSharePolicy(),
	}); rerr != nil {
		t.Fatalf("registering the plain share: %v", rerr)
	}
	cryptDir := t.TempDir()
	if rerr := e.Core.RegisterShare(ctx, core.ShareDef{
		ID: 2, Name: "cryptshare", Host: cryptDir, Policy: vfs.DefaultSharePolicy(),
	}); rerr != nil {
		t.Fatalf("registering the encrypted share: %v", rerr)
	}
	adminDir := t.TempDir()
	if rerr := e.Core.RegisterShare(ctx, core.ShareDef{
		ID: 3, Name: "adminshare", Host: adminDir, Policy: vfs.DefaultSharePolicy(),
	}); rerr != nil {
		t.Fatalf("registering the admin's own share: %v", rerr)
	}

	admin, aerr := e.Auth.CreateAdmin(ctx, "alice", "Alice", pwOf(loginPassword))
	if aerr != nil {
		t.Fatalf("creating admin: %v", aerr)
	}
	bob, berr := e.Auth.CreateUser(ctx, "bob", "Bob", pwOf(loginPassword))
	if berr != nil {
		t.Fatalf("creating ordinary account: %v", berr)
	}
	if gerr := e.Core.GrantEveryShare(ctx, admin); gerr != nil {
		t.Fatalf("granting admin: %v", gerr)
	}
	if gerr := e.Core.GrantEveryShare(ctx, bob); gerr != nil {
		t.Fatalf("granting bob: %v", gerr)
	}

	plainVp, pverr := vfs.ParseVpath("openshare")
	if pverr != nil {
		t.Fatalf("parsing the plain vpath: %v", pverr)
	}
	cryptVp, cverr := vfs.ParseVpath("cryptshare")
	if cverr != nil {
		t.Fatalf("parsing the encrypted vpath: %v", cverr)
	}
	adminVp, averr := vfs.ParseVpath("adminshare")
	if averr != nil {
		t.Fatalf("parsing the admin vpath: %v", averr)
	}

	// Bob owns the plain and the (later encrypted) link; alice owns a
	// third, unrelated one. The administrative listing has to surface
	// bob's plain link under alice's own request, which only happens by
	// going through ListAllLinks rather than the caller's own, and bob's
	// own listing must not surface alice's.
	bobPlainRes, rerr := e.Core.Resolve(core.UserID(bob), plainVp, acl.Share)
	if rerr != nil {
		t.Fatalf("resolving the plain share for bob: %v", rerr)
	}
	bobCryptRes, rerr := e.Core.Resolve(core.UserID(bob), cryptVp, acl.Share)
	if rerr != nil {
		t.Fatalf("resolving the encrypted share for bob: %v", rerr)
	}
	adminRes, rerr := e.Core.Resolve(core.UserID(admin), adminVp, acl.Share)
	if rerr != nil {
		t.Fatalf("resolving admin's own share: %v", rerr)
	}
	if _, _, lerr := e.Core.CreateLink(ctx, bobPlainRes, core.LinkSpec{Perms: acl.Read, MaxDown: -1}); lerr != nil {
		t.Fatalf("minting bob's plain link: %v", lerr)
	}
	if _, _, lerr := e.Core.CreateLink(ctx, bobCryptRes, core.LinkSpec{Perms: acl.Read, MaxDown: -1}); lerr != nil {
		t.Fatalf("minting bob's link into the share that becomes encrypted: %v", lerr)
	}
	if _, _, lerr := e.Core.CreateLink(ctx, adminRes, core.LinkSpec{Perms: acl.Read, MaxDown: -1}); lerr != nil {
		t.Fatalf("minting alice's own link: %v", lerr)
	}
	if eerr := e.Core.EnableEncryption(ctx, 2, encryptionSettingsForTest()); eerr != nil {
		t.Fatalf("enabling encryption: %v", eerr)
	}

	base := serveCompatEngine(t, e)
	adminAuth := davAuth(t, e, base, admin)
	bobToken, _, terr := e.Auth.CreateSyncCredential(ctx, bob, "bob device")
	if terr != nil {
		t.Fatalf("minting bob's credential: %v", terr)
	}

	get := func(auth func(*http.Request)) []byte {
		t.Helper()
		req := newReq(t, http.MethodGet,
			base+"/ocs/v2.php/apps/files_sharing/api/v1/shares?all_links=true", nil)
		auth(req)
		req.Header.Set("Accept", "application/json")
		resp, err := compatClient().Do(req)
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("GET shares failed: %v", err)
		}
		body := readAllBody(t, resp.Body)
		closeRespBody(t, resp)
		return body
	}

	// An administrator with the flag sees a link owned by another account.
	adminBody := get(func(r *http.Request) { r.Header.Set("Authorization", adminAuth) })
	if !strings.Contains(string(adminBody), "openshare") {
		t.Errorf("administrator did not see bob's plain link: %s", adminBody)
	}
	if !strings.Contains(string(adminBody), "adminshare") {
		t.Errorf("administrator did not see its own link: %s", adminBody)
	}
	if strings.Contains(string(adminBody), "cryptshare") {
		t.Errorf("administrator saw the encrypted-share link: %s", adminBody)
	}

	// An ordinary account with the same flag sees only its own links: bob's
	// plain link, never alice's, and never the encrypted one.
	bobBody := get(func(r *http.Request) { r.SetBasicAuth("bob", bobToken) })
	if !strings.Contains(string(bobBody), "openshare") {
		t.Errorf("bob did not see his own plain link: %s", bobBody)
	}
	if strings.Contains(string(bobBody), "adminshare") {
		t.Errorf("bob saw alice's link: %s", bobBody)
	}
	if strings.Contains(string(bobBody), "cryptshare") {
		t.Errorf("bob saw the encrypted-share link: %s", bobBody)
	}
}
