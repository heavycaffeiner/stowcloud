//go:build linux

package lifecycle_test

import (
	"context"
	"encoding/json"
	"net/http"
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
