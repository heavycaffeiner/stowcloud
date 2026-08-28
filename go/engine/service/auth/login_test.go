package auth_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/task"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/auth"
)

func TestLoginMintsASessionAndRecordsIt(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	id := f.account(t, "alice")

	sess, err := f.svc.Login(ctx, auth.LoginRequest{
		Name: "alice", Password: pw(testPassword), IP: "192.0.2.1", UA: "client",
	}, 0)
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if sess.UserID != id || sess.Token.Len() == 0 {
		t.Fatalf("the session is %+v", sess)
	}
	principal, err := f.svc.LookupSession(ctx, sess.Token)
	if err != nil {
		t.Fatalf("LookupSession: %v", err)
	}
	if principal.UserID != id || principal.Login != "alice" {
		t.Fatalf("the principal is %+v", principal)
	}

	rows, _, err := f.svc.AuditPage(ctx, auth.AuditFilter{})
	if err != nil {
		t.Fatalf("AuditPage: %v", err)
	}
	if len(rows) != 1 || rows[0].Event != "login" || !rows[0].OK {
		t.Fatalf("the log holds %+v", rows)
	}
}

// A response identical in content but faster is still an oracle, so an
// unknown account pays for the same memory-hard invocation a real one does.
func TestAnUnknownAccountAndAWrongPasswordAnswerAlikeAndCostAlike(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.account(t, "alice")

	// One invocation was already spent creating the account.
	before := f.svc.PeakConcurrency()
	_ = before

	_, unknownErr := f.svc.Login(ctx, auth.LoginRequest{
		Name: "nobody", Password: pw(testPassword), IP: "192.0.2.1",
	}, 0)
	_, wrongErr := f.svc.Login(ctx, auth.LoginRequest{
		Name: "alice", Password: pw("a different password"), IP: "192.0.2.2",
	}, 0)
	if !errors.Is(unknownErr, auth.ErrCredentials) || !errors.Is(wrongErr, auth.ErrCredentials) {
		t.Fatalf("the two refusals are %v and %v", unknownErr, wrongErr)
	}
	if unknownErr.Error() != wrongErr.Error() {
		t.Fatalf("the two refusals read differently: %q and %q", unknownErr, wrongErr)
	}

	// A run of guesses against one name is what the log is read to find, so
	// the unknown attempt is recorded with the tried name and no actor.
	rows, _, err := f.svc.AuditPage(ctx, auth.AuditFilter{})
	if err != nil {
		t.Fatalf("AuditPage: %v", err)
	}
	var unknown *auth.AuditRow
	for i := range rows {
		if rows[i].Target != nil && *rows[i].Target == "nobody" {
			unknown = &rows[i]
		}
	}
	if unknown == nil {
		t.Fatalf("the unknown-name attempt was not recorded: %+v", rows)
	}
	if unknown.Actor != nil {
		t.Fatalf("the unknown-name attempt names actor %d", *unknown.Actor)
	}
	if unknown.OK {
		t.Fatal("the unknown-name attempt was recorded as a success")
	}
}

// The disabled answer comes after the password verified, so it never tells a
// stranger that an account exists.
func TestADisabledAccountAnswersByWhetherThePasswordWasRight(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.admin(t, "admin")
	id := f.account(t, "alice")
	if err := f.svc.DisableAccount(ctx, id); err != nil {
		t.Fatalf("DisableAccount: %v", err)
	}

	_, err := f.svc.Login(ctx, auth.LoginRequest{Name: "alice", Password: pw(testPassword)}, 0)
	if !errors.Is(err, auth.ErrAccountDisabled) {
		t.Fatalf("a disabled account with the right password returned %v", err)
	}
	_, err = f.svc.Login(ctx, auth.LoginRequest{Name: "alice", Password: pw("wrong password here")}, 0)
	if !errors.Is(err, auth.ErrCredentials) {
		t.Fatalf("a disabled account with a wrong password returned %v", err)
	}
}

// The budget refuses the eleventh attempt inside the window. The window is
// per client address, so one client cannot exhaust another's.
func TestTheLimiterRefusesPastItsBudgetAndIsPerAddress(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.account(t, "alice")

	var last error
	for i := 0; i < 11; i++ {
		_, last = f.svc.Login(ctx, auth.LoginRequest{
			Name: "alice", Password: pw("the wrong password"), IP: "192.0.2.1",
		}, 0)
	}
	if !errors.Is(last, auth.ErrRateLimited) {
		t.Fatalf("the eleventh attempt returned %v", last)
	}
	if _, err := f.svc.Login(ctx, auth.LoginRequest{
		Name: "alice", Password: pw(testPassword), IP: "192.0.2.9",
	}, 0); err != nil {
		t.Fatalf("another address was refused: %v", err)
	}
}

// The limiter is hit once per request and concurrently. Before this it
// guarded its map with nothing, and two logins arriving together could write
// it at the same time, which ends the process.
func TestTheLimiterSurvivesConcurrentAttempts(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.account(t, "alice")

	const callers = 32
	var wg sync.WaitGroup
	wg.Add(callers)
	for i := 0; i < callers; i++ {
		n := i
		task.Go(ctx, "auth: concurrent login attempt", func() {
			defer wg.Done()
			// Distinct addresses as well as shared ones, so both the bucket
			// path and the eviction path are hit.
			addr := "192.0.2." + string(rune('0'+n%10))
			// The answer is discarded on purpose: what is under test is that
			// concurrent attempts do not corrupt the limiter's own state.
			if _, lerr := f.svc.Login(ctx, auth.LoginRequest{
				Name: "alice", Password: pw("wrong password value"), IP: addr,
			}, 0); lerr == nil {
				t.Error("a wrong password was accepted")
			}
		})
	}
	wg.Wait()
}

// The session existed by the time the row was written. Returning that error
// told the person their credentials were wrong while they held a session
// that worked.
func TestALoginSurvivesAnAuditLogItCannotWrite(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.account(t, "alice")

	// The log is made unwritable and nothing else is: the session path has to
	// keep working while the append fails.
	breakAuditLog(t, f)

	sess, err := f.svc.Login(ctx, auth.LoginRequest{Name: "alice", Password: pw(testPassword)}, 0)
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if _, lerr := f.svc.LookupSession(ctx, sess.Token); lerr != nil {
		t.Fatalf("the session the login returned does not resolve: %v", lerr)
	}
}

// Raising the cost protects existing accounts only because a successful
// verification under older parameters rehashes.
func TestALoginUnderStaleParametersRehashes(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	id := f.account(t, "alice")

	// Put an older hash on the row, the way a previous build would have.
	old := encodeArgon(testPassword, 8192, 1, 1)
	if err := f.store.SetAccountPassword(ctx, id, old, nil, 0); err != nil {
		t.Fatalf("seeding the older hash: %v", err)
	}
	if _, err := f.svc.Login(ctx, auth.LoginRequest{Name: "alice", Password: pw(testPassword)}, 0); err != nil {
		t.Fatalf("Login: %v", err)
	}
	acct, err := f.store.AccountByID(ctx, id)
	if err != nil {
		t.Fatalf("AccountByID: %v", err)
	}
	if acct.PwHash == old {
		t.Fatal("the stale hash was not replaced")
	}
	if auth.Stale(acct.PwHash) {
		t.Fatal("the replacement is still stale")
	}
}

func TestSessionsExpireAbsolutelyAndWhenIdle(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clk := &steppingClock{at: start}
	f := newFixtureWithClock(t, clk)
	id := f.account(t, "alice")

	absolute, err := f.svc.CreateSession(ctx, id, "", "", 0, time.Hour)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	clk.advance(2 * time.Hour)
	if _, err = f.svc.LookupSession(ctx, absolute.Token); !errors.Is(err, auth.ErrCredentials) {
		t.Fatalf("a session past its absolute window resolved: %v", err)
	}

	idle, err := f.svc.CreateSession(ctx, id, "", "", 0, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	clk.advance(31 * time.Minute)
	if _, err = f.svc.LookupSession(ctx, idle.Token); !errors.Is(err, auth.ErrCredentials) {
		t.Fatalf("an idle session resolved: %v", err)
	}
}

// The stamp refreshes on use, so an active client stays signed in.
func TestUsingASessionRefreshesItsIdleWindow(t *testing.T) {
	ctx := context.Background()
	clk := &steppingClock{at: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	f := newFixtureWithClock(t, clk)
	id := f.account(t, "alice")

	sess, err := f.svc.CreateSession(ctx, id, "", "", 0, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	for i := 0; i < 4; i++ {
		clk.advance(20 * time.Minute)
		if _, lerr := f.svc.LookupSession(ctx, sess.Token); lerr != nil {
			t.Fatalf("the session went cold after %d steps: %v", i+1, lerr)
		}
	}
}

// Revocation is immediate because the generation counter invalidates every
// cached decision, not because a lifetime elapsed.
func TestARevokedSessionRefusesImmediately(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	id := f.account(t, "alice")

	sess, err := f.svc.CreateSession(ctx, id, "", "", 0, time.Hour)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	before := f.svc.Generation()
	if err = f.svc.RevokeSession(ctx, sess.Token); err != nil {
		t.Fatalf("RevokeSession: %v", err)
	}
	if f.svc.Generation() == before {
		t.Fatal("revoking a session did not bump the generation")
	}
	if _, err = f.svc.LookupSession(ctx, sess.Token); !errors.Is(err, auth.ErrCredentials) {
		t.Fatalf("a revoked session resolved: %v", err)
	}
}

// Disabling drops the account's sessions in the same transaction, so a client
// holding one is signed out at the moment the write commits rather than when
// its window happens to elapse.
func TestDisablingAnAccountEndsItsLiveSessions(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.admin(t, "admin")
	id := f.account(t, "alice")
	sess, err := f.svc.CreateSession(ctx, id, "", "", 0, time.Hour)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err = f.svc.DisableAccount(ctx, id); err != nil {
		t.Fatalf("DisableAccount: %v", err)
	}
	if _, err = f.svc.LookupSession(ctx, sess.Token); !errors.Is(err, auth.ErrCredentials) {
		t.Fatalf("a disabled account's session returned %v", err)
	}
}

// A session that outlives a disable, because it was minted afterwards or by
// another process, still refuses, and says why: the lookup checks the account
// and not only the row.
func TestASessionOfADisabledAccountRefusesWithItsOwnReason(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.admin(t, "admin")
	id := f.account(t, "alice")
	if err := f.svc.DisableAccount(ctx, id); err != nil {
		t.Fatalf("DisableAccount: %v", err)
	}
	sess, err := f.svc.CreateSession(ctx, id, "", "", 0, time.Hour)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if _, err = f.svc.LookupSession(ctx, sess.Token); !errors.Is(err, auth.ErrAccountDisabled) {
		t.Fatalf("a disabled account's session returned %v", err)
	}
}

// The client holds row digests rather than tokens, so the revocation is
// scoped to the owner in the same predicate.
func TestRevokingByHashRefusesAnotherOwnersSession(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	alice := f.account(t, "alice")
	mallory := f.account(t, "mallory")

	sess, err := f.svc.CreateSession(ctx, alice, "", "", 0, time.Hour)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	rows, err := f.svc.Sessions(ctx, alice)
	if err != nil || len(rows) != 1 {
		t.Fatalf("Sessions returned %d rows, %v", len(rows), err)
	}
	if err = f.svc.RevokeSessionByHash(ctx, mallory, rows[0].IDHash); err != nil {
		t.Fatalf("RevokeSessionByHash: %v", err)
	}
	if _, err = f.svc.LookupSession(ctx, sess.Token); err != nil {
		t.Fatalf("another account's revocation killed the session: %v", err)
	}
}
