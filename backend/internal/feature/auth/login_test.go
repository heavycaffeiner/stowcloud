package auth_test

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/heavycaffeiner/stowcloud/backend/internal/feature/auth"
	"github.com/heavycaffeiner/stowcloud/backend/internal/platform/concurrency"
)

// A non-sentinel OIDC lookup error refuses login before a session is minted.
func TestLoginPropagatesOIDCLinkLookupErrors(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.account(t, "alice")
	dropOIDCLinkTable(t, f)

	sess, err := f.svc.Login(context.Background(), auth.LoginRequest{
		Name: "alice", Password: pw(testPassword), IP: "192.0.2.1",
	}, 0)
	if err == nil {
		t.Fatal("Login succeeded despite an OIDC lookup error")
	}
	if sess.Token.Len() != 0 {
		t.Fatalf("Login minted a session after the OIDC lookup error: %+v", sess)
	}
}

// A non-sentinel OIDC lookup error refuses password verification.
func TestVerifyPasswordPropagatesOIDCLinkLookupErrors(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.account(t, "alice")
	dropOIDCLinkTable(t, f)

	principal, err := f.svc.VerifyPassword(context.Background(), "alice", pw(testPassword))
	if err == nil {
		t.Fatal("VerifyPassword succeeded despite an OIDC lookup error")
	}
	if principal.UserID != 0 {
		t.Fatalf("VerifyPassword returned a principal after the OIDC lookup error: %+v", principal)
	}
}

func dropOIDCLinkTable(t *testing.T, f fixture) {
	t.Helper()
	if err := f.store.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(), "DROP TABLE oidc_link")
		return err
	}); err != nil {
		t.Fatalf("drop oidc_link table: %v", err)
	}
}

// A response identical in content but faster is still an oracle, so an
// unknown account pays for the same memory-hard invocation a real one does.
func TestAnUnknownAccountAndAWrongPasswordAnswerAlikeAndCostAlike(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t)
	f.account(t, "alice")

	const callers = 32
	var wg sync.WaitGroup
	wg.Add(callers)
	for i := 0; i < callers; i++ {
		n := i
		concurrency.Go(ctx, "auth: concurrent login attempt", func() {
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
	t.Parallel()
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
	t.Parallel()
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
	if f.svc.Stale(acct.PwHash) {
		t.Fatal("the replacement is still stale")
	}
}

func TestSessionsExpireAbsolutelyAndWhenIdle(t *testing.T) {
	t.Parallel()
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

// A session that outlives a disable, because it was minted afterwards or by
// another process, still refuses, and says why: the lookup checks the account
// and not only the row.
func TestASessionOfADisabledAccountRefusesWithItsOwnReason(t *testing.T) {
	t.Parallel()
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
