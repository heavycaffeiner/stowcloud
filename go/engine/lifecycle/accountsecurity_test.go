//go:build linux

package lifecycle_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/secret"
	"github.com/heavycaffeiner/stowcloud/go/engine/lifecycle"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/auth"
)

// signedIn returns a session cookie and its CSRF token for the standard
// account.
func signedIn(t *testing.T, base string) (*http.Cookie, string) {
	t.Helper()

	resp := postJSON(t, base+"/api/v1/auth/login",
		map[string]string{"login": loginName, "password": loginPassword})
	cookie := resp.sessionCookie()
	if cookie == nil {
		t.Fatalf("signing in produced no cookie: %d %v", resp.status, resp.body)
	}
	return cookie, resp.field("csrf")
}

// mutate performs a mutation carrying the session and its CSRF token.
func mutate(t *testing.T, method, url string, cookie *http.Cookie, csrf string, body any) (int, map[string]any) {
	t.Helper()

	encoded := []byte("")
	if body != nil {
		var merr error
		encoded, merr = json.Marshal(body)
		if merr != nil {
			t.Fatalf("encoding: %v", merr)
		}
	}

	req, err := http.NewRequest(method, url, bytes.NewReader(encoded))
	if err != nil {
		t.Fatalf("building: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	req.Header.Set("Sc-Csrf", csrf)

	resp, err := testClient().Do(req)
	if err != nil {
		t.Fatalf("requesting %s: %v", url, err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	}()

	out := map[string]any{}
	if derr := json.NewDecoder(resp.Body).Decode(&out); derr != nil {
		out = map[string]any{}
	}
	return resp.StatusCode, out
}

// A password change needs the current password, not just a live session.
//
// A session is what somebody who walked past an unlocked screen already has.
// Without the reconfirmation they could set a new password and own the
// account outright.
func TestChangingAPasswordNeedsTheCurrentOne(t *testing.T) {
	base, _, _ := bootForLogin(t)
	cookie, csrf := signedIn(t, base)

	status, body := mutate(t, http.MethodPost, base+"/api/v1/account/password", cookie, csrf,
		map[string]string{"current": "not-the-password", "new": "a-brand-new-password"})
	if status == http.StatusNoContent || status == http.StatusOK {
		t.Fatalf("a wrong current password changed the password: %d %v", status, body)
	}

	// Omitted entirely, which is the shape a caller relying on the session
	// alone would send.
	status, body = mutate(t, http.MethodPost, base+"/api/v1/account/password", cookie, csrf,
		map[string]string{"new": "a-brand-new-password"})
	if status == http.StatusNoContent || status == http.StatusOK {
		t.Fatalf("a password change with no reconfirmation succeeded: %d %v", status, body)
	}

	// And the old password still works, so neither refusal changed anything.
	after := postJSON(t, base+"/api/v1/auth/login",
		map[string]string{"login": loginName, "password": loginPassword})
	if after.status != http.StatusOK {
		t.Error("the refused change altered the password anyway")
	}
}

// The correct current password changes it, and the new one is what signs in.
func TestChangingAPassword(t *testing.T) {
	base, _, _ := bootForLogin(t)
	cookie, csrf := signedIn(t, base)

	const replacement = "a-brand-new-long-password"
	status, body := mutate(t, http.MethodPost, base+"/api/v1/account/password", cookie, csrf,
		map[string]string{"current": loginPassword, "new": replacement})
	if status != http.StatusNoContent {
		t.Fatalf("the change answered %d: %v", status, body)
	}

	if old := postJSON(t, base+"/api/v1/auth/login",
		map[string]string{"login": loginName, "password": loginPassword}); old.status == http.StatusOK {
		t.Error("the old password still signs in")
	}
	if fresh := postJSON(t, base+"/api/v1/auth/login",
		map[string]string{"login": loginName, "password": replacement}); fresh.status != http.StatusOK {
		t.Errorf("the new password does not sign in: %d %v", fresh.status, fresh.body)
	}
}

// A password under the floor is refused, and the old one keeps working.
func TestAWeakPasswordIsRefused(t *testing.T) {
	base, _, _ := bootForLogin(t)
	cookie, csrf := signedIn(t, base)

	status, _ := mutate(t, http.MethodPost, base+"/api/v1/account/password", cookie, csrf,
		map[string]string{"current": loginPassword, "new": "short"})
	if status == http.StatusNoContent {
		t.Fatal("a password under the floor was accepted")
	}

	if after := postJSON(t, base+"/api/v1/auth/login",
		map[string]string{"login": loginName, "password": loginPassword}); after.status != http.StatusOK {
		t.Error("the refused change replaced the password anyway")
	}
}

// Minting an app password returns the token once, it is live on the file
// protocol, and it is refused on the native API.
func TestMintingAnAppPassword(t *testing.T) {
	base, _, _ := bootForLogin(t)
	cookie, csrf := signedIn(t, base)

	status, body := mutate(t, http.MethodPost, base+"/api/v1/account/app-passwords", cookie, csrf,
		map[string]string{"current": loginPassword, "name": "a-laptop"})
	if status != http.StatusCreated {
		t.Fatalf("minting answered %d: %v", status, body)
	}

	token := stringField(body, "token")
	if token == "" {
		t.Fatal("no token, so the credential that was just created cannot be used")
	}

	// It is a real credential: it authenticates a real request on its own.
	// Proven on the DAV mount, which is what an app password is for; the
	// native API now admits only the browser session, so a device credential
	// cannot demonstrate liveness there any more.
	davReq, derr := http.NewRequest("PROPFIND", base+"/dav/", nil)
	if derr != nil {
		t.Fatalf("building the dav request: %v", derr)
	}
	davReq.SetBasicAuth("ignored", token)
	davResp, derr2 := testClient().Do(davReq)
	if derr2 != nil {
		t.Fatalf("requesting: %v", derr2)
	}
	if cerr := davResp.Body.Close(); cerr != nil {
		t.Errorf("closing: %v", cerr)
	}
	if davResp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("the minted token does not authenticate on dav: %d", davResp.StatusCode)
	}

	// And it cannot reach the native API at all: the account and auth
	// families are session-only, and the file routes that used to admit a
	// device credential now refuse it the same way every other route does.
	code, _ := appPasswordAuthed(t, http.MethodGet, base+"/api/v1/files/list?path=/", token)
	if code != http.StatusNotFound {
		t.Errorf("an app password reached the native API: %d", code)
	}

	// And it cannot reach account management, which is the boundary the token
	// exists inside rather than an accident of routing.
	code, _ = appPasswordAuthed(t, http.MethodGet, base+"/api/v1/account/app-passwords", token)
	if code != http.StatusNotFound {
		t.Errorf("an app password reached the account listing: %d", code)
	}

	// The listing never shows the token again, because only its digest is
	// kept. Read with the session, which is what the screen holds.
	code, listed := withCookie(t, http.MethodGet, base+"/api/v1/account/app-passwords", cookie)
	if code != http.StatusOK {
		t.Fatalf("listing answered %d", code)
	}
	if containsToken(string(listed), token) {
		t.Errorf("the listing carries the token: %s", listed)
	}
}

// containsToken reports whether a body holds the secret verbatim.
func containsToken(body, token string) bool {
	return token != "" && strings.Contains(body, token)
}

// Minting needs the current password too. The credential outlives the session
// that created it, so a session alone must not be able to mint one.
func TestMintingAnAppPasswordNeedsTheCurrentPassword(t *testing.T) {
	base, _, _ := bootForLogin(t)
	cookie, csrf := signedIn(t, base)

	status, body := mutate(t, http.MethodPost, base+"/api/v1/account/app-passwords", cookie, csrf,
		map[string]string{"name": "a-laptop"})
	if status == http.StatusCreated {
		t.Fatalf("a session alone minted a credential: %v", body)
	}

	status, body = mutate(t, http.MethodPost, base+"/api/v1/account/app-passwords", cookie, csrf,
		map[string]string{"current": "not-the-password", "name": "a-laptop"})
	if status == http.StatusCreated {
		t.Fatalf("a wrong password minted a credential: %v", body)
	}
}

// A session can be signed out by the handle the listing published.
func TestRevokingOneSession(t *testing.T) {
	base, _, _ := bootForLogin(t)

	doomed, _ := signedIn(t, base)
	keeper, keeperCSRF := signedIn(t, base)

	// The handle for the session that is about to be revoked, read from the
	// listing as a client would.
	code, listed := withCookie(t, http.MethodGet, base+"/api/v1/account/sessions", keeper)
	if code != http.StatusOK {
		t.Fatalf("listing answered %d: %s", code, listed)
	}
	var rows []map[string]any
	if err := json.Unmarshal(listed, &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) < 2 {
		t.Fatalf("the account has %d sessions, want both", len(rows))
	}

	var handle string
	for _, row := range rows {
		if !boolField(row, "current") {
			handle = stringField(row, "handle")
			break
		}
	}
	if handle == "" {
		t.Fatal("no revocable session in the listing")
	}

	status, body := mutate(t, http.MethodDelete,
		base+"/api/v1/account/sessions/"+handle, keeper, keeperCSRF, nil)
	if status != http.StatusNoContent {
		t.Fatalf("revoking answered %d: %v", status, body)
	}

	// One died, the other lives. Revoking every session of the account would
	// pass a check that only looked at the first.
	if after, _ := withCookie(t, http.MethodGet, base+"/api/v1/auth/session", doomed); after == http.StatusOK {
		t.Error("the revoked session still authenticates")
	}
	if after, _ := withCookie(t, http.MethodGet, base+"/api/v1/auth/session", keeper); after != http.StatusOK {
		t.Error("revoking one session killed the other")
	}
}

// A handle belonging to another account revokes nothing.
func TestRevokingAnotherAccountsSessionIsRefused(t *testing.T) {
	ctx := context.Background()

	e, err := lifecycle.Open(ctx, lifecycle.Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, cerr := e.Auth.CreateUser(ctx, loginName, "Alice", pwOf(loginPassword)); cerr != nil {
		t.Fatal(cerr)
	}
	if _, cerr := e.Auth.CreateUser(ctx, "mallory", "Mallory", pwOf(loginPassword)); cerr != nil {
		t.Fatal(cerr)
	}
	base := serve(t, e)

	victim, _ := signedIn(t, base)
	code, listed := withCookie(t, http.MethodGet, base+"/api/v1/account/sessions", victim)
	if code != http.StatusOK {
		t.Fatalf("listing answered %d", code)
	}
	var rows []map[string]any
	if uerr := json.Unmarshal(listed, &rows); uerr != nil {
		t.Fatal(uerr)
	}
	if len(rows) == 0 {
		t.Fatal("no sessions listed")
	}
	handle := stringField(rows[0], "handle")

	// Mallory signs in and aims the victim's handle at the revoke route.
	attacker := postJSON(t, base+"/api/v1/auth/login",
		map[string]string{"login": "mallory", "password": loginPassword})
	attackerCookie := attacker.sessionCookie()
	if attackerCookie == nil {
		t.Fatal("the second account did not sign in")
	}

	status, _ := mutate(t, http.MethodDelete,
		base+"/api/v1/account/sessions/"+handle, attackerCookie, attacker.field("csrf"), nil)
	if status == http.StatusNoContent {
		t.Fatal("one account revoked another's session")
	}

	// The victim's session is untouched.
	if after, _ := withCookie(t, http.MethodGet, base+"/api/v1/auth/session", victim); after != http.StatusOK {
		t.Error("the victim's session was revoked anyway")
	}
}

// Enrolling a second factor requires a working code, and issues recovery
// codes that actually sign in.
func TestEnrollingASecondFactor(t *testing.T) {
	base, _, _ := bootForLogin(t)
	cookie, csrf := signedIn(t, base)

	status, setup := mutate(t, http.MethodPost, base+"/api/v1/account/totp/setup", cookie, csrf,
		map[string]string{"current": loginPassword})
	if status != http.StatusOK {
		t.Fatalf("setup answered %d: %v", status, setup)
	}
	secretB32 := stringField(setup, "secret")
	if secretB32 == "" {
		t.Fatal("setup returned no secret")
	}
	if uri := stringField(setup, "uri"); uri == "" {
		t.Error("setup returned no otpauth URI, so nothing can scan it")
	}

	// Setup alone must not have turned the factor on: a person who scanned
	// into an app that then failed would be locked out.
	if before := postJSON(t, base+"/api/v1/auth/login",
		map[string]string{"login": loginName, "password": loginPassword}); before.field("required") != "" {
		t.Error("setup enrolled the factor before any code verified")
	}

	status, enrolled := mutate(t, http.MethodPost, base+"/api/v1/account/totp/enroll", cookie, csrf,
		map[string]string{
			"current": loginPassword,
			"secret":  secretB32,
			"code":    referenceCode(t, secretB32, nowStep()),
		})
	if status != http.StatusOK {
		t.Fatalf("enrolling answered %d: %v", status, enrolled)
	}

	// Now the password step asks for a code.
	after := postJSON(t, base+"/api/v1/auth/login",
		map[string]string{"login": loginName, "password": loginPassword})
	if after.field("required") != "totp" {
		t.Errorf("after enrolling, the password step answers %v", after.body)
	}
}

// A code that does not verify leaves the factor off.
//
// The enrolment is written before the code is checked, because verification
// reads what is stored. If the undo were missing, a person whose authenticator
// was misconfigured would be locked out of their own account by a screen that
// told them the code was wrong.
func TestAFailedEnrolmentLeavesTheFactorOff(t *testing.T) {
	base, _, _ := bootForLogin(t)
	cookie, csrf := signedIn(t, base)

	status, setup := mutate(t, http.MethodPost, base+"/api/v1/account/totp/setup", cookie, csrf,
		map[string]string{"current": loginPassword})
	if status != http.StatusOK {
		t.Fatalf("setup answered %d", status)
	}
	secretB32 := stringField(setup, "secret")

	status, _ = mutate(t, http.MethodPost, base+"/api/v1/account/totp/enroll", cookie, csrf,
		map[string]string{"current": loginPassword, "secret": secretB32, "code": "000000"})
	if status == http.StatusOK {
		t.Fatal("a wrong code completed the enrolment")
	}

	// The password alone still signs in, so the factor is off.
	after := postJSON(t, base+"/api/v1/auth/login",
		map[string]string{"login": loginName, "password": loginPassword})
	if after.field("required") != "" {
		t.Fatalf("a failed enrolment left the factor on: %v", after.body)
	}
	if after.sessionCookie() == nil {
		t.Error("the account can no longer sign in with its password")
	}
}

// Disabling needs the current password and then really turns it off.
func TestDisablingTheSecondFactor(t *testing.T) {
	base, e, id := bootForLogin(t)
	enrol(t, e, id)
	ctx := context.Background()

	// Signing in now needs a code, so the session comes from the service.
	sess, err := e.Auth.CreateSession(ctx, id, "127.0.0.1", "test", 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	// A request cookie, not a Set-Cookie: the attributes belong on the
	// response the server writes, and this is what a browser sends back.
	cookie := &http.Cookie{
		Name:     "__Host-sc_sid",
		Value:    hexOf(sess.Token),
		Path:     "/",
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}
	code, view := withCookie(t, http.MethodGet, base+"/api/v1/auth/session", cookie)
	if code != http.StatusOK {
		t.Fatalf("the session does not authenticate: %d %s", code, view)
	}
	var v map[string]any
	if uerr := json.Unmarshal(view, &v); uerr != nil {
		t.Fatal(uerr)
	}
	csrf := stringField(v, "csrf")

	// A wrong password must not disable it.
	status, _ := mutate(t, http.MethodPost, base+"/api/v1/account/totp/disable", cookie, csrf,
		map[string]string{"current": "not-the-password"})
	if status == http.StatusNoContent {
		t.Fatal("a wrong password disabled the second factor")
	}
	still := postJSON(t, base+"/api/v1/auth/login",
		map[string]string{"login": loginName, "password": loginPassword})
	if still.field("required") != "totp" {
		t.Fatal("the factor was disabled by a refused request")
	}

	status, body := mutate(t, http.MethodPost, base+"/api/v1/account/totp/disable", cookie, csrf,
		map[string]string{"current": loginPassword})
	if status != http.StatusNoContent {
		t.Fatalf("disabling answered %d: %v", status, body)
	}

	after := postJSON(t, base+"/api/v1/auth/login",
		map[string]string{"login": loginName, "password": loginPassword})
	if after.sessionCookie() == nil {
		t.Errorf("the password alone still does not sign in: %v", after.body)
	}

	smbState, err := e.Auth.SMBStateOf(context.Background(), id)
	if err != nil {
		t.Fatalf("SMBStateOf: %v", err)
	}
	if smbState.Credential != auth.SMBCredentialAccount {
		t.Errorf("SMB credential was not restored after disabling TOTP: %+v", smbState)
	}
}

// pwOf wraps a plaintext password for the service.
func pwOf(s string) secret.Secret { return secret.New([]byte(s)) }

// hexOf renders a session token as the cookie carries it.
func hexOf(t secret.Secret) string { return hex.EncodeToString(t.Reveal()) }

// The session listing marks the one making the request.
//
// A screen offering "sign out my other devices" reads this field. If nothing
// is ever marked, that screen signs the person out of the device they are
// holding, and the listing looks correct while doing it.
func TestTheSessionListingMarksTheCurrentOne(t *testing.T) {
	base, _, _ := bootForLogin(t)

	first, _ := signedIn(t, base)
	second, _ := signedIn(t, base)

	for _, probe := range []struct {
		name   string
		cookie *http.Cookie
	}{{"first", first}, {"second", second}} {
		code, listed := withCookie(t, http.MethodGet, base+"/api/v1/account/sessions", probe.cookie)
		if code != http.StatusOK {
			t.Fatalf("%s: listing answered %d", probe.name, code)
		}
		var rows []map[string]any
		if err := json.Unmarshal(listed, &rows); err != nil {
			t.Fatal(err)
		}
		if len(rows) != 2 {
			t.Fatalf("%s: %d sessions listed, want 2", probe.name, len(rows))
		}

		// Exactly one, and it has to move with the cookie: a listing that
		// marked every row, or always the same row, would pass a check that
		// only counted.
		var marked int
		for _, row := range rows {
			if boolField(row, "current") {
				marked++
			}
		}
		if marked != 1 {
			t.Errorf("%s: %d of %d rows are marked current", probe.name, marked, len(rows))
		}
	}

	// The two requests must not mark the same row, which is what proves the
	// mark follows the caller rather than the ordering.
	if markedHandle(t, base, first) == markedHandle(t, base, second) {
		t.Error("both sessions mark the same row, so the mark does not follow the caller")
	}
}

// markedHandle returns the handle of the row a listing marks current.
func markedHandle(t *testing.T, base string, cookie *http.Cookie) string {
	t.Helper()

	code, listed := withCookie(t, http.MethodGet, base+"/api/v1/account/sessions", cookie)
	if code != http.StatusOK {
		t.Fatalf("listing answered %d", code)
	}
	var rows []map[string]any
	if err := json.Unmarshal(listed, &rows); err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if boolField(row, "current") {
			return stringField(row, "handle")
		}
	}
	t.Fatal("no row is marked current")
	return ""
}

// An app password cannot read the session list.
//
// It is a filesystem credential handed to a device. The account family is the
// browser's own surface, and a token that could read the session list could
// also revoke the session that created it.
func TestAnAppPasswordCannotReachTheSessionList(t *testing.T) {
	base, token, _ := bootWithUser(t)

	code, listed := appPasswordAuthed(t, http.MethodGet, base+"/api/v1/account/sessions", token)
	if code != http.StatusNotFound {
		t.Fatalf("an app password reached the session list: %d %s", code, listed)
	}
}

// A handle that is only a prefix of a real one revokes nothing.
//
// The handle is a hex digest, so a prefix comparison would let a caller walk
// it a character at a time: send one character, then two, and whichever
// prefix stops answering not-found names a live session. An empty handle
// under that comparison matches the first row outright.
func TestAPartialSessionHandleRevokesNothing(t *testing.T) {
	base, _, _ := bootForLogin(t)

	victim, _ := signedIn(t, base)
	caller, csrf := signedIn(t, base)

	handle := markedHandle(t, base, victim)
	if len(handle) < 8 {
		t.Fatalf("the handle is %d characters, too short to truncate", len(handle))
	}

	for _, partial := range []string{
		handle[:1],
		handle[:len(handle)/2],
		handle[:len(handle)-1],
		handle + "0",
	} {
		status, _ := mutate(t, http.MethodDelete,
			base+"/api/v1/account/sessions/"+partial, caller, csrf, nil)
		if status == http.StatusNoContent {
			t.Errorf("the handle %q (%d of %d characters) revoked a session",
				partial, len(partial), len(handle))
		}
	}

	// And the session it was aimed at is still live.
	if code, _ := withCookie(t, http.MethodGet, base+"/api/v1/auth/session", victim); code != http.StatusOK {
		t.Error("a partial handle revoked the session anyway")
	}
}

// A negative expiry is refused rather than quietly minting a credential that
// expired before it was returned.
//
// time.Duration multiplication of a negative day count produces a moment in
// the past, and the store would accept it: the caller gets a token in a 201
// response that authenticates nothing.
func TestANegativeExpiryIsRefused(t *testing.T) {
	base, _, _ := bootForLogin(t)
	cookie, csrf := signedIn(t, base)

	status, body := mutate(t, http.MethodPost, base+"/api/v1/account/app-passwords", cookie, csrf,
		map[string]any{"current": loginPassword, "name": "a-laptop", "expires_in_days": -30})
	if status == http.StatusCreated {
		t.Fatalf("a negative expiry minted a credential: %v", body)
	}
	if status != http.StatusUnprocessableEntity {
		t.Errorf("answered %d, want the constraint refusal: %v", status, body)
	}
}

// A credential minted with an expiry still authenticates before it lapses.
func TestAnExpiringCredentialWorksUntilItLapses(t *testing.T) {
	base, _, _ := bootForLogin(t)
	cookie, csrf := signedIn(t, base)

	status, body := mutate(t, http.MethodPost, base+"/api/v1/account/app-passwords", cookie, csrf,
		map[string]any{"current": loginPassword, "name": "a-phone", "expires_in_days": 30})
	if status != http.StatusCreated {
		t.Fatalf("minting answered %d: %v", status, body)
	}

	// Proven on the DAV mount: the native API admits only the browser
	// session, so a device credential's liveness cannot be shown there.
	token := stringField(body, "token")
	davReq, derr := http.NewRequest("PROPFIND", base+"/dav/", nil)
	if derr != nil {
		t.Fatalf("building the dav request: %v", derr)
	}
	davReq.SetBasicAuth("ignored", token)
	davResp, derr2 := testClient().Do(davReq)
	if derr2 != nil {
		t.Fatalf("requesting: %v", derr2)
	}
	if cerr := davResp.Body.Close(); cerr != nil {
		t.Errorf("closing: %v", cerr)
	}
	if davResp.StatusCode != http.StatusMultiStatus {
		t.Errorf("a credential with 30 days left does not authenticate: %d", davResp.StatusCode)
	}
}

// An enrolment hands back recovery codes that actually work.
//
// An empty list, or codes the store never recorded, is the same failure from
// the person's side: they save what the screen showed and find out it is
// worthless on the day they have lost their authenticator.
func TestEnrolmentIssuesUsableRecoveryCodes(t *testing.T) {
	base, _, _ := bootForLogin(t)
	cookie, csrf := signedIn(t, base)

	_, setup := mutate(t, http.MethodPost, base+"/api/v1/account/totp/setup", cookie, csrf,
		map[string]string{"current": loginPassword})
	secretB32 := stringField(setup, "secret")

	status, enrolled := mutate(t, http.MethodPost, base+"/api/v1/account/totp/enroll", cookie, csrf,
		map[string]string{
			"current": loginPassword,
			"secret":  secretB32,
			"code":    referenceCode(t, secretB32, nowStep()),
		})
	if status != http.StatusOK {
		t.Fatalf("enrolling answered %d: %v", status, enrolled)
	}

	raw, ok := enrolled["recovery_codes"].([]any)
	if !ok || len(raw) == 0 {
		t.Fatalf("the enrolment issued no recovery codes: %v", enrolled)
	}

	// One of them signs in, which is the only thing that proves they were
	// recorded rather than merely generated and returned.
	first, isString := raw[0].(string)
	if !isString || first == "" {
		t.Fatalf("the first recovery code is not a usable string: %v", raw[0])
	}

	challenge := postJSON(t, base+"/api/v1/auth/login",
		map[string]string{"login": loginName, "password": loginPassword})
	done := postJSON(t, base+"/api/v1/auth/login/totp",
		map[string]string{"challenge": challenge.field("challenge"), "code": first})
	if done.status != http.StatusOK {
		t.Errorf("a recovery code from the enrolment does not sign in: %d %v", done.status, done.body)
	}
}

// boolField reads a bool out of a decoded body, false when absent or of
// another type.
func boolField(body map[string]any, name string) bool {
	b, ok := body[name].(bool)
	return ok && b
}
