//go:build linux && compat_nc

package lifecycle_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/lifecycle"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/auth"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/state"
)

// SEC-AUTH-07: Admin OIDC unlink must not assign a hardcoded fallback password.
func TestRegressionAdminOIDCUnlinkDoesNotSetHardcodedPassword(t *testing.T) {
	ctx := context.Background()
	e, err := lifecycle.Open(ctx, lifecycle.Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cerr := e.Close(); cerr != nil {
			t.Error(cerr)
		}
	})

	if _, aerr := e.Auth.CreateAdmin(ctx, "auditadmin", "Admin", pwOf(loginPassword)); aerr != nil {
		t.Fatal(aerr)
	}
	uid, uerr := e.Auth.CreateUser(ctx, "audituser", "User", pwOf(loginPassword))
	if uerr != nil {
		t.Fatal(uerr)
	}
	if lerr := e.Auth.CreateOIDCLink(ctx, uid, "https://idp.example", "sub-123"); lerr != nil {
		t.Fatal(lerr)
	}

	base := serve(t, e)
	admin := signIn(t, base, "auditadmin", loginPassword)

	status, body := mutate(t, http.MethodDelete, fmt.Sprintf("%s/api/v1/admin/users/%d/oidc", base, uid), admin.cookie, admin.csrf, nil)
	if status != http.StatusNoContent {
		t.Fatalf("admin unlink returned status %d: %v", status, body)
	}

	login := postJSON(t, base+"/api/v1/auth/login", map[string]string{
		"login":    "audituser",
		"password": "ResetRequired123!",
	})
	if login.sessionCookie() != nil {
		t.Fatal("login succeeded with hardcoded ResetRequired123! password; must be refused")
	}
	if login.status != http.StatusUnauthorized {
		t.Fatalf("login with ResetRequired123! returned %d, want 401", login.status)
	}
}

// SEC-AUTH-08: Self-service OIDC unlink must verify user password.
func TestRegressionSelfOIDCUnlinkRequiresPassword(t *testing.T) {
	base, e, uid := bootForLogin(t)
	cookie, csrf := signedIn(t, base)
	ctx := context.Background()

	if lerr := e.Auth.CreateOIDCLink(ctx, uid, "https://idp.example", "sub-456"); lerr != nil {
		t.Fatal(lerr)
	}

	// Attempt unlink with an incorrect password.
	status, _ := mutate(t, http.MethodDelete, base+"/api/v1/account/oidc-link", cookie, csrf, map[string]string{
		"current": "wrong-password",
	})
	if status != http.StatusUnauthorized {
		t.Fatalf("unlink with wrong password returned status %d, want 401", status)
	}

	// Attempt unlink with empty password.
	emptyStatus, _ := mutate(t, http.MethodDelete, base+"/api/v1/account/oidc-link", cookie, csrf, map[string]string{
		"current": "",
	})
	if emptyStatus != http.StatusUnprocessableEntity {
		t.Fatalf("unlink with empty password returned status %d, want 422", emptyStatus)
	}

	// Unlink with correct password succeeds.
	okStatus, _ := mutate(t, http.MethodDelete, base+"/api/v1/account/oidc-link", cookie, csrf, map[string]string{
		"current": loginPassword,
	})
	if okStatus != http.StatusNoContent {
		t.Fatalf("unlink with correct password returned status %d, want 204", okStatus)
	}
}

// SEC-AUTH-09: Failed TOTP re-enrollment must preserve existing factor.
func TestRegressionFailedTOTPReEnrollmentPreservesExistingFactor(t *testing.T) {
	base, e, uid := bootForLogin(t)
	cookie, csrf := signedIn(t, base)
	ctx := context.Background()

	origSecret := "JBSWY3DPEHPK3PXP"
	if err := e.Auth.EnrollTOTP(ctx, uid, origSecret); err != nil {
		t.Fatal(err)
	}
	hasBefore, err := e.Auth.HasTOTP(ctx, uid)
	if err != nil || !hasBefore {
		t.Fatalf("factor not enrolled: has=%v err=%v", hasBefore, err)
	}

	// Attempt re-enrollment with an invalid verification code.
	status, body := mutate(t, http.MethodPost, base+"/api/v1/account/totp/enroll", cookie, csrf, map[string]string{
		"current": loginPassword,
		"secret":  "GEZDGNBVGY3TQOJQ",
		"code":    "000000",
	})
	if status != http.StatusUnauthorized {
		t.Fatalf("invalid code enrollment returned status %d, want 401: %v", status, body)
	}

	// The existing factor must still be intact and active.
	hasAfter, err := e.Auth.HasTOTP(ctx, uid)
	if err != nil || !hasAfter {
		t.Fatalf("existing factor was erased: has=%v err=%v", hasAfter, err)
	}

	// Password-only login must still demand TOTP challenge.
	login := postJSON(t, base+"/api/v1/auth/login", map[string]string{
		"login":    loginName,
		"password": loginPassword,
	})
	if login.sessionCookie() != nil {
		t.Fatal("password-only login succeeded without TOTP challenge; factor was removed")
	}
	if login.status != http.StatusOK || login.field("challenge") == "" {
		t.Fatalf("expected TOTP challenge, got status %d body %v", login.status, login.body)
	}
}

// SEC-AUTH-10: WebDAV trash mount must enforce credential permission mask and allowed shares.
func TestRegressionAppPasswordTrashAndShareScopeEnforced(t *testing.T) {
	base, e, uid := bootForLogin(t)
	ctx := context.Background()

	hostAllowed := t.TempDir()
	if err := os.WriteFile(filepath.Join(hostAllowed, "allowed.txt"), []byte("allowed content"), 0o600); err != nil {
		t.Fatal(err)
	}
	defAllowed, err := e.Core.CreateShare(ctx, core.ShareSpec{Name: "allowed", Host: hostAllowed})
	if err != nil {
		t.Fatal(err)
	}
	if _, gerr := e.Core.CreateGrant(ctx, core.GrantSpec{
		User: &uid, Share: defAllowed.ID, Allow: acl.Read | acl.Download | acl.Write | acl.Create | acl.Delete,
		Inherit: true, Label: "allowed",
	}); gerr != nil {
		t.Fatal(gerr)
	}

	hostExcluded := t.TempDir()
	if werr := os.WriteFile(filepath.Join(hostExcluded, "excluded.txt"), []byte("excluded content"), 0o600); werr != nil {
		t.Fatal(werr)
	}
	defExcluded, derr := e.Core.CreateShare(ctx, core.ShareSpec{Name: "excluded", Host: hostExcluded})
	if derr != nil {
		t.Fatal(derr)
	}
	defExcluded.TrashEnabled = true
	if rerr := e.Core.RegisterShare(ctx, defExcluded); rerr != nil {
		t.Fatal(rerr)
	}
	if _, gerr := e.Core.CreateGrant(ctx, core.GrantSpec{
		User: &uid, Share: defExcluded.ID, Allow: acl.Read | acl.Download | acl.Write | acl.Create | acl.Delete,
		Inherit: true, Label: "excluded",
	}); gerr != nil {
		t.Fatal(gerr)
	}

	// Mint an app password restricted to read/download on share "allowed" only.
	token, err := e.Auth.CreateAppPassword(ctx, uid, "backup-agent", auth.Scope{
		Perms:  uint16(acl.Read | acl.Download),
		Shares: []string{"allowed"},
	}, 0)
	if err != nil {
		t.Fatal(err)
	}

	// Reading from the excluded share via WebDAV must be rejected.
	status, body := appPasswordAuthed(t, http.MethodGet, base+"/dav/excluded/excluded.txt", token)
	if status != http.StatusNotFound {
		t.Fatalf("read on excluded share returned %d, want 404: %s", status, body)
	}

	// Deleting trash via /dav-trash/trash with read-only token must be rejected with 403.
	trashStatus, trashBody := appPasswordAuthed(t, http.MethodDelete, base+"/dav-trash/trash", token)
	if trashStatus != http.StatusForbidden {
		t.Fatalf("delete trash with read-only token returned %d, want 403: %s", trashStatus, trashBody)
	}
}

// SEC-AUTH-11: App password with excessive expiration days must be rejected, preventing overflow.
func TestRegressionAppPasswordExcessiveExpiryRefused(t *testing.T) {
	base, _, _ := bootForLogin(t)
	cookie, csrf := signedIn(t, base)

	status, body := mutate(t, http.MethodPost, base+"/api/v1/account/app-passwords", cookie, csrf, map[string]any{
		"current":         loginPassword,
		"name":            "overflow-token",
		"expires_in_days": 106752,
	})
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("overflow expiry returned status %d, want 422: %v", status, body)
	}
}

// SEC-SMB-01: Group grants and group deny rules must expand to group members in SMB publication.
func TestRegressionSMBGroupGrantsExpandedToMembers(t *testing.T) {
	ctx := context.Background()
	e, err := lifecycle.Open(ctx, lifecycle.Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cerr := e.Close(); cerr != nil {
			t.Error(cerr)
		}
	})

	aliceID, err := e.Auth.CreateUser(ctx, "alice", "Alice", pwOf(loginPassword))
	if err != nil {
		t.Fatal(err)
	}
	bobID, err := e.Auth.CreateUser(ctx, "bob", "Bob", pwOf(loginPassword))
	if err != nil {
		t.Fatal(err)
	}
	groupID, err := e.State.CreateGroup(ctx, "engineers")
	if err != nil {
		t.Fatal(err)
	}
	if merr := e.State.AddMembership(ctx, aliceID, groupID); merr != nil {
		t.Fatal(merr)
	}

	// Persist a whole-share group allow grant.
	if _, perr := e.State.PersistGrant(ctx, state.GrantRow{
		Group:   &groupID,
		Share:   1,
		Subpath: "",
		Allow:   uint16(acl.Read | acl.Download),
		Inherit: true,
		Label:   "PublicShare",
	}, 0); perr != nil {
		t.Fatal(perr)
	}

	// Persist a whole-share group deny grant on share 2.
	if _, perr := e.State.PersistGrant(ctx, state.GrantRow{
		Group:   &groupID,
		Share:   2,
		Subpath: "",
		Deny:    uint16(acl.Read),
		Inherit: true,
		Label:   "RestrictedShare",
	}, 0); perr != nil {
		t.Fatal(perr)
	}

	memberships, err := e.State.Memberships(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := e.State.ListGrants(ctx, state.GrantFilter{})
	if err != nil {
		t.Fatal(err)
	}
	grants := lifecycle.GrantsOf(rows, memberships)

	// Alice (group member) must receive grants for share 1 and share 2.
	var aliceShare1, aliceShare2Deny bool
	for _, g := range grants {
		if g.User == aliceID && g.Share == 1 && g.AllowRead {
			aliceShare1 = true
		}
		if g.User == aliceID && g.Share == 2 && g.Denies {
			aliceShare2Deny = true
		}
		if g.User == bobID {
			t.Errorf("bob received grant %+v despite not being in the group", g)
		}
	}
	if !aliceShare1 {
		t.Error("alice did not receive expanded group allow grant for share 1")
	}
	if !aliceShare2Deny {
		t.Error("alice did not receive expanded group deny rule for share 2")
	}
}

// SEC-TRASH-02: Subfolder grants must not leak other subfolders' trash entries, allow cross-subfolder restore or purge.
func TestRegressionTrashSubfolderIsolation(t *testing.T) {
	ctx := context.Background()
	e, err := lifecycle.Open(ctx, lifecycle.Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cerr := e.Close(); cerr != nil {
			t.Error(cerr)
		}
	})

	adminID, err := e.Auth.CreateAdmin(ctx, "admin", "Admin", pwOf(loginPassword))
	if err != nil {
		t.Fatal(err)
	}
	aliceID, err := e.Auth.CreateUser(ctx, "alice", "Alice", pwOf(loginPassword))
	if err != nil {
		t.Fatal(err)
	}

	hostDir := t.TempDir()
	if merr := os.MkdirAll(filepath.Join(hostDir, "public"), 0o750); merr != nil {
		t.Fatal(merr)
	}
	if merr := os.MkdirAll(filepath.Join(hostDir, "confidential"), 0o750); merr != nil {
		t.Fatal(merr)
	}
	if werr := os.WriteFile(filepath.Join(hostDir, "confidential", "secret.txt"), []byte("secret data"), 0o600); werr != nil {
		t.Fatal(werr)
	}

	def, derr := e.Core.CreateShare(ctx, core.ShareSpec{Name: "files", Host: hostDir})
	if derr != nil {
		t.Fatal(derr)
	}
	def.TrashEnabled = true
	if rerr := e.Core.RegisterShare(ctx, def); rerr != nil {
		t.Fatal(rerr)
	}

	// Admin has whole-share access. Alice only has access to subfolder "public".
	if _, gerr := e.Core.CreateGrant(ctx, core.GrantSpec{
		User: &adminID, Share: def.ID, Allow: acl.Read | acl.Write | acl.Create | acl.Delete | acl.Download, Inherit: true, Label: "admin-files",
	}); gerr != nil {
		t.Fatal(gerr)
	}
	if _, gerr := e.Core.CreateGrant(ctx, core.GrantSpec{
		User: &aliceID, Share: def.ID, Subpath: "public", Allow: acl.Read | acl.Write | acl.Create | acl.Delete | acl.Download, Inherit: true, Label: "alice-public",
	}); gerr != nil {
		t.Fatal(gerr)
	}

	// Admin deletes confidential file.
	confPath, perr := vfs.ParseVpath("/admin-files/confidential/secret.txt")
	if perr != nil {
		t.Fatal(perr)
	}
	resolved, err := e.Core.Resolve(core.UserID(adminID), confPath, acl.Delete)
	if err != nil {
		t.Fatal(err)
	}
	if delErr := e.Core.Delete(ctx, resolved, false); delErr != nil {
		t.Fatal(delErr)
	}

	// Alice lists trash.
	alicePath, perr := vfs.ParseVpath("/alice-public")
	if perr != nil {
		t.Fatal(perr)
	}
	aliceResolved, err := e.Core.Resolve(core.UserID(aliceID), alicePath, acl.Read)
	if err != nil {
		t.Fatal(err)
	}
	aliceTrash, err := e.Core.TrashList(ctx, aliceResolved)
	if err != nil {
		t.Fatal(err)
	}
	if len(aliceTrash) != 0 {
		t.Fatalf("alice (restricted to public) saw confidential trash: %+v", aliceTrash)
	}

	// Admin lists trash and sees it.
	adminPath, perr := vfs.ParseVpath("/admin-files")
	if perr != nil {
		t.Fatal(perr)
	}
	adminResolved, err := e.Core.Resolve(core.UserID(adminID), adminPath, acl.Read)
	if err != nil {
		t.Fatal(err)
	}
	adminTrash, err := e.Core.TrashList(ctx, adminResolved)
	if err != nil {
		t.Fatal(err)
	}
	if len(adminTrash) != 1 {
		t.Fatalf("admin expected 1 trash entry, got %d", len(adminTrash))
	}

	// Alice attempts to purge the confidential trash entry directly -> must fail.
	trashID := adminTrash[0].ID
	if err := e.Core.TrashPurge(ctx, aliceResolved, &trashID); err == nil {
		t.Fatal("alice purged confidential trash entry; expected ErrDenied")
	}

	// Alice attempts to restore the confidential trash entry -> must fail.
	if _, err := e.Core.TrashRestore(ctx, aliceResolved, trashID); err == nil {
		t.Fatal("alice restored confidential trash entry; expected ErrDenied")
	}
}

// SEC-UPL-03: Public drop link must enforce RequestBody limit and refuse oversized bodies.
func TestRegressionPublicDropLinkEnforcesRequestBodyLimit(t *testing.T) {
	base, token, _ := linkEngineOverFolderAt(t, acl.Create)

	// Body exceeding limits.RequestBody (1 MiB)
	oversized := bytes.Repeat([]byte("A"), 1<<20+100)
	req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("%s/s/%s/drop?name=big.txt", base, token), bytes.NewReader(oversized))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Length", strconv.Itoa(len(oversized)))
	resp, err := testClient().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	bodyBytes, rerr := io.ReadAll(resp.Body)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if cerr := resp.Body.Close(); cerr != nil {
		t.Error(cerr)
	}
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized drop returned %d (%s), want 413", resp.StatusCode, string(bodyBytes))
	}
}

// SEC-AUTH-12: Re-creating a user with the same name must not inherit the previous user's home directory.
func TestRegressionDeletedUserHomeDirectoryNotInherited(t *testing.T) {
	ctx := context.Background()
	e, err := lifecycle.Open(ctx, lifecycle.Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cerr := e.Close(); cerr != nil {
			t.Error(cerr)
		}
	})

	homesDir := t.TempDir()
	if herr := e.Core.EnableHomes(ctx, homesDir); herr != nil {
		t.Fatal(herr)
	}

	// 1. Create user bob and seed a private file in his home.
	bob1, err := e.Auth.CreateUser(ctx, "bob", "Bob", pwOf(loginPassword))
	if err != nil {
		t.Fatal(err)
	}
	bobHome, berr := vfs.ParseVpath("/Home/secret.txt")
	if berr != nil {
		t.Fatal(berr)
	}
	bobRes, err := e.Core.Resolve(core.UserID(bob1), bobHome, acl.Write|acl.Create)
	if err != nil {
		t.Fatal(err)
	}
	if _, cerr := e.Core.CreateFile(ctx, bobRes, vfs.DurableOpts{}, nil, func(f *vfs.File) error {
		_, w := f.WriteAt([]byte("confidential notes"), 0)
		return w
	}); cerr != nil {
		t.Fatal(cerr)
	}

	// 2. Delete user bob and clean his home.
	if clerr := e.Core.CleanupHome(ctx, core.UserID(bob1)); clerr != nil {
		t.Fatal(clerr)
	}
	if derr := e.Auth.DeleteUser(ctx, int64(bob1)); derr != nil {
		t.Fatal(derr)
	}

	// 3. Create a new user bob (new ID).
	bob2, err := e.Auth.CreateUser(ctx, "bob", "Bob Second", pwOf(loginPassword))
	if err != nil {
		t.Fatal(err)
	}
	bob2Res, err := e.Core.Resolve(core.UserID(bob2), bobHome, acl.Read)
	if err == nil {
		// If secret.txt can be opened, it means the old file was inherited!
		_, stream, serr := e.Core.OpenStream(ctx, bob2Res, nil)
		if serr == nil {
			if clerr := stream.Close(); clerr != nil {
				t.Error(clerr)
			}
			t.Fatal("new user bob inherited the deleted user's secret file")
		}
	}
}
