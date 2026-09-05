package auth_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/service/auth"
)

func TestAnAppPasswordVerifiesAndCarriesItsScope(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	id := f.account(t, "alice")

	scope := auth.Scope{Perms: 0b101, Shares: []string{"photos", "docs"}}
	token, err := f.svc.CreateAppPassword(ctx, id, "phone", scope, 0)
	if err != nil {
		t.Fatalf("CreateAppPassword: %v", err)
	}
	principal, got, err := f.svc.VerifyAppPassword(ctx, token)
	if err != nil {
		t.Fatalf("VerifyAppPassword: %v", err)
	}
	if principal.UserID != id || got.Perms != scope.Perms {
		t.Fatalf("the credential resolved to %+v with scope %+v", principal, got)
	}
	if len(got.Shares) != 2 || got.Shares[0] != "photos" || got.Shares[1] != "docs" {
		t.Fatalf("the scope round-tripped as %v", got.Shares)
	}
}

// A code read off a screen and typed into a phone must not fail on a
// character the reader guessed wrong.
func TestConfusableSpellingsOfATokenAllVerify(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	id := f.account(t, "alice")

	token, err := f.svc.CreateAppPassword(ctx, id, "phone", auth.Scope{}, 0)
	if err != nil {
		t.Fatalf("CreateAppPassword: %v", err)
	}
	for _, spelling := range []string{
		strings.ToLower(token),
		strings.ReplaceAll(token, "1", "I"),
		strings.ReplaceAll(token, "1", "l"),
		strings.ReplaceAll(token, "0", "O"),
		strings.ReplaceAll(strings.ToLower(token), "0", "o"),
	} {
		if _, _, verr := f.svc.VerifyAppPassword(ctx, spelling); verr != nil {
			t.Fatalf("the spelling %q did not verify: %v", spelling, verr)
		}
	}
}

// The token cache would otherwise serve a revoked credential for up to a
// minute, so the generation counter is what makes the revocation immediate.
func TestRevocationBeatsTheTokenCache(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	id := f.account(t, "alice")

	token, err := f.svc.CreateAppPassword(ctx, id, "phone", auth.Scope{}, 0)
	if err != nil {
		t.Fatalf("CreateAppPassword: %v", err)
	}
	if _, _, verr := f.svc.VerifyAppPassword(ctx, token); verr != nil {
		t.Fatalf("VerifyAppPassword: %v", verr)
	}
	rows, err := f.svc.AppPasswords(ctx, id)
	if err != nil || len(rows) != 1 {
		t.Fatalf("AppPasswords returned %d rows, %v", len(rows), err)
	}
	if err := f.svc.RevokeAppPassword(ctx, id, rows[0].ID); err != nil {
		t.Fatalf("RevokeAppPassword: %v", err)
	}
	if _, _, verr := f.svc.VerifyAppPassword(ctx, token); !errors.Is(verr, auth.ErrCredentials) {
		t.Fatalf("a revoked credential returned %v", verr)
	}
}

func TestAWipedCredentialStaysRefused(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	id := f.account(t, "alice")
	token, err := f.svc.CreateAppPassword(ctx, id, "phone", auth.Scope{}, 0)
	if err != nil {
		t.Fatalf("CreateAppPassword: %v", err)
	}
	rows, err := f.svc.AppPasswords(ctx, id)
	if err != nil || len(rows) != 1 {
		t.Fatalf("AppPasswords returned %d rows, %v", len(rows), err)
	}
	if err := f.svc.RequestWipe(ctx, id, rows[0].ID); err != nil {
		t.Fatalf("RequestWipe: %v", err)
	}
	for i := 0; i < 2; i++ {
		if _, _, verr := f.svc.VerifyAppPassword(ctx, token); !errors.Is(verr, auth.ErrCredentials) {
			t.Fatalf("a wiped credential returned %v", verr)
		}
	}
}

func TestAnExpiredOrDisownedCredentialRefuses(t *testing.T) {
	ctx := context.Background()
	clk := &steppingClock{at: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	f := newFixtureWithClock(t, clk)
	f.admin(t, "admin")
	id := f.account(t, "alice")

	expiring, err := f.svc.CreateAppPassword(ctx, id, "temp", auth.Scope{}, time.Hour)
	if err != nil {
		t.Fatalf("CreateAppPassword: %v", err)
	}
	clk.advance(2 * time.Hour)
	if _, _, verr := f.svc.VerifyAppPassword(ctx, expiring); !errors.Is(verr, auth.ErrCredentials) {
		t.Fatalf("an expired credential returned %v", verr)
	}

	lasting, err := f.svc.CreateAppPassword(ctx, id, "phone", auth.Scope{}, 0)
	if err != nil {
		t.Fatalf("CreateAppPassword: %v", err)
	}
	if err := f.svc.DisableAccount(ctx, id); err != nil {
		t.Fatalf("DisableAccount: %v", err)
	}
	if _, _, verr := f.svc.VerifyAppPassword(ctx, lasting); !errors.Is(verr, auth.ErrCredentials) {
		t.Fatalf("a disabled owner's credential returned %v", verr)
	}
}

// The device-login policy lives here rather than in whatever wiring calls it,
// and the id it returns is what a failed delivery revokes without anybody
// retaining the plaintext.
func TestASyncCredentialCarriesFullScopeAndNoExpiry(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	id := f.account(t, "alice")

	token, credID, err := f.svc.CreateSyncCredential(ctx, id, "desktop")
	if err != nil {
		t.Fatalf("CreateSyncCredential: %v", err)
	}
	_, scope, err := f.svc.VerifyAppPassword(ctx, token)
	if err != nil {
		t.Fatalf("VerifyAppPassword: %v", err)
	}
	if scope.Perms != auth.SyncScopePerms || len(scope.Shares) != 0 {
		t.Fatalf("the sync scope is %+v", scope)
	}
	rows, err := f.svc.AppPasswords(ctx, id)
	if err != nil || len(rows) != 1 {
		t.Fatalf("AppPasswords returned %d rows, %v", len(rows), err)
	}
	if rows[0].ExpiresNs != nil {
		t.Fatalf("the sync credential expires at %d", *rows[0].ExpiresNs)
	}

	// A delivery that failed revokes exactly this credential, by id.
	if err := f.svc.RevokeAppPassword(ctx, id, credID); err != nil {
		t.Fatalf("RevokeAppPassword: %v", err)
	}
	if _, _, verr := f.svc.VerifyAppPassword(ctx, token); !errors.Is(verr, auth.ErrCredentials) {
		t.Fatalf("the orphaned credential still verifies: %v", verr)
	}
}

func TestTheSecondFactorAcceptsItsWindowAndRefusesOutsideIt(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	id := f.account(t, "alice")

	secretB32, err := f.svc.GenerateTOTPSecret()
	if err != nil {
		t.Fatalf("GenerateTOTPSecret: %v", err)
	}
	if err := f.svc.EnrollTOTP(ctx, id, secretB32); err != nil {
		t.Fatalf("EnrollTOTP: %v", err)
	}

	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC).UnixNano()
	step := int64(30 * time.Second)
	for _, offset := range []int64{-1, 0, 1} {
		code := totpCode(t, secretB32, (now+offset*step)/step)
		ok, verr := f.svc.VerifyTOTP(ctx, id, code, now)
		if verr != nil {
			t.Fatalf("VerifyTOTP at offset %d: %v", offset, verr)
		}
		if !ok {
			t.Fatalf("the code at offset %d was refused", offset)
		}
	}
	for _, offset := range []int64{-2, 2} {
		code := totpCode(t, secretB32, (now+offset*step)/step)
		ok, verr := f.svc.VerifyTOTP(ctx, id, code, now)
		if verr != nil {
			t.Fatalf("VerifyTOTP at offset %d: %v", offset, verr)
		}
		if ok {
			t.Fatalf("the code at offset %d was accepted", offset)
		}
	}
}

// A code captured in transit must not be usable again inside its own window.
func TestASecondFactorCodeCannotBeReplayed(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	id := f.account(t, "alice")
	secretB32, err := f.svc.GenerateTOTPSecret()
	if err != nil {
		t.Fatalf("GenerateTOTPSecret: %v", err)
	}
	if err := f.svc.EnrollTOTP(ctx, id, secretB32); err != nil {
		t.Fatalf("EnrollTOTP: %v", err)
	}

	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC).UnixNano()
	code := totpCode(t, secretB32, now/int64(30*time.Second))
	if ok, verr := f.svc.VerifyTOTP(ctx, id, code, now); verr != nil || !ok {
		t.Fatalf("the first presentation returned %v, %v", ok, verr)
	}
	if ok, verr := f.svc.VerifyTOTP(ctx, id, code, now); verr != nil || ok {
		t.Fatalf("the replay returned %v, %v", ok, verr)
	}
}

// The factor the person just added must not be bypassable by the older
// protocol answering to the account password.
func TestEnrollingASecondFactorDropsTheStoredSMBCredential(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	id := f.account(t, "alice")

	state, err := f.svc.SMBStateOf(ctx, id)
	if err != nil {
		t.Fatalf("SMBStateOf: %v", err)
	}
	if state.Credential != auth.SMBCredentialAccount {
		t.Fatalf("a fresh account reports %+v, want a usable credential", state)
	}

	secretB32, err := f.svc.GenerateTOTPSecret()
	if err != nil {
		t.Fatalf("GenerateTOTPSecret: %v", err)
	}
	if err = f.svc.EnrollTOTP(ctx, id, secretB32); err != nil {
		t.Fatalf("EnrollTOTP: %v", err)
	}
	if state, err = f.svc.SMBStateOf(ctx, id); err != nil {
		t.Fatalf("SMBStateOf: %v", err)
	}
	if state.Credential != auth.SMBCredentialNone || state.Reason != auth.SMBUnavailableNotSet {
		t.Fatalf("after enrolment the account reports %+v", state)
	}
}

// A sign-in restores a credential the account is eligible for and has lost.
//
// The interface tells such an account that signing in again makes SMB work
// with the account password. Nothing did that, so the credential stayed
// missing however often they signed in and the screen kept saying the same
// thing. The password verified here is the only place the plaintext exists.
func TestSigningInRestoresAMissingSMBCredential(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	id := f.account(t, "alice")

	if _, err := f.svc.ClearSMBPassword(ctx, id); err != nil {
		t.Fatalf("ClearSMBPassword: %v", err)
	}
	gone, err := f.svc.SMBStateOf(ctx, id)
	if err != nil {
		t.Fatalf("SMBStateOf: %v", err)
	}
	if gone.Credential != auth.SMBCredentialNone {
		t.Fatalf("the credential survived being cleared: %+v", gone)
	}

	if _, lerr := f.svc.Login(ctx, auth.LoginRequest{
		Name: "alice", Password: pw(testPassword), IP: "192.0.2.1",
	}, 0); lerr != nil {
		t.Fatalf("Login: %v", lerr)
	}

	back, err := f.svc.SMBStateOf(ctx, id)
	if err != nil {
		t.Fatalf("SMBStateOf: %v", err)
	}
	if back.Credential != auth.SMBCredentialAccount {
		t.Fatalf("signing in did not restore the credential: %+v", back)
	}
}

// Signing in must not reinstate the credential the second factor closed.
//
// Enrolment drops it precisely so the account password stops working over a
// protocol whose authentication cannot be strengthened to match. A restore on
// the next sign-in would undo that silently.
func TestSigningInDoesNotRestoreTheCredentialASecondFactorClosed(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	id := f.account(t, "alice")

	secretB32, err := f.svc.GenerateTOTPSecret()
	if err != nil {
		t.Fatalf("GenerateTOTPSecret: %v", err)
	}
	if eerr := f.svc.EnrollTOTP(ctx, id, secretB32); eerr != nil {
		t.Fatalf("EnrollTOTP: %v", eerr)
	}

	// The password alone cannot complete this sign-in, and the attempt must
	// not leave a credential behind either.
	if _, lerr := f.svc.Login(ctx, auth.LoginRequest{
		Name: "alice", Password: pw(testPassword), IP: "192.0.2.2",
	}, 0); lerr == nil {
		t.Fatal("a password-only sign-in succeeded for an enrolled account")
	}

	state, err := f.svc.SMBStateOf(ctx, id)
	if err != nil {
		t.Fatalf("SMBStateOf: %v", err)
	}
	if state.Credential != auth.SMBCredentialNone {
		t.Fatalf("a sign-in reinstated the credential enrolment closed: %+v", state)
	}
}

func TestRecoveryCodesAreSingleUseAndCountDown(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	id := f.account(t, "alice")

	codes, err := f.svc.GenerateRecoveryCodes(ctx, id, 3)
	if err != nil || len(codes) != 3 {
		t.Fatalf("GenerateRecoveryCodes returned %d codes, %v", len(codes), err)
	}
	if n, cerr := f.svc.RecoveryCodesRemaining(ctx, id); cerr != nil || n != 3 {
		t.Fatalf("the count is %d, %v", n, cerr)
	}

	used, err := f.svc.UseRecoveryCode(ctx, id, strings.ToLower(codes[0]))
	if err != nil || !used {
		t.Fatalf("the first use returned %v, %v", used, err)
	}
	if used, err = f.svc.UseRecoveryCode(ctx, id, codes[0]); err != nil || used {
		t.Fatalf("the second use returned %v, %v", used, err)
	}
	if n, cerr := f.svc.RecoveryCodesRemaining(ctx, id); cerr != nil || n != 2 {
		t.Fatalf("the count is %d, %v", n, cerr)
	}

	// Generating replaces the set, so a code from the old list stops working.
	if _, err = f.svc.GenerateRecoveryCodes(ctx, id, 2); err != nil {
		t.Fatalf("GenerateRecoveryCodes: %v", err)
	}
	if used, err = f.svc.UseRecoveryCode(ctx, id, codes[1]); err != nil || used {
		t.Fatalf("a code from the replaced set returned %v, %v", used, err)
	}
}

func TestARecoveryCodeSetSizeIsBounded(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	id := f.account(t, "alice")

	for _, n := range []int{0, -1, auth.RecoveryCodesMax + 1} {
		if _, err := f.svc.GenerateRecoveryCodes(ctx, id, n); !errors.Is(err, auth.ErrRecoverySetSize) {
			t.Fatalf("a set of %d returned %v", n, err)
		}
	}
}

// A code that is not the alphabet is a refusal rather than an error: it is a
// wrong code, and the caller answers the same way it answers any other.
func TestAMalformedRecoveryCodeIsARefusal(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	id := f.account(t, "alice")
	if _, err := f.svc.GenerateRecoveryCodes(ctx, id, 1); err != nil {
		t.Fatalf("GenerateRecoveryCodes: %v", err)
	}
	used, err := f.svc.UseRecoveryCode(ctx, id, "not a code!")
	if err != nil || used {
		t.Fatalf("a malformed code returned %v, %v", used, err)
	}
}
