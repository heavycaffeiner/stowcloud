//go:build linux && compat_nc

package lifecycle_test

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"

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
	if err := os.WriteFile(filepath.Join(hostExcluded, "excluded.txt"), []byte("excluded content"), 0o600); err != nil {
		t.Fatal(err)
	}
	defExcluded, err := e.Core.CreateShare(ctx, core.ShareSpec{Name: "excluded", Host: hostExcluded})
	if err != nil {
		t.Fatal(err)
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
	if err := e.State.AddMembership(ctx, aliceID, groupID); err != nil {
		t.Fatal(err)
	}

	// Persist a whole-share group allow grant.
	if _, err := e.State.PersistGrant(ctx, state.GrantRow{
		Group:   &groupID,
		Share:   1,
		Subpath: "",
		Allow:   uint16(acl.Read | acl.Download),
		Inherit: true,
		Label:   "PublicShare",
	}, 0); err != nil {
		t.Fatal(err)
	}

	// Persist a whole-share group deny grant on share 2.
	if _, err := e.State.PersistGrant(ctx, state.GrantRow{
		Group:   &groupID,
		Share:   2,
		Subpath: "",
		Deny:    uint16(acl.Read),
		Inherit: true,
		Label:   "RestrictedShare",
	}, 0); err != nil {
		t.Fatal(err)
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
