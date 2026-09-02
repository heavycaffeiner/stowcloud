package auth_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/auth"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/dbfile"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/state"
)

// An account created without a file-sharing credential had no way to reach
// that protocol until it changed its password, and the interface's "set a
// separate password" framing made that defect read as a policy.
func TestANewAccountReachesTheFileSharingProtocolWithNoFurtherStep(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	id := f.account(t, "alice")

	state, err := f.svc.SMBStateOf(ctx, id)
	if err != nil {
		t.Fatalf("SMBStateOf: %v", err)
	}
	if state.Credential != auth.SMBCredentialAccount {
		t.Fatalf("a fresh account reports %+v", state)
	}
	creds, err := f.svc.SMBCredentials(ctx)
	if err != nil {
		t.Fatalf("SMBCredentials: %v", err)
	}
	if len(creds) != 1 || creds[0].Name != "alice" {
		t.Fatalf("the publishable credentials are %+v", creds)
	}
	offset, err := num.Narrow[uint32](id)
	if err != nil {
		t.Fatalf("the account id does not fit a uid: %v", err)
	}
	if creds[0].UID != auth.SMBBaseUid+offset {
		t.Fatalf("the uid is %d, want the row id offset by the base", creds[0].UID)
	}
}

// A committed transaction is not a completed security decision until the
// sidecar file agrees, so every credential-changing path re-renders and tells
// the publisher.
func TestEveryCredentialChangeRepublishesAndNotifies(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.admin(t, "admin")
	id := f.account(t, "alice")

	baseline := *f.published
	sinkBaseline := f.sink.n

	steps := []struct {
		name string
		run  func() error
	}{
		{"a new account", func() error { _, err := f.svc.CreateUser(ctx, "carol", "Carol", pw(testPassword)); return err }},
		{"a password change", func() error { return f.svc.SetPassword(ctx, id, pw("another long password")) }},
		{"a separate password", func() error { return f.svc.SetSMBPassword(ctx, id, pw(testPassword)) }},
		{"clearing it", func() error { _, err := f.svc.ClearSMBPassword(ctx, id); return err }},
		{"an access toggle", func() error { return f.svc.SetSMBAccess(ctx, id, false, true) }},
		{"a disable", func() error { return f.svc.DisableAccount(ctx, id) }},
		{"an enable", func() error { return f.svc.EnableAccount(ctx, id) }},
		{"a deletion", func() error { return f.svc.DeleteUser(ctx, id) }},
	}
	for _, step := range steps {
		before, sinkBefore := *f.published, f.sink.n
		if err := step.run(); err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
		if *f.published == before {
			t.Fatalf("%s did not re-render the credential file", step.name)
		}
		if f.sink.n == sinkBefore {
			t.Fatalf("%s did not tell the publisher", step.name)
		}
	}
	if *f.published <= baseline || f.sink.n <= sinkBaseline {
		t.Fatal("nothing was published at all")
	}
}

// The deleted account has to leave the published file too, or it keeps
// working over the older protocol.
func TestADeletedAccountLeavesThePublishedCredentials(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.admin(t, "admin")
	id := f.account(t, "alice")

	if err := f.svc.DeleteUser(ctx, id); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	creds, err := f.svc.SMBCredentials(ctx)
	if err != nil {
		t.Fatalf("SMBCredentials: %v", err)
	}
	for _, c := range creds {
		if c.Name == "alice" {
			t.Fatal("the deleted account is still publishable")
		}
	}
}

// Policy governs publication only, never storage, so reverting it restores
// access with no one needing to set a password again.
func TestTheSecondFactorPolicyOnlyChangesWhatIsPublished(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	id := f.account(t, "alice")
	if err := f.svc.SetSMBPassword(ctx, id, pw(testPassword)); err != nil {
		t.Fatalf("SetSMBPassword: %v", err)
	}
	secretB32, err := f.svc.GenerateTOTPSecret()
	if err != nil {
		t.Fatalf("GenerateTOTPSecret: %v", err)
	}
	if err = f.svc.EnrollTOTP(ctx, id, secretB32); err != nil {
		t.Fatalf("EnrollTOTP: %v", err)
	}
	// Enrolment drops the derived credential, so the separate one goes back.
	if err = f.svc.SetSMBPassword(ctx, id, pw(testPassword)); err != nil {
		t.Fatalf("SetSMBPassword: %v", err)
	}

	f.svc.SetSMBTOTPPolicy(auth.TOTPBlock)
	creds, err := f.svc.SMBCredentials(ctx)
	if err != nil {
		t.Fatalf("SMBCredentials: %v", err)
	}
	if len(creds) != 0 {
		t.Fatalf("a blocked account is still published: %+v", creds)
	}
	state, err := f.svc.SMBStateOf(ctx, id)
	if err != nil {
		t.Fatalf("SMBStateOf: %v", err)
	}
	if state.Reason != auth.SMBUnavailableTOTPBlocked {
		t.Fatalf("the blocked account reports %+v", state)
	}

	f.svc.SetSMBTOTPPolicy(auth.TOTPRequireSeparate)
	if creds, err = f.svc.SMBCredentials(ctx); err != nil {
		t.Fatalf("SMBCredentials: %v", err)
	}
	if len(creds) != 1 {
		t.Fatalf("moving the policy back did not restore access: %+v", creds)
	}
}

// Clearing the separate password means losing the protocol for an account
// whose account password cannot serve it, and saying so beats reporting a
// success that reads as "nothing changed".
func TestClearingASeparatePasswordReportsWhetherTheAccountPasswordTakesOver(t *testing.T) {
	ctx := context.Background()

	t.Run("an ordinary account reverts", func(t *testing.T) {
		f := newFixture(t)
		id := f.account(t, "alice")
		if err := f.svc.SetSMBPassword(ctx, id, pw(testPassword)); err != nil {
			t.Fatalf("SetSMBPassword: %v", err)
		}
		revertible, err := f.svc.ClearSMBPassword(ctx, id)
		if err != nil || !revertible {
			t.Fatalf("ClearSMBPassword = %v, %v", revertible, err)
		}
	})

	t.Run("an opted-out account does not", func(t *testing.T) {
		f := newFixture(t)
		id := f.account(t, "alice")
		if err := f.svc.SetSMBAccess(ctx, id, true, false); err != nil {
			t.Fatalf("SetSMBAccess: %v", err)
		}
		revertible, err := f.svc.ClearSMBPassword(ctx, id)
		if err != nil || revertible {
			t.Fatalf("ClearSMBPassword = %v, %v", revertible, err)
		}
	})

	t.Run("a provider-linked account does not", func(t *testing.T) {
		f := newFixture(t)
		id := f.account(t, "alice")
		if err := f.svc.CreateOIDCLink(ctx, id, "https://idp", "subject"); err != nil {
			t.Fatalf("CreateOIDCLink: %v", err)
		}
		revertible, err := f.svc.ClearSMBPassword(ctx, id)
		if err != nil || revertible {
			t.Fatalf("ClearSMBPassword = %v, %v", revertible, err)
		}
	})

	t.Run("a blocked second factor does not", func(t *testing.T) {
		f := newFixture(t)
		id := f.account(t, "alice")
		secretB32, err := f.svc.GenerateTOTPSecret()
		if err != nil {
			t.Fatalf("GenerateTOTPSecret: %v", err)
		}
		if err = f.svc.EnrollTOTP(ctx, id, secretB32); err != nil {
			t.Fatalf("EnrollTOTP: %v", err)
		}
		f.svc.SetSMBTOTPPolicy(auth.TOTPBlock)
		revertible, err := f.svc.ClearSMBPassword(ctx, id)
		if err != nil || revertible {
			t.Fatalf("ClearSMBPassword = %v, %v", revertible, err)
		}
	})
}

// A session is not a credential: signing somebody out of every device because
// they changed their own password is a surprise rather than a property.
func TestAPasswordChangeKeepsSessionsAndInvalidatesCachedDecisions(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	id := f.account(t, "alice")

	sess, err := f.svc.CreateSession(ctx, id, "", "", 0, 0)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	// Warm the credential cache with a decision under the old password.
	if _, verr := f.svc.VerifyPassword(ctx, "alice", pw("the new password here")); !errors.Is(verr, auth.ErrCredentials) {
		t.Fatalf("the future password verified early: %v", verr)
	}

	if err = f.svc.SetPassword(ctx, id, pw("the new password here")); err != nil {
		t.Fatalf("SetPassword: %v", err)
	}
	if _, err = f.svc.LookupSession(ctx, sess.Token); err != nil {
		t.Fatalf("the session did not survive a password change: %v", err)
	}
	if _, err = f.svc.VerifyPassword(ctx, "alice", pw("the new password here")); err != nil {
		t.Fatalf("the cached refusal outlived the change: %v", err)
	}
}

// A live session is what somebody who walked past an unlocked screen already
// has, and the credentials these screens create outlive it.
func TestReconfirmingAPasswordAnswersWithoutRevealingWhoExists(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	id := f.account(t, "alice")

	ok, err := f.svc.VerifyAccountPassword(ctx, id, pw(testPassword))
	if err != nil || !ok {
		t.Fatalf("the right password returned %v, %v", ok, err)
	}
	if ok, err = f.svc.VerifyAccountPassword(ctx, id, pw("a wrong password")); err != nil || ok {
		t.Fatalf("a wrong password returned %v, %v", ok, err)
	}
	if ok, err = f.svc.VerifyAccountPassword(ctx, 404, pw(testPassword)); err != nil || ok {
		t.Fatalf("a missing account returned %v, %v", ok, err)
	}
}

// Recovering from a deployment nobody can administer means editing the
// database by hand.
func TestTheLastAdministratorIsProtected(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	admin := f.admin(t, "admin")

	if err := f.svc.DisableAccount(ctx, admin); !errors.Is(err, auth.ErrLastAdmin) {
		t.Fatalf("disabling the last administrator returned %v", err)
	}
	if err := f.svc.DeleteUser(ctx, admin); !errors.Is(err, auth.ErrLastAdmin) {
		t.Fatalf("deleting the last administrator returned %v", err)
	}
}

func TestTheAdministrativeListingCarriesNoHash(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	admin := f.admin(t, "admin")
	alice := f.account(t, "alice")

	rows, err := f.svc.ListUsers(ctx)
	if err != nil || len(rows) != 2 {
		t.Fatalf("ListUsers returned %d rows, %v", len(rows), err)
	}
	one, err := f.svc.UserByID(ctx, alice)
	if err != nil {
		t.Fatalf("UserByID: %v", err)
	}
	if one.Name != "alice" || one.IsAdmin {
		t.Fatalf("the row reads as %+v", one)
	}
	isAdmin, err := f.svc.IsAdmin(ctx, admin)
	if err != nil || !isAdmin {
		t.Fatalf("IsAdmin returned %v, %v", isAdmin, err)
	}
}

func TestQuotaRefusesACapThatIsNotACap(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	id := f.account(t, "alice")

	for _, bytes := range []int64{0, -1} {
		b := bytes
		if err := f.svc.SetQuota(ctx, id, &b); !errors.Is(err, auth.ErrInvalidQuota) {
			t.Fatalf("a cap of %d returned %v", bytes, err)
		}
	}
	ceiling := int64(1 << 30)
	if err := f.svc.SetQuota(ctx, id, &ceiling); err != nil {
		t.Fatalf("SetQuota: %v", err)
	}
	if err := f.svc.SetQuota(ctx, id, nil); err != nil {
		t.Fatalf("clearing the quota: %v", err)
	}
}

// Two accounts with no group in common are not in each other's directory,
// and out of scope and absent are one answer: telling them apart is a
// directory a stranger can walk.
func TestTheAccountDirectoryAppliesVisibility(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	admin := f.admin(t, "admin")
	alice := f.account(t, "alice")
	bob := f.account(t, "bob")

	if _, visible, err := f.svc.AccountInfoByLogin(ctx, alice, "bob"); err != nil || visible {
		t.Fatalf("a stranger was visible: %v, %v", visible, err)
	}
	if _, visible, err := f.svc.AccountInfoByLogin(ctx, alice, "nobody"); err != nil || visible {
		t.Fatalf("an absent account answered differently: %v, %v", visible, err)
	}
	if _, visible, err := f.svc.AccountInfoByLogin(ctx, alice, "alice"); err != nil || !visible {
		t.Fatalf("the caller's own account was not visible: %v, %v", visible, err)
	}
	if _, visible, err := f.svc.AccountInfoByLogin(ctx, admin, "bob"); err != nil || !visible {
		t.Fatalf("an administrator could not see an account: %v, %v", visible, err)
	}

	group, err := f.svc.CreateGroup(ctx, "staff")
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	for _, id := range []int64{alice, bob} {
		if err = f.svc.AddToGroup(ctx, id, group); err != nil {
			t.Fatalf("AddToGroup: %v", err)
		}
	}
	info, visible, err := f.svc.AccountInfoByLogin(ctx, alice, "bob")
	if err != nil || !visible {
		t.Fatalf("a shared group did not make the account visible: %v, %v", visible, err)
	}
	if info.LoginName != "bob" || len(info.Groups) != 1 || info.Groups[0] != "staff" {
		t.Fatalf("the projection is %+v", info)
	}
}

func TestResolvingAnAccountOrGroupReportsAbsenceRatherThanRaisingIt(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	id := f.account(t, "alice")
	group, err := f.svc.CreateGroup(ctx, "staff")
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}

	got, found, err := f.svc.ResolveAccount(ctx, "alice")
	if err != nil || !found || got != id {
		t.Fatalf("ResolveAccount returned %d, %v, %v", got, found, err)
	}
	if _, found, err = f.svc.ResolveAccount(ctx, "nobody"); err != nil || found {
		t.Fatalf("an absent account returned %v, %v", found, err)
	}
	if got, found, err = f.svc.ResolveGroup(ctx, "staff"); err != nil || !found || got != group {
		t.Fatalf("ResolveGroup returned %d, %v, %v", got, found, err)
	}
	if _, found, err = f.svc.ResolveGroup(ctx, "nowhere"); err != nil || found {
		t.Fatalf("an absent group returned %v, %v", found, err)
	}
}

// Without the crossing a membership change is live in the database and stale
// in the process answering requests.
func TestEveryMembershipChangeNotifiesTheEvaluator(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	id := f.account(t, "alice")

	reloads := 0
	// The callback is wired at construction, so this test builds its own
	// service over the same database.
	svc := newServiceWithMembership(t, f, func() { reloads++ })
	group, err := svc.CreateGroup(ctx, "staff")
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	for _, step := range []struct {
		name string
		run  func() error
	}{
		{"adding", func() error { return svc.AddToGroup(ctx, id, group) }},
		{"removing", func() error { return svc.RemoveFromGroup(ctx, id, group) }},
		{"replacing the set", func() error { return svc.SetMembership(ctx, id, []int64{group}) }},
		{"deleting the group", func() error { return svc.DeleteGroup(ctx, group) }},
	} {
		before := reloads
		if err := step.run(); err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
		if reloads == before {
			t.Fatalf("%s did not notify the evaluator", step.name)
		}
	}
}

// The account file is written only where the publisher asks for one, and it
// carries the same accounts at the same uids as the credential file. The two
// are matched by uid rather than by name, so a disagreement leaves the import
// with nothing for that account and the symptom is a client that cannot
// connect.
func TestTheAccountFileCarriesTheSameAccountsAtTheSameUids(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.account(t, "alice")
	f.account(t, "bob")

	path := filepath.Join(t.TempDir(), "passwd")
	if err := f.svc.PublishPasswdEntries(ctx, path, 1000); err != nil {
		t.Fatalf("PublishPasswdEntries: %v", err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the account file: %v", err)
	}

	creds, err := f.svc.SMBCredentials(ctx)
	if err != nil {
		t.Fatalf("SMBCredentials: %v", err)
	}
	if len(creds) != 2 {
		t.Fatalf("the publishable credentials are %+v", creds)
	}
	for _, c := range creds {
		want := fmt.Sprintf("%s:%d\n", c.Name, c.UID)
		if !strings.Contains(string(body), want) {
			t.Errorf("the account file is missing %q:\n%s", want, body)
		}
	}
	if *f.passwdGID != 1000 {
		t.Errorf("the renderer was handed gid %d, want the one the caller named", *f.passwdGID)
	}
}

// An account that cannot reach the protocol is absent from the account file as
// well as the credential file. Present in one alone it would still resolve as a
// name, which is a login that gets further than it should before failing.
func TestAnIneligibleAccountIsAbsentFromTheAccountFile(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	id := f.account(t, "alice")
	f.account(t, "bob")

	// Opting out is the strongest of the three refusals and the one an
	// account applies to itself.
	if err := f.svc.SetSMBAccess(ctx, id, true, false); err != nil {
		t.Fatalf("SetSMBAccess: %v", err)
	}

	path := filepath.Join(t.TempDir(), "passwd")
	if err := f.svc.PublishPasswdEntries(ctx, path, 1000); err != nil {
		t.Fatalf("PublishPasswdEntries: %v", err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the account file: %v", err)
	}
	if strings.Contains(string(body), "alice") {
		t.Errorf("the opted-out account is still in the account file:\n%s", body)
	}
	if !strings.Contains(string(body), "bob") {
		t.Errorf("the eligible account is missing from the account file:\n%s", body)
	}
}

// A deployment with no account renderer writes no account file, rather than an
// empty one. An empty file is a roster saying nobody may connect, which is a
// different claim from this deployment not publishing one.
func TestNoAccountFileIsWrittenWithoutARenderer(t *testing.T) {
	dir := t.TempDir()
	fdb, err := dbfile.Open(context.Background(), state.Spec(filepath.Join(dir, "state.db")))
	if err != nil {
		t.Fatalf("opening the state database: %v", err)
	}
	t.Cleanup(func() {
		if cerr := fdb.Close(); cerr != nil {
			t.Errorf("Close: %v", cerr)
		}
	})
	svc := auth.New(auth.Config{Store: state.New(fdb), StoreDir: dir})
	t.Setenv("SC_MASTER_KEY_FILE", filepath.Join(dir, "master.key"))
	if _, oerr := svc.OpenMasterKey(context.Background()); oerr != nil {
		t.Fatalf("OpenMasterKey: %v", oerr)
	}

	path := filepath.Join(dir, "passwd")
	if perr := svc.PublishPasswdEntries(context.Background(), path, 1000); perr != nil {
		t.Fatalf("PublishPasswdEntries: %v", perr)
	}
	if _, serr := os.Stat(path); !os.IsNotExist(serr) {
		t.Errorf("an account file was written with no renderer configured: %v", serr)
	}
}
