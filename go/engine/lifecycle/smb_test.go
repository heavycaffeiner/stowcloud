//go:build linux

package lifecycle_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/secret"
	"github.com/heavycaffeiner/stowcloud/go/engine/lifecycle"
)

// Setting the protocol credential needs the account password.
//
// It changes how the account authenticates over a second protocol, which is
// not something a session alone should decide: somebody who walked past an
// unlocked screen would otherwise give themselves a mount.
func TestSettingTheProtocolPasswordNeedsTheAccountPassword(t *testing.T) {
	base, _, _ := bootForLogin(t)
	cookie, csrf := signedIn(t, base)

	for _, body := range []map[string]any{
		{"new": "a-long-enough-protocol-password"},
		{"current": "not-the-password", "new": "a-long-enough-protocol-password"},
	} {
		status, got := mutate(t, http.MethodPost, base+"/api/v1/account/smb/password",
			cookie, csrf, body)
		if status == http.StatusOK {
			t.Errorf("%v set the protocol password: %v", body, got)
		}
	}
}

// The credential is set, and the state says the protocol works.
func TestSettingTheProtocolPassword(t *testing.T) {
	base, _, _ := bootForLogin(t)
	cookie, csrf := signedIn(t, base)

	status, body := mutate(t, http.MethodPost, base+"/api/v1/account/smb/password",
		cookie, csrf, map[string]any{
			"current": loginPassword,
			"new":     "a-long-enough-protocol-password",
		})
	if status != http.StatusOK {
		t.Fatalf("setting answered %d: %v", status, body)
	}

	// The answer is the state, not a bare success: every route here changes
	// it, and a screen that had to ask again could show the previous one.
	if stringField(body, "credential") != "account" {
		t.Errorf("the protocol reports credential %v after one was set", body["credential"])
	}
	if stringField(body, "reason") != "" {
		t.Errorf("a working credential carries the reason %v", body["reason"])
	}

	// And no credential material came back.
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"a-long-enough-protocol-password", "hash", "secret"} {
		if strings.Contains(string(raw), leak) {
			t.Errorf("the response carries %q: %s", leak, raw)
		}
	}
}

// A password under the floor is refused here too.
func TestAWeakProtocolPasswordIsRefused(t *testing.T) {
	base, _, _ := bootForLogin(t)
	cookie, csrf := signedIn(t, base)

	status, _ := mutate(t, http.MethodPost, base+"/api/v1/account/smb/password",
		cookie, csrf, map[string]any{"current": loginPassword, "new": "short"})
	if status == http.StatusOK {
		t.Error("a protocol password under the floor was accepted")
	}
}

// Clearing says whether protocol access survives it.
//
// Clearing is sometimes losing that access entirely, and a bare success there
// reads as "nothing changed" to somebody who has just lost a mount.
func TestClearingTheProtocolPasswordSaysWhetherAccessSurvives(t *testing.T) {
	base, _, _ := bootForLogin(t)
	cookie, csrf := signedIn(t, base)

	if status, body := mutate(t, http.MethodPost, base+"/api/v1/account/smb/password",
		cookie, csrf, map[string]any{
			"current": loginPassword, "new": "a-long-enough-protocol-password",
		}); status != http.StatusOK {
		t.Fatalf("setting answered %d: %v", status, body)
	}

	status, body := mutate(t, http.MethodDelete, base+"/api/v1/account/smb/password",
		cookie, csrf, map[string]any{"current": loginPassword})
	if status != http.StatusOK {
		t.Fatalf("clearing answered %d: %v", status, body)
	}

	if _, present := body["revertible"]; !present {
		t.Error("the answer does not say whether access can be restored")
	}
	state, ok := body["state"].(map[string]any)
	if !ok {
		t.Fatalf("no state in %v", body)
	}
	if stringField(state, "credential") != "none" {
		t.Errorf("the credential is %v after being cleared", state["credential"])
	}
	if stringField(state, "reason") != "not_set" {
		t.Errorf("the reason is %v, want not_set", state["reason"])
	}
}

// An opted-out account does not get protocol access back by clearing.
//
// The separate password was the only thing making the protocol work for it,
// so clearing is losing that access rather than falling back to the account
// password. A flag that were always true would say the opposite here, which
// is why this case exists alongside the ordinary one above.
func TestClearingIsNotRevertibleForAnOptedOutAccount(t *testing.T) {
	base, _, _ := bootForLogin(t)
	cookie, csrf := signedIn(t, base)

	if status, body := mutate(t, http.MethodPost, base+"/api/v1/account/smb/password",
		cookie, csrf, map[string]any{
			"current": loginPassword, "new": "a-long-enough-protocol-password",
		}); status != http.StatusOK {
		t.Fatalf("setting answered %d: %v", status, body)
	}
	// Opting out after the credential exists, since setting one clears the
	// opt-out by design.
	if status, body := mutate(t, http.MethodPost, base+"/api/v1/account/smb",
		cookie, csrf, map[string]any{"current": loginPassword, "opt_out": true}); status != http.StatusOK {
		t.Fatalf("opting out answered %d: %v", status, body)
	}

	status, body := mutate(t, http.MethodDelete, base+"/api/v1/account/smb/password",
		cookie, csrf, map[string]any{"current": loginPassword})
	if status != http.StatusOK {
		t.Fatalf("clearing answered %d: %v", status, body)
	}
	if boolField(body, "revertible") {
		t.Error("an opted-out account was told it can restore protocol access by clearing")
	}
}

// Opting out forces the enabled switch off.
//
// Opting out is the stronger statement: a credential that is not stored
// cannot be live, so a state reporting both would be describing something
// that cannot exist.
func TestOptingOutForcesTheProtocolOff(t *testing.T) {
	base, _, _ := bootForLogin(t)
	cookie, csrf := signedIn(t, base)

	status, body := mutate(t, http.MethodPost, base+"/api/v1/account/smb",
		cookie, csrf, map[string]any{
			"current": loginPassword, "opt_out": true, "enabled": true,
		})
	if status != http.StatusOK {
		t.Fatalf("answered %d: %v", status, body)
	}

	if !boolField(body, "opt_out") {
		t.Error("the opt-out was not recorded")
	}
	if boolField(body, "enabled") {
		t.Error("opting out left the protocol enabled, which cannot be true at once")
	}
	if stringField(body, "reason") != "opted_out" {
		t.Errorf("the reason is %v, want opted_out", body["reason"])
	}
}

// The access switches need the account password as well.
func TestTheProtocolAccessSwitchesNeedThePassword(t *testing.T) {
	base, _, _ := bootForLogin(t)
	cookie, csrf := signedIn(t, base)

	status, _ := mutate(t, http.MethodPost, base+"/api/v1/account/smb",
		cookie, csrf, map[string]any{"opt_out": true})
	if status == http.StatusOK {
		t.Error("a session alone changed the protocol access switches")
	}
}

// The index estimate measures the corpus and says what an index would cost.
func TestTheIndexEstimate(t *testing.T) {
	base, cookie, csrf, _, _ := adminEngine(t)
	makeShare(t, base, cookie, csrf, "docs")

	status, raw := withCookie(t, http.MethodGet, base+"/api/v1/admin/index/estimate", cookie)
	if status != http.StatusOK {
		t.Fatalf("answered %d: %s", status, raw)
	}

	var view map[string]any
	if err := json.Unmarshal(raw, &view); err != nil {
		t.Fatalf("decoding %s: %v", raw, err)
	}

	// Decimal strings, because a large corpus runs past 2^53 and the figure
	// an operator plans against would round.
	for _, field := range []string{"index_bytes", "files", "name_bytes"} {
		value := stringField(view, field)
		if value == "" {
			t.Errorf("%s is missing from %s", field, raw)
			continue
		}
		if _, err := strconv.ParseUint(value, 10, 64); err != nil {
			t.Errorf("%s is %q, which is not a decimal", field, value)
		}
	}

	// The confidence and the derivation. An estimate presented without them
	// invites planning against a figure the estimator is itself unsure of.
	if stringField(view, "confidence") == "" {
		t.Error("the estimate carries no confidence")
	}
	if stringField(view, "formula") == "" {
		t.Error("the estimate carries no derivation, so a wrong term cannot be found")
	}
}

// The index estimate needs an administrator.
func TestTheIndexEstimateNeedsAnAdministrator(t *testing.T) {
	base, _, _, plainCookie, _ := adminEngine(t)

	status, _ := withCookie(t, http.MethodGet,
		base+"/api/v1/admin/index/estimate", plainCookie)
	if status != http.StatusForbidden {
		t.Errorf("an ordinary account read the index estimate: %d", status)
	}
}

// The stored second-factor policy decides protocol access after a restart.
//
// The value was read out of the document and then dropped on the floor, so an
// operator who set the blocking policy went on running under the permissive
// one: every enrolled account kept the protocol access the operator had just
// revoked, and the settings screen showed the revocation as applied.
func TestTheStoredSecondFactorPolicyBlocksTheProtocol(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	// Saved before the engine that serves it opens, which is the ordinary
	// order: an operator configures, the server restarts, the value applies.
	first, err := lifecycle.Open(ctx, lifecycle.Options{DataDir: dir})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	if merr := first.State.MergeSettings(ctx, "smb", map[string]any{
		"totp_policy": "block",
	}); merr != nil {
		t.Fatalf("saving the policy: %v", merr)
	}
	if _, cerr := first.Auth.CreateUser(ctx, loginName, "Alice",
		secret.New([]byte(loginPassword))); cerr != nil {
		t.Fatalf("creating the account: %v", cerr)
	}
	if cerr := first.Close(); cerr != nil {
		t.Fatalf("closing: %v", cerr)
	}

	// A second engine over the same directory, so the policy is read from the
	// document rather than from anything left in memory.
	e, err := lifecycle.Open(ctx, lifecycle.Options{DataDir: dir})
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}
	t.Cleanup(func() {
		if cerr := e.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	})
	base := serve(t, e)
	cookie, csrf := signedIn(t, base)

	// A credential exists, so the only thing standing between this account
	// and the protocol is the policy.
	if status, body := mutate(t, http.MethodPost, base+"/api/v1/account/smb/password",
		cookie, csrf, map[string]any{
			"current": loginPassword, "new": "a-long-enough-protocol-password",
		}); status != http.StatusOK {
		t.Fatalf("setting the protocol password answered %d: %v", status, body)
	}

	_, setup := mutate(t, http.MethodPost, base+"/api/v1/account/totp/setup", cookie, csrf,
		map[string]string{"current": loginPassword})
	secretB32 := stringField(setup, "secret")
	if status, body := mutate(t, http.MethodPost, base+"/api/v1/account/totp/enroll",
		cookie, csrf, map[string]string{
			"current": loginPassword,
			"secret":  secretB32,
			"code":    referenceCode(t, secretB32, nowStep()),
		}); status != http.StatusOK {
		t.Fatalf("enrolling answered %d: %v", status, body)
	}

	// Reading the state through a route that returns it. Setting the access
	// switches changes nothing here and answers with the current state.
	status, state := mutate(t, http.MethodPost, base+"/api/v1/account/smb",
		cookie, csrf, map[string]any{"current": loginPassword, "enabled": true})
	if status != http.StatusOK {
		t.Fatalf("reading the state answered %d: %v", status, state)
	}

	if stringField(state, "reason") != "totp_blocked" {
		t.Errorf("an enrolled account reports %v under the blocking policy, want totp_blocked",
			state["reason"])
	}
	if stringField(state, "credential") == "smb" {
		t.Error("an enrolled account keeps protocol access the blocking policy revoked")
	}
}

// A policy name nobody wrote blocks rather than admits.
//
// Guessing wrong in the permissive direction hands protocol access to exactly
// the accounts an operator with an unusual value may have been trying to shut
// out.
func TestAnUnknownSecondFactorPolicyBlocks(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	first, err := lifecycle.Open(ctx, lifecycle.Options{DataDir: dir})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	if merr := first.State.MergeSettings(ctx, "smb", map[string]any{
		"totp_policy": "permissive-ish",
	}); merr != nil {
		t.Fatalf("saving the policy: %v", merr)
	}
	if _, cerr := first.Auth.CreateUser(ctx, loginName, "Alice",
		secret.New([]byte(loginPassword))); cerr != nil {
		t.Fatalf("creating the account: %v", cerr)
	}
	if cerr := first.Close(); cerr != nil {
		t.Fatalf("closing: %v", cerr)
	}

	e, err := lifecycle.Open(ctx, lifecycle.Options{DataDir: dir})
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}
	t.Cleanup(func() {
		if cerr := e.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	})
	base := serve(t, e)
	cookie, csrf := signedIn(t, base)

	_, setup := mutate(t, http.MethodPost, base+"/api/v1/account/totp/setup", cookie, csrf,
		map[string]string{"current": loginPassword})
	secretB32 := stringField(setup, "secret")
	if status, body := mutate(t, http.MethodPost, base+"/api/v1/account/totp/enroll",
		cookie, csrf, map[string]string{
			"current": loginPassword,
			"secret":  secretB32,
			"code":    referenceCode(t, secretB32, nowStep()),
		}); status != http.StatusOK {
		t.Fatalf("enrolling answered %d: %v", status, body)
	}

	status, state := mutate(t, http.MethodPost, base+"/api/v1/account/smb",
		cookie, csrf, map[string]any{"current": loginPassword, "enabled": true})
	if status != http.StatusOK {
		t.Fatalf("reading the state answered %d: %v", status, state)
	}
	if stringField(state, "reason") != "totp_blocked" {
		t.Errorf("an unrecognised policy admitted an enrolled account: reason %v",
			state["reason"])
	}
}

// The permissive policy lets an enrolled account keep its separate password.
//
// It is the default and the whole reason a separate password exists: an
// account with a second factor cannot present it over the protocol, so it is
// told to set one. A policy that blocked here would make that pointless.
//
// This case is what separates the two branches. Misspelling the blocking name
// falls through to the default, which also blocks, so the blocking tests
// above pass either way; only this one notices when the permissive branch
// stops being reachable.
func TestThePermissivePolicyKeepsProtocolAccess(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	first, err := lifecycle.Open(ctx, lifecycle.Options{DataDir: dir})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	if merr := first.State.MergeSettings(ctx, "smb", map[string]any{
		"totp_policy": "require_separate",
	}); merr != nil {
		t.Fatalf("saving the policy: %v", merr)
	}
	if _, cerr := first.Auth.CreateUser(ctx, loginName, "Alice",
		secret.New([]byte(loginPassword))); cerr != nil {
		t.Fatalf("creating the account: %v", cerr)
	}
	if cerr := first.Close(); cerr != nil {
		t.Fatalf("closing: %v", cerr)
	}

	e, err := lifecycle.Open(ctx, lifecycle.Options{DataDir: dir})
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}
	t.Cleanup(func() {
		if cerr := e.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	})
	base := serve(t, e)
	cookie, csrf := signedIn(t, base)

	if status, body := mutate(t, http.MethodPost, base+"/api/v1/account/smb/password",
		cookie, csrf, map[string]any{
			"current": loginPassword, "new": "a-long-enough-protocol-password",
		}); status != http.StatusOK {
		t.Fatalf("setting the protocol password answered %d: %v", status, body)
	}

	_, setup := mutate(t, http.MethodPost, base+"/api/v1/account/totp/setup", cookie, csrf,
		map[string]string{"current": loginPassword})
	secretB32 := stringField(setup, "secret")
	if status, body := mutate(t, http.MethodPost, base+"/api/v1/account/totp/enroll",
		cookie, csrf, map[string]string{
			"current": loginPassword,
			"secret":  secretB32,
			"code":    referenceCode(t, secretB32, nowStep()),
		}); status != http.StatusOK {
		t.Fatalf("enrolling answered %d: %v", status, body)
	}

	status, state := mutate(t, http.MethodPost, base+"/api/v1/account/smb",
		cookie, csrf, map[string]any{"current": loginPassword, "enabled": true})
	if status != http.StatusOK {
		t.Fatalf("reading the state answered %d: %v", status, state)
	}
	if stringField(state, "reason") == "totp_blocked" {
		t.Error("the permissive policy blocked an enrolled account holding a separate password")
	}
}

// bootWithSidecar opens an engine whose file-sharing settings name a rendered
// directory, and returns the engine with that directory.
//
// Saved before the engine that serves it opens, because the section decides
// things at construction: the credential file's path is fixed when auth is
// built, so a value written afterwards would not reach it.
func bootWithSidecar(t *testing.T) (*lifecycle.Engine, string) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	configDir := filepath.Join(t.TempDir(), "smb")

	first, err := lifecycle.Open(ctx, lifecycle.Options{DataDir: dir})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	if merr := first.State.MergeSettings(ctx, "smb", map[string]any{
		"enabled": true, "config_dir": configDir, "workgroup": "WORKGROUP",
	}); merr != nil {
		t.Fatalf("saving the settings: %v", merr)
	}
	if _, cerr := first.Auth.CreateUser(ctx, loginName, "Alice",
		secret.New([]byte(loginPassword))); cerr != nil {
		t.Fatalf("creating the account: %v", cerr)
	}
	if cerr := first.Close(); cerr != nil {
		t.Fatalf("closing: %v", cerr)
	}

	e, err := lifecycle.Open(ctx, lifecycle.Options{DataDir: dir})
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}
	t.Cleanup(func() {
		if cerr := e.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	})
	return e, configDir
}

// Turning file sharing off takes effect on the running server.
//
// The rendered files are how the setting reaches the daemon: their absence is
// what the sidecar reads as teardown, stopping it and pruning the credentials.
// The switch used to be read only when the process started, so an operator who
// turned sharing off kept serving every share until somebody restarted the
// container, and the settings screen showed it as off the whole time.
//
// Driven through the settings route rather than the engine, because that is
// where the defect was: the save stored the value and never pushed it.
func TestTurningFileSharingOffReachesTheSidecar(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	configDir := filepath.Join(t.TempDir(), "smb")

	first, err := lifecycle.Open(ctx, lifecycle.Options{DataDir: dir})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	if merr := first.State.MergeSettings(ctx, "smb", map[string]any{
		"enabled": true, "config_dir": configDir, "workgroup": "WORKGROUP",
	}); merr != nil {
		t.Fatalf("saving the settings: %v", merr)
	}
	if _, cerr := first.Auth.CreateAdmin(ctx, "root", "Root", pwOf(loginPassword)); cerr != nil {
		t.Fatalf("creating the administrator: %v", cerr)
	}
	if cerr := first.Close(); cerr != nil {
		t.Fatalf("closing: %v", cerr)
	}

	e, err := lifecycle.Open(ctx, lifecycle.Options{DataDir: dir})
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}
	t.Cleanup(func() {
		if cerr := e.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	})
	base := serve(t, e)

	admin := postJSON(t, base+"/api/v1/auth/login",
		map[string]string{"login": "root", "password": loginPassword})
	cookie, csrf := admin.sessionCookie(), admin.field("csrf")
	if cookie == nil {
		t.Fatalf("the administrator did not sign in: %d %v", admin.status, admin.body)
	}

	conf := filepath.Join(configDir, "smb.conf")
	if _, serr := os.Stat(conf); serr != nil {
		t.Fatalf("the configuration was not rendered at boot: %v", serr)
	}

	status, body := mutate(t, http.MethodPatch, base+"/api/v1/admin/settings/smb",
		cookie, csrf, map[string]any{"enabled": false})
	if status != http.StatusOK {
		t.Fatalf("turning sharing off answered %d: %v", status, body)
	}
	if restart, ok := body["restart_required"].(bool); ok && restart {
		t.Error("the save asked for a restart, which is what this removed")
	}
	if _, serr := os.Stat(conf); !os.IsNotExist(serr) {
		t.Errorf("the configuration survived the switch being turned off: %v", serr)
	}

	// And back on, because a toggle that only goes one way is a switch an
	// operator cannot undo without the restart this removed.
	status, body = mutate(t, http.MethodPatch, base+"/api/v1/admin/settings/smb",
		cookie, csrf, map[string]any{"enabled": true})
	if status != http.StatusOK {
		t.Fatalf("turning sharing on answered %d: %v", status, body)
	}
	if _, serr := os.Stat(conf); serr != nil {
		t.Errorf("the configuration did not come back when sharing was turned on: %v", serr)
	}
}

// A server that boots with sharing off still writes credentials once it is on.
//
// This is the defect an operator meets as "SMB accepts nobody". The credential
// file's path was decided when the process started and the switch was part of
// that decision, so a deployment that booted with sharing off held an empty
// path forever: every password a person set was stored, the screen reported it
// as set, and the file the daemon authenticates against was never written.
// Only a restart repaired it, and turning sharing on is exactly the moment
// nobody expects to need one.
func TestEnablingSharingLaterStillWritesCredentials(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	configDir := filepath.Join(t.TempDir(), "smb")

	// Configured but off, which is what makes the path empty at startup.
	first, err := lifecycle.Open(ctx, lifecycle.Options{DataDir: dir})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	if merr := first.State.MergeSettings(ctx, "smb", map[string]any{
		"enabled": false, "config_dir": configDir, "workgroup": "WORKGROUP",
	}); merr != nil {
		t.Fatalf("saving the settings: %v", merr)
	}
	if _, cerr := first.Auth.CreateAdmin(ctx, "root", "Root", pwOf(loginPassword)); cerr != nil {
		t.Fatalf("creating the administrator: %v", cerr)
	}
	if cerr := first.Close(); cerr != nil {
		t.Fatalf("closing: %v", cerr)
	}

	e, err := lifecycle.Open(ctx, lifecycle.Options{DataDir: dir})
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}
	t.Cleanup(func() {
		if cerr := e.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	})
	base := serve(t, e)

	admin := postJSON(t, base+"/api/v1/auth/login",
		map[string]string{"login": "root", "password": loginPassword})
	cookie, csrf := admin.sessionCookie(), admin.field("csrf")
	if cookie == nil {
		t.Fatalf("the administrator did not sign in: %d %v", admin.status, admin.body)
	}

	if status, body := mutate(t, http.MethodPatch, base+"/api/v1/admin/settings/smb",
		cookie, csrf, map[string]any{"enabled": true}); status != http.StatusOK {
		t.Fatalf("turning sharing on answered %d: %v", status, body)
	}

	if status, body := mutate(t, http.MethodPost, base+"/api/v1/account/smb/password",
		cookie, csrf, map[string]any{
			"current": loginPassword, "new": "a-long-enough-smb-password",
		}); status != http.StatusOK {
		t.Fatalf("setting the SMB password answered %d: %v", status, body)
	}

	// The file the daemon reads. Absent, every account it names is one that
	// cannot authenticate, which is what the sidecar reports and what a person
	// meets as a password the server accepted and the mount refuses.
	body, rerr := os.ReadFile(filepath.Join(configDir, "smbpasswd"))
	if rerr != nil {
		t.Fatalf("the credential file was not written after sharing was enabled: %v", rerr)
	}
	if !strings.Contains(string(body), "root:") {
		t.Errorf("the credential file does not carry the account:\n%s", body)
	}
}

// readCredentialFile returns the rendered credential file's contents.
func readCredentialFile(t *testing.T, configDir string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(configDir, "smbpasswd"))
	if err != nil {
		t.Fatalf("reading the credential file: %v", err)
	}
	return string(body)
}

// A credential change reaches the rendered file, not only the database.
//
// This is the defect the wiring closed. The routes answered 200 while nothing
// was passed to auth, so the file was never written: the daemon authenticates
// against the last file that was published, which left a withdrawn credential
// serving until something else happened to publish one.
func TestSettingTheProtocolPasswordReachesTheRenderedFile(t *testing.T) {
	e, configDir := bootWithSidecar(t)
	base := serve(t, e)
	cookie, csrf := signedIn(t, base)

	if status, body := mutate(t, http.MethodPost, base+"/api/v1/account/smb/password",
		cookie, csrf, map[string]any{
			"current": loginPassword, "new": "a-long-enough-protocol-password",
		}); status != http.StatusOK {
		t.Fatalf("setting the protocol password answered %d: %v", status, body)
	}

	got := readCredentialFile(t, configDir)
	if !strings.Contains(got, loginName+":") {
		t.Fatalf("the account is absent from the credential file:\n%s", got)
	}
	// The record carries the disabled marker rather than a second, weaker hash.
	if !strings.Contains(got, ":XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX:") {
		t.Errorf("the record does not carry the disabled LANMAN field:\n%s", got)
	}
	// And the plaintext never reaches the file.
	if strings.Contains(got, "a-long-enough-protocol-password") {
		t.Errorf("the credential file carries the plaintext:\n%s", got)
	}
}

// Withdrawing from the protocol removes the account from the rendered file.
//
// The absence is the revocation. A record left behind is one the import still
// reads, so the daemon would go on accepting a credential the account has
// given up.
func TestOptingOutRemovesTheAccountFromTheRenderedFile(t *testing.T) {
	e, configDir := bootWithSidecar(t)
	base := serve(t, e)
	cookie, csrf := signedIn(t, base)

	if status, body := mutate(t, http.MethodPost, base+"/api/v1/account/smb/password",
		cookie, csrf, map[string]any{
			"current": loginPassword, "new": "a-long-enough-protocol-password",
		}); status != http.StatusOK {
		t.Fatalf("setting the protocol password answered %d: %v", status, body)
	}
	if before := readCredentialFile(t, configDir); !strings.Contains(before, loginName+":") {
		t.Fatalf("the account never reached the credential file:\n%s", before)
	}

	if status, body := mutate(t, http.MethodPost, base+"/api/v1/account/smb",
		cookie, csrf, map[string]any{
			"current": loginPassword, "opt_out": true,
		}); status != http.StatusOK {
		t.Fatalf("opting out answered %d: %v", status, body)
	}

	if after := readCredentialFile(t, configDir); strings.Contains(after, loginName+":") {
		t.Errorf("the withdrawn account is still in the credential file:\n%s", after)
	}
}

// Clearing the separate credential removes the record too.
func TestClearingTheProtocolPasswordReachesTheRenderedFile(t *testing.T) {
	e, configDir := bootWithSidecar(t)
	base := serve(t, e)
	cookie, csrf := signedIn(t, base)

	if status, body := mutate(t, http.MethodPost, base+"/api/v1/account/smb/password",
		cookie, csrf, map[string]any{
			"current": loginPassword, "new": "a-long-enough-protocol-password",
		}); status != http.StatusOK {
		t.Fatalf("setting answered %d: %v", status, body)
	}
	first := readCredentialFile(t, configDir)

	if status, body := mutate(t, http.MethodDelete, base+"/api/v1/account/smb/password",
		cookie, csrf, map[string]any{"current": loginPassword}); status != http.StatusOK {
		t.Fatalf("clearing answered %d: %v", status, body)
	}

	second := readCredentialFile(t, configDir)
	if first == second {
		t.Errorf("clearing the credential left the file unchanged:\n%s", second)
	}
	if strings.Contains(second, loginName+":") {
		t.Errorf("the cleared account is still in the credential file:\n%s", second)
	}
}

// The two rendered files agree on every account and every uid.
//
// They are matched by uid rather than by name, so a disagreement makes the
// import produce nothing for that account and the only symptom is a client
// that cannot connect.
func TestTheRenderedFilesAgreeOnEveryAccount(t *testing.T) {
	e, configDir := bootWithSidecar(t)
	base := serve(t, e)
	cookie, csrf := signedIn(t, base)

	if status, body := mutate(t, http.MethodPost, base+"/api/v1/account/smb/password",
		cookie, csrf, map[string]any{
			"current": loginPassword, "new": "a-long-enough-protocol-password",
		}); status != http.StatusOK {
		t.Fatalf("setting answered %d: %v", status, body)
	}

	// The account file is written by a publish rather than by a credential
	// change, so one is asked for here.
	if err := e.Auth.PublishPasswdEntries(context.Background(),
		filepath.Join(configDir, "passwd"), 1000); err != nil {
		t.Fatalf("publishing the account file: %v", err)
	}

	creds, err := e.Auth.SMBCredentials(context.Background())
	if err != nil {
		t.Fatalf("SMBCredentials: %v", err)
	}
	if len(creds) == 0 {
		t.Fatal("no account is publishable, so this check is watching nothing")
	}

	passdb := readCredentialFile(t, configDir)
	passwdBody, err := os.ReadFile(filepath.Join(configDir, "passwd"))
	if err != nil {
		t.Fatalf("reading the account file: %v", err)
	}
	for _, c := range creds {
		uid := strconv.FormatUint(uint64(c.UID), 10)
		if !strings.Contains(passdb, c.Name+":"+uid+":") {
			t.Errorf("the credential file does not carry %s at uid %s:\n%s", c.Name, uid, passdb)
		}
		if !strings.Contains(string(passwdBody), c.Name+":x:"+uid+":") {
			t.Errorf("the account file does not carry %s at uid %s:\n%s", c.Name, uid, passwdBody)
		}
	}
}

// A deployment with no sidecar says so rather than reporting an apply.
func TestTheApplyRouteRefusesWithoutASidecar(t *testing.T) {
	base, adminCookie, adminCSRF, _, _ := adminEngine(t)

	status, body := mutate(t, http.MethodPost, base+"/api/v1/admin/smb/apply",
		adminCookie, adminCSRF, nil)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("an unconfigured deployment answered %d: %v", status, body)
	}
	// The catalogue key rather than the rendered sentence: the client owns the
	// wording, so the key is what a screen branches on.
	if key := reasonKey(body); key != "smb.not_configured" {
		t.Errorf("the refusal carries reason key %q, want smb.not_configured", key)
	}
}

// reasonKey reads the catalogue key out of a refusal envelope.
func reasonKey(body map[string]any) string {
	err, ok := body["error"].(map[string]any)
	if !ok {
		return ""
	}
	detail, ok := err["detail"].(map[string]any)
	if !ok {
		return ""
	}
	return stringField(detail, "reason_key")
}

// The apply route is an administrator's.
func TestTheApplyRouteNeedsAnAdministrator(t *testing.T) {
	base, _, _, plainCookie, plainCSRF := adminEngine(t)

	status, _ := mutate(t, http.MethodPost, base+"/api/v1/admin/smb/apply",
		plainCookie, plainCSRF, nil)
	if status != http.StatusForbidden {
		t.Errorf("an ordinary account reached the apply route: %d", status)
	}
}

// With a directory configured and no agent socket, an apply renders the files
// and reports the daemon as unchanged.
//
// That is a deployment without the SMB container: the files are written and
// nothing applies them, so there is no socket to ask and nothing to report
// failing.
func TestAnApplyRendersTheConfigurationWithoutAnAgent(t *testing.T) {
	e, configDir := bootWithSidecar(t)
	base := serve(t, e)

	if _, err := e.Auth.CreateAdmin(context.Background(), "root", "Root",
		secret.New([]byte(loginPassword))); err != nil {
		t.Fatalf("creating the administrator: %v", err)
	}
	admin := postJSON(t, base+"/api/v1/auth/login",
		map[string]string{"login": "root", "password": loginPassword})
	if admin.sessionCookie() == nil {
		t.Fatalf("the administrator did not sign in: %d %v", admin.status, admin.body)
	}

	status, body := mutate(t, http.MethodPost, base+"/api/v1/admin/smb/apply",
		admin.sessionCookie(), admin.field("csrf"), nil)
	if status != http.StatusOK {
		t.Fatalf("the apply answered %d: %v", status, body)
	}
	if applied, isBool := body["ok"].(bool); !isBool || !applied {
		t.Errorf("the apply reported a failure: %v", body)
	}
	if action := stringField(body, "action"); action != "unchanged" {
		t.Errorf("the daemon action is %q, want unchanged with no socket", action)
	}

	// The configuration file is on disk, which is what the agent would read.
	if _, serr := os.Stat(filepath.Join(configDir, "smb.conf")); serr != nil {
		t.Errorf("the configuration was not rendered: %v", serr)
	}
}

// A withdrawal reaches the account file as well as the credential file.
//
// The credential file is written by auth on every change; the account file is
// written only by a publish. So this is what proves the publisher is attached
// as the sink: without it one file drops the account and the other keeps it,
// leaving the pair disagreeing about who exists.
func TestAWithdrawalReachesBothRenderedFiles(t *testing.T) {
	e, configDir := bootWithSidecar(t)
	base := serve(t, e)
	cookie, csrf := signedIn(t, base)

	passwdPath := filepath.Join(configDir, "passwd")
	before, err := os.ReadFile(passwdPath)
	if err != nil {
		t.Fatalf("no account file was written at boot: %v", err)
	}
	if !strings.Contains(string(before), loginName+":x:") {
		t.Fatalf("the account never reached the account file:\n%s", before)
	}

	if status, body := mutate(t, http.MethodPost, base+"/api/v1/account/smb",
		cookie, csrf, map[string]any{
			"current": loginPassword, "opt_out": true,
		}); status != http.StatusOK {
		t.Fatalf("opting out answered %d: %v", status, body)
	}

	after, err := os.ReadFile(passwdPath)
	if err != nil {
		t.Fatalf("reading the account file: %v", err)
	}
	if strings.Contains(string(after), loginName+":x:") {
		t.Errorf("the withdrawn account is still in the account file:\n%s", after)
	}
	// The two still agree, which is what the uid pairing rests on: an account
	// in one file and not the other cannot authenticate either way.
	if strings.Contains(readCredentialFile(t, configDir), loginName+":") {
		t.Error("the withdrawn account is still in the credential file")
	}
}
