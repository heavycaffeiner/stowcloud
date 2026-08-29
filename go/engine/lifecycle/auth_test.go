//go:build linux

package lifecycle_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec // RFC 6238 fixes the algorithm for the reference code this test computes.
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/clock"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/secret"
	"github.com/heavycaffeiner/stowcloud/go/engine/lifecycle"
)

const (
	loginName     = "alice"
	loginPassword = "a-long-enough-password"
)

// bootForLogin serves an engine holding one account whose password is known,
// and hands back the engine so a test can enrol a second factor on it.
func bootForLogin(t *testing.T) (string, *lifecycle.Engine, int64) {
	t.Helper()
	ctx := context.Background()

	e, err := lifecycle.Open(ctx, lifecycle.Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	t.Cleanup(func() {
		if cerr := e.Close(); cerr != nil {
			t.Errorf("closing the engine: %v", cerr)
		}
	})

	id, err := e.Auth.CreateUser(ctx, loginName, "Alice", secret.New([]byte(loginPassword)))
	if err != nil {
		t.Fatalf("creating the account: %v", err)
	}
	return serve(t, e), e, id
}

// answer is everything a test needs from a sign-in, with nothing left open.
//
// The body is read and closed inside postJSON rather than handed back live: a
// helper returning an open response leaves every caller responsible for
// closing it, and one that forgets leaks a connection for the whole run.
type answer struct {
	status  int
	body    map[string]any
	cookies []*http.Cookie
}

// field reads a string out of the decoded body, empty when absent or not a
// string.
func (a answer) field(name string) string {
	s, ok := a.body[name].(string)
	if !ok {
		return ""
	}
	return s
}

// sessionCookie finds the session cookie this response set, if any.
func (a answer) sessionCookie() *http.Cookie {
	for _, c := range a.cookies {
		if c.Name == "__Host-sc_sid" {
			return c
		}
	}
	return nil
}

// postJSON sends a body with no credential.
func postJSON(t *testing.T, url string, body any) answer {
	t.Helper()

	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(string(encoded)))
	if err != nil {
		t.Fatalf("building: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := testClient().Do(req)
	if err != nil {
		t.Fatalf("requesting %s: %v", url, err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	}()

	out := answer{status: resp.StatusCode, cookies: resp.Cookies()}
	// A refusal may carry no body at all, so a decode failure is only
	// interesting when something was actually sent.
	if derr := json.NewDecoder(resp.Body).Decode(&out.body); derr != nil {
		out.body = map[string]any{}
	}
	return out
}

// A correct password without a second factor produces a session.
func TestSigningInWithAPassword(t *testing.T) {
	base, _, id := bootForLogin(t)

	resp := postJSON(t, base+"/api/v1/auth/login",
		map[string]string{"login": loginName, "password": loginPassword})
	if resp.status != http.StatusOK {
		t.Fatalf("signing in answered %d", resp.status)
	}

	body := resp.body
	if body["login"] != loginName {
		t.Errorf("the response names %v, not the account that signed in", body["login"])
	}
	// Decimal, because a JavaScript number loses exactness past 2^53 and an id
	// that round-trips wrong names a different account.
	if body["id"] != fmt.Sprint(id) {
		t.Errorf("the id came back as %v, want the decimal string %d", body["id"], id)
	}
	if csrf := resp.field("csrf"); csrf == "" {
		t.Error("no CSRF token, so this session cannot make a single mutation")
	}

	// The token itself must not be in the body. A cookie the browser attaches
	// on its own is not readable by a page's scripts; the same secret in JSON
	// is.
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"token", "session", "secret"} {
		if strings.Contains(string(raw), `"`+key+`"`) {
			t.Errorf("the response carries a %q field: %s", key, raw)
		}
	}
}

// The cookie's attributes are what stop it being stolen or sent cross-site.
// Each one is checked, because a missing flag weakens the session silently.
func TestTheSessionCookieIsProtected(t *testing.T) {
	base, _, _ := bootForLogin(t)

	resp := postJSON(t, base+"/api/v1/auth/login",
		map[string]string{"login": loginName, "password": loginPassword})

	cookie := resp.sessionCookie()
	if cookie == nil {
		t.Fatal("the sign-in set no session cookie")
	}
	if !cookie.HttpOnly {
		t.Error("the cookie is readable by scripts, so an injected one can take the session")
	}
	if !cookie.Secure {
		t.Error("the cookie is sent over plain HTTP")
	}
	if cookie.SameSite != http.SameSiteLaxMode {
		t.Errorf("SameSite is %v, so a cross-site request may carry it", cookie.SameSite)
	}
	if cookie.Path != "/" {
		t.Errorf("the path is %q; the __Host- prefix requires /", cookie.Path)
	}
	// The __Host- prefix forbids a Domain, and a browser rejects the whole
	// cookie if one is set. That reads as a sign-in that silently did nothing.
	if cookie.Domain != "" {
		t.Errorf("a Domain of %q makes a browser reject the __Host- cookie", cookie.Domain)
	}
	if cookie.Value == "" {
		t.Error("the cookie is empty, so it names no session")
	}
}

// The session the cookie names is real: it authenticates a later request.
func TestTheSessionCookieAuthenticates(t *testing.T) {
	base, _, _ := bootForLogin(t)

	resp := postJSON(t, base+"/api/v1/auth/login",
		map[string]string{"login": loginName, "password": loginPassword})
	cookie := resp.sessionCookie()
	if cookie == nil {
		t.Fatal("no cookie to present")
	}

	status, body := withCookie(t, http.MethodGet, base+"/api/v1/auth/session", cookie)
	if status != http.StatusOK {
		t.Fatalf("the cookie did not authenticate: %d %s", status, body)
	}

	var view map[string]any
	if err := json.Unmarshal(body, &view); err != nil {
		t.Fatal(err)
	}
	if view["login"] != loginName {
		t.Errorf("the session reports %v", view["login"])
	}
}

// withCookie performs a request carrying one cookie.
func withCookie(t *testing.T, method, url string, cookie *http.Cookie) (int, []byte) {
	t.Helper()

	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatalf("building: %v", err)
	}
	req.AddCookie(cookie)

	resp, err := testClient().Do(req)
	if err != nil {
		t.Fatalf("requesting: %v", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	}()

	body := make([]byte, 0, 1024)
	buf := make([]byte, 1024)
	for {
		n, rerr := resp.Body.Read(buf)
		body = append(body, buf[:n]...)
		if rerr != nil {
			break
		}
	}
	return resp.StatusCode, body
}

// A wrong password is refused, and the refusal says nothing about whether the
// account exists.
func TestAWrongPasswordIsRefused(t *testing.T) {
	base, _, _ := bootForLogin(t)

	wrong := postJSON(t, base+"/api/v1/auth/login",
		map[string]string{"login": loginName, "password": "not-the-password"})
	if wrong.status != http.StatusUnauthorized {
		t.Errorf("a wrong password answered %d", wrong.status)
	}
	if wrong.sessionCookie() != nil {
		t.Error("a refused sign-in set a session cookie")
	}
	wrongBody := wrong.body

	absent := postJSON(t, base+"/api/v1/auth/login",
		map[string]string{"login": "nobody", "password": "not-the-password"})
	if absent.status != wrong.status {
		t.Errorf("an absent account answers %d and a wrong password %d: the pair is an oracle for which names exist",
			absent.status, wrong.status)
	}

	// Byte for byte, not merely the same status. A distinguishing message
	// tells a guesser which half of the pair to keep working on.
	absentBody := absent.body
	if fmt.Sprint(absentBody) != fmt.Sprint(wrongBody) {
		t.Errorf("the two refusals differ:\n absent: %v\n wrong:  %v", absentBody, wrongBody)
	}
}

// An enrolled account is asked for a code rather than signed in.
func TestAnEnrolledAccountIsAskedForACode(t *testing.T) {
	base, e, id := bootForLogin(t)
	enrol(t, e, id)

	resp := postJSON(t, base+"/api/v1/auth/login",
		map[string]string{"login": loginName, "password": loginPassword})
	if resp.status != http.StatusOK {
		t.Fatalf("the password step answered %d", resp.status)
	}
	if resp.sessionCookie() != nil {
		t.Fatal("the password alone produced a session for an enrolled account")
	}

	body := resp.body
	if body["required"] != "totp" {
		t.Errorf("the response asks for %v", body["required"])
	}
	if challenge := resp.field("challenge"); challenge == "" {
		t.Fatal("no challenge, so the code screen has nothing to present")
	}

	// The challenge must not name the account in the clear. It travels through
	// a client that has proved a password and nothing more, and a readable id
	// turns a password guess into an account enumeration.
	if strings.Contains(fmt.Sprint(body), loginName) {
		t.Errorf("the challenge response names the account: %v", body)
	}
}

// enrol puts a second factor on the account and returns its secret.
func enrol(t *testing.T, e *lifecycle.Engine, id int64) string {
	t.Helper()
	ctx := context.Background()

	secretB32, err := e.Auth.GenerateTOTPSecret()
	if err != nil {
		t.Fatalf("generating a secret: %v", err)
	}
	if eerr := e.Auth.EnrollTOTP(ctx, id, secretB32); eerr != nil {
		t.Fatalf("enrolling: %v", eerr)
	}
	return secretB32
}

// The two steps together produce a session.
func TestTheCodeStepCompletesTheSignIn(t *testing.T) {
	base, e, id := bootForLogin(t)
	secretB32 := enrol(t, e, id)

	first := postJSON(t, base+"/api/v1/auth/login",
		map[string]string{"login": loginName, "password": loginPassword})
	challenge := first.field("challenge")
	if challenge == "" {
		t.Fatal("no challenge")
	}

	second := postJSON(t, base+"/api/v1/auth/login/totp", map[string]string{
		"challenge": challenge,
		"code":      referenceCode(t, secretB32, nowStep()),
	})
	if second.status != http.StatusOK {
		t.Fatalf("the code step answered %d", second.status)
	}
	if second.sessionCookie() == nil {
		t.Fatal("a completed sign-in set no session cookie")
	}
}

// A forged challenge cannot skip the password.
//
// This is the whole reason the challenge is signed: without a valid signature,
// anyone holding a code could name any account and sign in as it.
func TestAForgedChallengeIsRefused(t *testing.T) {
	base, e, id := bootForLogin(t)
	secretB32 := enrol(t, e, id)
	code := referenceCode(t, secretB32, nowStep())

	// A well-formed body for the right account, signed with nothing.
	forged := postJSON(t, base+"/api/v1/auth/login/totp", map[string]string{
		"challenge": forgeChallenge(id, nowStep()*30),
		"code":      code,
	})
	if forged.status == http.StatusOK {
		t.Fatal("an unsigned challenge completed a sign-in, so the password step can be skipped")
	}
	if forged.sessionCookie() != nil {
		t.Fatal("a forged challenge set a session cookie")
	}
}

// A challenge whose signature was altered is refused, which is the same
// property from the other side: the body is genuine and the MAC is not.
func TestATamperedChallengeIsRefused(t *testing.T) {
	base, e, id := bootForLogin(t)
	secretB32 := enrol(t, e, id)

	first := postJSON(t, base+"/api/v1/auth/login",
		map[string]string{"login": loginName, "password": loginPassword})
	challenge := first.field("challenge")
	if challenge == "" {
		t.Fatal("no challenge")
	}

	// Flip one character of the signature half.
	dot := strings.LastIndex(challenge, ".")
	if dot < 0 || dot == len(challenge)-1 {
		t.Fatalf("the challenge has no signature: %q", challenge)
	}
	altered := challenge[:dot+1] + flipFirst(challenge[dot+1:])

	resp := postJSON(t, base+"/api/v1/auth/login/totp", map[string]string{
		"challenge": altered,
		"code":      referenceCode(t, secretB32, nowStep()),
	})
	if resp.status == http.StatusOK {
		t.Fatal("a challenge with an altered signature was accepted")
	}
}

// flipFirst changes the first character to a different one from the same
// alphabet, so the value stays decodable and only its bits differ.
func flipFirst(s string) string {
	if s == "" {
		return s
	}
	replacement := byte('A')
	if s[0] == 'A' {
		replacement = 'B'
	}
	return string(replacement) + s[1:]
}

// forgeChallenge builds the challenge's plaintext shape with an empty
// signature, which is what an attacker who has seen one can produce.
func forgeChallenge(userID, nowUnix int64) string {
	body := fmt.Sprintf("%d:%d:AAAAAAAAAAA", userID, nowUnix)
	return base64Raw(body) + "." + base64Raw("")
}

// A wrong code does not complete a sign-in.
func TestAWrongCodeIsRefused(t *testing.T) {
	base, e, id := bootForLogin(t)
	enrol(t, e, id)

	first := postJSON(t, base+"/api/v1/auth/login",
		map[string]string{"login": loginName, "password": loginPassword})
	challenge := first.field("challenge")

	resp := postJSON(t, base+"/api/v1/auth/login/totp",
		map[string]string{"challenge": challenge, "code": "000000"})
	if resp.status == http.StatusOK {
		t.Fatal("a wrong code completed a sign-in")
	}
	if resp.sessionCookie() != nil {
		t.Fatal("a wrong code set a session cookie")
	}
}

// Logging out revokes the session server-side, not just in the browser.
//
// Clearing the cookie alone leaves the token live for anything that copied it,
// while the person believes they signed out.
func TestLoggingOutRevokesTheSession(t *testing.T) {
	base, _, _ := bootForLogin(t)

	resp := postJSON(t, base+"/api/v1/auth/login",
		map[string]string{"login": loginName, "password": loginPassword})
	cookie := resp.sessionCookie()
	if cookie == nil {
		t.Fatal("no cookie")
	}
	csrf := resp.field("csrf")

	status, body := logout(t, base, cookie, csrf)
	if status != http.StatusNoContent {
		t.Fatalf("logging out answered %d: %s", status, body)
	}

	// The same cookie, presented again. It has to fail, and it has to fail
	// against the database rather than because the browser dropped it: this
	// request carries it deliberately.
	after, afterBody := withCookie(t, http.MethodGet, base+"/api/v1/auth/session", cookie)
	if after == http.StatusOK {
		t.Fatalf("the revoked cookie still authenticates: %s", afterBody)
	}
}

// logout performs the mutation with the CSRF token the session requires.
func logout(t *testing.T, base string, cookie *http.Cookie, csrf string) (int, []byte) {
	t.Helper()

	req, err := http.NewRequest(http.MethodPost, base+"/api/v1/auth/logout", nil)
	if err != nil {
		t.Fatalf("building: %v", err)
	}
	req.AddCookie(cookie)
	req.Header.Set("Sc-Csrf", csrf)

	resp, err := testClient().Do(req)
	if err != nil {
		t.Fatalf("requesting: %v", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	}()

	body := make([]byte, 0, 512)
	buf := make([]byte, 512)
	for {
		n, rerr := resp.Body.Read(buf)
		body = append(body, buf[:n]...)
		if rerr != nil {
			break
		}
	}
	return resp.StatusCode, body
}

// A mutation without the CSRF token is refused, so a cross-site page cannot
// sign a person out or act as them.
func TestAMutationWithoutTheCSRFTokenIsRefused(t *testing.T) {
	base, _, _ := bootForLogin(t)

	resp := postJSON(t, base+"/api/v1/auth/login",
		map[string]string{"login": loginName, "password": loginPassword})
	cookie := resp.sessionCookie()
	if cookie == nil {
		t.Fatal("no cookie")
	}

	status, _ := logout(t, base, cookie, "")
	if status == http.StatusNoContent {
		t.Fatal("a mutation carrying only the cookie succeeded, so the CSRF check did not run")
	}

	// And the session survived the refusal: a rejected request must not have
	// had its effect anyway.
	after, _ := withCookie(t, http.MethodGet, base+"/api/v1/auth/session", cookie)
	if after != http.StatusOK {
		t.Error("the refused logout revoked the session anyway")
	}
}

// referenceCode is the RFC 6238 computation, written here rather than taken
// from the service, so the test drives the endpoint with a code derived
// independently of the code that checks it.
func referenceCode(t *testing.T, secretB32 string, step int64) string {
	t.Helper()

	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secretB32)
	if err != nil {
		t.Fatalf("decoding the secret: %v", err)
	}
	if step < 0 {
		t.Fatalf("a step of %d has no counter", step)
	}
	counterValue, nerr := num.Narrow[uint64](step)
	if nerr != nil {
		t.Fatalf("a step of %d has no counter: %v", step, nerr)
	}
	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], counterValue)

	mac := hmac.New(sha1.New, key)
	if _, werr := mac.Write(counter[:]); werr != nil {
		t.Fatalf("computing: %v", werr)
	}
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	value := uint32(sum[off]&0x7f)<<24 |
		uint32(sum[off+1])<<16 |
		uint32(sum[off+2])<<8 |
		uint32(sum[off+3])
	return fmt.Sprintf("%06d", value%1_000_000)
}

// base64Raw is the unpadded URL-safe encoding the challenge uses.
func base64Raw(s string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(s))
}

// A sign-in missing a field is refused as a credential failure, not as
// malformed input.
//
// A missing password and a wrong one are the same event to anyone probing for
// which accounts exist. Answering 400 for one and 401 for the other lets a
// caller separate "this field was empty" from "this account was not there",
// and the empty-field answer is reachable without guessing anything.
func TestAnEmptyCredentialFieldAnswersLikeAWrongOne(t *testing.T) {
	base, _, _ := bootForLogin(t)

	wrong := postJSON(t, base+"/api/v1/auth/login",
		map[string]string{"login": loginName, "password": "not-the-password"})
	wrongBody := wrong.body

	for _, missing := range []map[string]string{
		{"login": loginName, "password": ""},
		{"login": "", "password": loginPassword},
		{"login": "", "password": ""},
	} {
		resp := postJSON(t, base+"/api/v1/auth/login", missing)
		if resp.status != wrong.status {
			t.Errorf("%v answered %d, while a wrong password answers %d",
				missing, resp.status, wrong.status)
			continue
		}
		if got := fmt.Sprint(resp.body); got != fmt.Sprint(wrongBody) {
			t.Errorf("%v answered %s, while a wrong password answers %v",
				missing, got, wrongBody)
		}
	}
}

// A mistyped TOTP code does not consume a recovery code.
//
// Recovery codes are finite and a person cannot tell one was spent. Checking
// them before the TOTP would burn one on every fumbled six digits, and an
// account would run out without anyone doing anything wrong.
func TestAMistypedCodeDoesNotSpendARecoveryCode(t *testing.T) {
	base, e, id := bootForLogin(t)
	enrol(t, e, id)
	ctx := context.Background()

	if _, err := e.Auth.GenerateRecoveryCodes(ctx, id, 8); err != nil {
		t.Fatalf("generating recovery codes: %v", err)
	}
	before, err := e.Auth.RecoveryCodesRemaining(ctx, id)
	if err != nil {
		t.Fatal(err)
	}

	first := postJSON(t, base+"/api/v1/auth/login",
		map[string]string{"login": loginName, "password": loginPassword})
	challenge := first.field("challenge")

	// Wrong as a TOTP, and shaped like a recovery code so the recovery path
	// genuinely attempts it rather than rejecting it as unparseable.
	resp := postJSON(t, base+"/api/v1/auth/login/totp",
		map[string]string{"challenge": challenge, "code": "ABCDEFGH"})
	if resp.status == http.StatusOK {
		t.Fatal("a wrong code signed in")
	}

	after, err := e.Auth.RecoveryCodesRemaining(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Errorf("a mistyped code spent %d recovery codes", before-after)
	}
}

// A recovery code completes a sign-in and is spent exactly once.
func TestARecoveryCodeSignsInOnce(t *testing.T) {
	base, e, id := bootForLogin(t)
	enrol(t, e, id)
	ctx := context.Background()

	codes, err := e.Auth.GenerateRecoveryCodes(ctx, id, 4)
	if err != nil {
		t.Fatalf("generating: %v", err)
	}
	if len(codes) == 0 {
		t.Fatal("no recovery codes were issued")
	}

	signIn := func(code string) answer {
		first := postJSON(t, base+"/api/v1/auth/login",
			map[string]string{"login": loginName, "password": loginPassword})
		challenge := first.field("challenge")
		return postJSON(t, base+"/api/v1/auth/login/totp",
			map[string]string{"challenge": challenge, "code": code})
	}

	if resp := signIn(codes[0]); resp.status != http.StatusOK {
		t.Fatalf("a recovery code answered %d", resp.status)
	}
	// The same code again. A recovery code that survives its use is a
	// permanent bypass of the second factor.
	if resp := signIn(codes[0]); resp.status == http.StatusOK {
		t.Error("the same recovery code signed in twice")
	}
}

// A logout whose revoke fails reports the failure.
//
// Answering 204 when the session is still live is the worst of both: the
// browser drops the cookie, the person believes they signed out, and the
// token in anything that copied the cookie keeps working with nothing left on
// screen to say so.
//
// The failure is produced by closing the databases under a running server,
// which is what a disk fault or an exhausted handle table looks like to the
// auth service: measured, the revoke then returns "sql: database is closed".
func TestALogoutWhoseRevokeFailsIsReported(t *testing.T) {
	ctx := context.Background()

	e, err := lifecycle.Open(ctx, lifecycle.Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	id, err := e.Auth.CreateUser(ctx, loginName, "Alice", secret.New([]byte(loginPassword)))
	if err != nil {
		t.Fatalf("creating the account: %v", err)
	}
	base := serve(t, e)

	resp := postJSON(t, base+"/api/v1/auth/login",
		map[string]string{"login": loginName, "password": loginPassword})
	cookie := resp.sessionCookie()
	if cookie == nil {
		t.Fatal("no cookie")
	}
	csrf := resp.field("csrf")
	_ = id

	// The server keeps serving; only its storage is gone.
	if cerr := e.Close(); cerr != nil {
		t.Logf("closing the databases: %v", cerr)
	}

	status, body := logout(t, base, cookie, csrf)
	if status == http.StatusNoContent || status == http.StatusOK {
		t.Fatalf("a logout that could not revoke answered %d: the session is still live and the caller was told otherwise", status)
	}
	if status != http.StatusInternalServerError {
		t.Errorf("answered %d, want a server fault: %s", status, body)
	}
}

// The CSRF token a session reports actually authorizes that session's
// mutations.
//
// A token that is merely non-empty proves nothing: a client reads this field
// and sends it back, so a wrong value means every mutation fails with nothing
// on screen explaining why. Checked by using it, not by looking at it.
func TestTheReportedCSRFTokenAuthorizesAMutation(t *testing.T) {
	base, _, _ := bootForLogin(t)

	resp := postJSON(t, base+"/api/v1/auth/login",
		map[string]string{"login": loginName, "password": loginPassword})
	cookie := resp.sessionCookie()
	if cookie == nil {
		t.Fatal("no cookie")
	}

	// The token from GET /auth/session, not the one the sign-in returned, so
	// the two paths are proven to agree.
	status, body := withCookie(t, http.MethodGet, base+"/api/v1/auth/session", cookie)
	if status != http.StatusOK {
		t.Fatalf("reading the session answered %d", status)
	}
	var view map[string]any
	if err := json.Unmarshal(body, &view); err != nil {
		t.Fatal(err)
	}
	csrf := stringField(view, "csrf")
	if csrf == "" {
		t.Fatal("the session reports no CSRF token, so this client can make no mutation")
	}

	if out, outBody := logout(t, base, cookie, csrf); out != http.StatusNoContent {
		t.Errorf("the reported token did not authorize a mutation: %d %s", out, outBody)
	}
}

// An app password's session view reports no CSRF token.
//
// It has no ambient authority to protect, and a token derived from an absent
// cookie would be a value that validates for nobody: a client that trusted it
// would send it and be refused.
func TestAnAppPasswordGetsNoCSRFToken(t *testing.T) {
	base, token := bootWithUser(t)

	status, body := authed(t, http.MethodGet, base+"/api/v1/auth/session", token)
	if status != http.StatusOK {
		t.Fatalf("answered %d: %s", status, body)
	}

	var view map[string]any
	if err := json.Unmarshal(body, &view); err != nil {
		t.Fatal(err)
	}
	if csrf := stringField(view, "csrf"); csrf != "" {
		t.Errorf("an app password was handed a CSRF token %q, which validates for no session", csrf)
	}
}

// stringField reads a string out of a decoded body, empty when absent or of
// another type.
func stringField(body map[string]any, name string) string {
	s, ok := body[name].(string)
	if !ok {
		return ""
	}
	return s
}

// nowStep is the current TOTP step, taken from the same wall clock the server
// reads. The clock package is the tree's one reader of time.Now; a test
// driving a live server has to agree with whatever it is currently using.
func nowStep() int64 {
	return clock.System().Now().Unix() / 30
}
