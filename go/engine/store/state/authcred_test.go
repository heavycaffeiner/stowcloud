package state_test

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/task"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/state"
)

func TestASessionRoundTripsAndRevokes(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	id := newAccount(t, d, "alice", 0)

	row := state.Session{
		IDHash: []byte("digest"), User: id,
		CreatedNs: 1, LastSeenNs: 1, AbsoluteNs: 100,
		IP: "192.0.2.1", UA: "client", AMR: 2,
	}
	if err := d.CreateSession(ctx, row); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	got, err := d.SessionByHash(ctx, []byte("digest"))
	if err != nil {
		t.Fatalf("SessionByHash: %v", err)
	}
	if got.User != id || got.IP != "192.0.2.1" || got.UA != "client" || got.AMR != 2 {
		t.Fatalf("the session read back as %+v", got)
	}

	if err = d.TouchSession(ctx, []byte("digest"), 55); err != nil {
		t.Fatalf("TouchSession: %v", err)
	}
	if got, err = d.SessionByHash(ctx, []byte("digest")); err != nil || got.LastSeenNs != 55 {
		t.Fatalf("the stamp read back as %d, %v", got.LastSeenNs, err)
	}

	if err = d.DeleteSession(ctx, []byte("digest")); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if _, err = d.SessionByHash(ctx, []byte("digest")); !errors.Is(err, state.ErrNoSuchSession) {
		t.Fatalf("a revoked session read back %v", err)
	}
}

// The owner is in the predicate with the digest, so the ownership check and
// the delete cannot disagree.
func TestRevokingBySessionHashIsOwnerScoped(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	alice := newAccount(t, d, "alice", 0)
	mallory := newAccount(t, d, "mallory", 0)

	if err := d.CreateSession(ctx, state.Session{
		IDHash: []byte("alice-session"), User: alice, AbsoluteNs: 100,
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := d.DeleteSessionOfUser(ctx, mallory, []byte("alice-session")); err != nil {
		t.Fatalf("DeleteSessionOfUser: %v", err)
	}
	if _, err := d.SessionByHash(ctx, []byte("alice-session")); err != nil {
		t.Fatalf("another account's revocation removed the session: %v", err)
	}
}

func TestSessionsOfListsAndCountsRevocations(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	id := newAccount(t, d, "alice", 0)

	for i, hash := range [][]byte{[]byte("a"), []byte("b"), []byte("c")} {
		if err := d.CreateSession(ctx, state.Session{
			IDHash: hash, User: id, LastSeenNs: int64(i), AbsoluteNs: 100,
		}); err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
	}
	rows, err := d.SessionsOf(ctx, id)
	if err != nil {
		t.Fatalf("SessionsOf: %v", err)
	}
	if len(rows) != 3 || !bytes.Equal(rows[0].IDHash, []byte("c")) {
		t.Fatalf("the listing is %d rows starting with %q, want 3 most recently used first",
			len(rows), rows[0].IDHash)
	}
	n, err := d.DeleteSessionsOf(ctx, id)
	if err != nil || n != 3 {
		t.Fatalf("DeleteSessionsOf reported %d, %v", n, err)
	}
}

func TestAnAppPasswordRoundTripsWithItsScope(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	user := newAccount(t, d, "alice", 0)

	expires := int64(999)
	id, err := d.CreateAppPassword(ctx, state.NewAppPassword{
		TokenHash: []byte("digest"), User: user, Name: "phone",
		ScopePerms: 0b1011, Shares: []string{"photos", "docs"},
		CreatedNs: 5, ExpiresNs: &expires,
	})
	if err != nil {
		t.Fatalf("CreateAppPassword: %v", err)
	}

	got, err := d.AppPasswordByHash(ctx, []byte("digest"))
	if err != nil {
		t.Fatalf("AppPasswordByHash: %v", err)
	}
	if got.ID != id || got.User != user || got.ScopePerms != 0b1011 {
		t.Fatalf("the credential read back as %+v", got)
	}
	if len(got.Shares) != 2 || got.Shares[0] != "photos" || got.Shares[1] != "docs" {
		t.Fatalf("the scope read back as %v", got.Shares)
	}
	if got.ExpiresNs == nil || *got.ExpiresNs != 999 {
		t.Fatalf("the expiry read back as %v", got.ExpiresNs)
	}

	list, err := d.AppPasswordsOf(ctx, user)
	if err != nil || len(list) != 1 || list[0].ID != id {
		t.Fatalf("the listing is %+v, %v", list, err)
	}
}

// An empty scope column is no list, not a list holding one empty name: the
// two mean "every share this account can see" and "a share with no name".
func TestAnEmptyScopeIsNoShareList(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	user := newAccount(t, d, "alice", 0)

	if _, err := d.CreateAppPassword(ctx, state.NewAppPassword{
		TokenHash: []byte("digest"), User: user, Name: "all",
	}); err != nil {
		t.Fatalf("CreateAppPassword: %v", err)
	}
	got, err := d.AppPasswordByHash(ctx, []byte("digest"))
	if err != nil {
		t.Fatalf("AppPasswordByHash: %v", err)
	}
	if got.Shares != nil {
		t.Fatalf("an empty scope read back as %v", got.Shares)
	}
}

// A single statement both marks and revokes the credential, so a device that
// never reconnects to receive the request cannot continue working.
func TestAWipeRequestAlsoExpiresTheCredential(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	user := newAccount(t, d, "alice", 0)
	id, err := d.CreateAppPassword(ctx, state.NewAppPassword{
		TokenHash: []byte("digest"), User: user, Name: "phone", CreatedNs: 1,
	})
	if err != nil {
		t.Fatalf("CreateAppPassword: %v", err)
	}
	if err = d.RequestAppPasswordWipe(ctx, user, id); err != nil {
		t.Fatalf("RequestAppPasswordWipe: %v", err)
	}
	got, err := d.AppPasswordByHash(ctx, []byte("digest"))
	if err != nil {
		t.Fatalf("AppPasswordByHash: %v", err)
	}
	if !got.WipeWanted || got.ExpiresNs == nil || *got.ExpiresNs != 0 {
		t.Fatalf("the wiped credential reads as %+v", got)
	}
}

func TestRevokingAnotherAccountsAppPasswordRefuses(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	alice := newAccount(t, d, "alice", 0)
	mallory := newAccount(t, d, "mallory", 0)
	id, err := d.CreateAppPassword(ctx, state.NewAppPassword{
		TokenHash: []byte("digest"), User: alice, Name: "phone",
	})
	if err != nil {
		t.Fatalf("CreateAppPassword: %v", err)
	}
	if err := d.DeleteAppPassword(ctx, mallory, id); !errors.Is(err, state.ErrNoSuchAppPassword) {
		t.Fatalf("another account's revocation returned %v", err)
	}
	if err := d.RequestAppPasswordWipe(ctx, mallory, id); !errors.Is(err, state.ErrNoSuchAppPassword) {
		t.Fatalf("another account's wipe returned %v", err)
	}
}

func TestEnrollingASecondFactorDropsTheSMBCredential(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	user := newAccount(t, d, "alice", 0)
	if err := d.PutSMBSecret(ctx, user, state.SMBSecret{Ciphertext: []byte("nt"), KeyVer: 1}); err != nil {
		t.Fatalf("PutSMBSecret: %v", err)
	}

	if err := d.EnrollTOTP(ctx, user,
		state.TOTPSecret{Ciphertext: []byte("totp"), KeyVer: 1}, 10); err != nil {
		t.Fatalf("EnrollTOTP: %v", err)
	}
	if _, err := d.SMBSecretOf(ctx, user); !errors.Is(err, state.ErrNoSMBSecret) {
		t.Fatalf("the SMB credential survived enrolment: %v", err)
	}
	acct, err := d.AccountByID(ctx, user)
	if err != nil || !acct.TOTPEnrolled {
		t.Fatalf("the account does not read as enrolled: %+v, %v", acct, err)
	}
}

// A code presented twice inside its window is refused the second time, even
// when the two presentations race: the insert is the acceptance.
func TestOnlyOneClaimantWinsATOTPStep(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	user := newAccount(t, d, "alice", 0)

	const racers = 8
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		won  int
		fail error
	)
	wg.Add(racers)
	for i := 0; i < racers; i++ {
		task.Go(ctx, "state: concurrent step claim", func() {
			defer wg.Done()
			claimed, err := d.ClaimTOTPStep(ctx, user, 1000, 999, 1)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				fail = err
				return
			}
			if claimed {
				won++
			}
		})
	}
	wg.Wait()
	if fail != nil {
		t.Fatalf("ClaimTOTPStep: %v", fail)
	}
	if won != 1 {
		t.Fatalf("%d of %d claimants won the same step, want exactly 1", won, racers)
	}
}

// Disabling removes the window with the secret: leaving it would refuse the
// steps it holds after a re-enrolment under a different secret, for nothing.
func TestDisablingASecondFactorClearsTheReplayWindow(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	user := newAccount(t, d, "alice", 0)
	if err := d.EnrollTOTP(ctx, user, state.TOTPSecret{Ciphertext: []byte("s"), KeyVer: 1}, 1); err != nil {
		t.Fatalf("EnrollTOTP: %v", err)
	}
	if _, err := d.ClaimTOTPStep(ctx, user, 1000, 999, 1); err != nil {
		t.Fatalf("ClaimTOTPStep: %v", err)
	}
	if err := d.DisableTOTP(ctx, user); err != nil {
		t.Fatalf("DisableTOTP: %v", err)
	}
	if err := d.EnrollTOTP(ctx, user, state.TOTPSecret{Ciphertext: []byte("s2"), KeyVer: 1}, 2); err != nil {
		t.Fatalf("EnrollTOTP: %v", err)
	}
	claimed, err := d.ClaimTOTPStep(ctx, user, 1000, 999, 2)
	if err != nil {
		t.Fatalf("ClaimTOTPStep: %v", err)
	}
	if !claimed {
		t.Fatal("the step was still held after the factor was disabled and re-enrolled")
	}
}

func TestARecoveryCodeIsSingleUseUnderConcurrency(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	user := newAccount(t, d, "alice", 0)
	if err := d.ReplaceRecoveryCodes(ctx, user, [][]byte{[]byte("one"), []byte("two")}); err != nil {
		t.Fatalf("ReplaceRecoveryCodes: %v", err)
	}

	const racers = 8
	var (
		wg  sync.WaitGroup
		mu  sync.Mutex
		won int
	)
	wg.Add(racers)
	for i := 0; i < racers; i++ {
		task.Go(ctx, "state: concurrent code redemption", func() {
			defer wg.Done()
			used, err := d.ConsumeRecoveryCode(ctx, user, []byte("one"))
			if err != nil {
				t.Errorf("ConsumeRecoveryCode: %v", err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			if used {
				won++
			}
		})
	}
	wg.Wait()
	if won != 1 {
		t.Fatalf("%d of %d redeemers accepted one code, want exactly 1", won, racers)
	}
	n, err := d.CountRecoveryCodes(ctx, user)
	if err != nil || n != 1 {
		t.Fatalf("the remaining count is %d, %v", n, err)
	}
}

func TestGeneratingRecoveryCodesReplacesTheSet(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	user := newAccount(t, d, "alice", 0)
	if err := d.ReplaceRecoveryCodes(ctx, user, [][]byte{[]byte("one")}); err != nil {
		t.Fatalf("ReplaceRecoveryCodes: %v", err)
	}
	if err := d.ReplaceRecoveryCodes(ctx, user, [][]byte{[]byte("two"), []byte("three")}); err != nil {
		t.Fatalf("ReplaceRecoveryCodes: %v", err)
	}
	used, err := d.ConsumeRecoveryCode(ctx, user, []byte("one"))
	if err != nil {
		t.Fatalf("ConsumeRecoveryCode: %v", err)
	}
	if used {
		t.Fatal("a code from the replaced set was still accepted")
	}
	n, err := d.CountRecoveryCodes(ctx, user)
	if err != nil || n != 2 {
		t.Fatalf("the new set counts %d, %v", n, err)
	}
}
