package state_test

import (
	"context"
	"errors"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/store/state"
)

func newAccount(t *testing.T, d *state.DB, name string, role int64) int64 {
	t.Helper()
	id, err := d.CreateAccount(context.Background(), state.NewAccount{
		Name:      name,
		PwHash:    "$argon2id$stub",
		Role:      role,
		CreatedNs: 1,
	}, nil)
	if err != nil {
		t.Fatalf("CreateAccount(%q): %v", name, err)
	}
	return id
}

func TestAnAccountRoundTripsByNameAndByID(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)

	id, err := d.CreateAccount(ctx, state.NewAccount{
		Name: "alice", Display: "Alice", PwHash: "$hash", CreatedNs: 42,
	}, nil)
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	byID, err := d.AccountByID(ctx, id)
	if err != nil {
		t.Fatalf("AccountByID: %v", err)
	}
	byName, err := d.AccountByName(ctx, "alice")
	if err != nil {
		t.Fatalf("AccountByName: %v", err)
	}
	if byID != byName {
		t.Fatalf("the two reads differ: %+v and %+v", byID, byName)
	}
	if byID.Display != "Alice" || byID.PwHash != "$hash" || byID.CreatedNs != 42 {
		t.Fatalf("the account read back as %+v", byID)
	}
	if !byID.SMBEnabled || byID.Disabled || byID.TOTPEnrolled {
		t.Fatalf("a fresh account read back as %+v", byID)
	}
}

// Imported accounts have no display name. Reading a missing one previously broke
// the scan, leaving the account unable to sign in at all and producing a server
// error rather than anything actionable.
func TestAnAbsentDisplayNameReadsAsEmpty(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	id := newAccount(t, d, "bob", 0)

	acct, err := d.AccountByID(ctx, id)
	if err != nil {
		t.Fatalf("AccountByID: %v", err)
	}
	if acct.Display != "" {
		t.Fatalf("the display name read back as %q", acct.Display)
	}
}

func TestAMissingAccountIsATypedRefusal(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)

	if _, err := d.AccountByID(ctx, 404); !errors.Is(err, state.ErrNoSuchAccount) {
		t.Fatalf("AccountByID on a missing row returned %v", err)
	}
	if _, err := d.AccountByName(ctx, "nobody"); !errors.Is(err, state.ErrNoSuchAccount) {
		t.Fatalf("AccountByName on a missing row returned %v", err)
	}
}

// The duplicate is typed rather than a driver message: a constraint failure
// reaching a client as a server error tells whoever typed the name that
// something broke rather than that the name is taken.
func TestADuplicateNameIsErrNameTaken(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	newAccount(t, d, "alice", 0)

	_, err := d.CreateAccount(ctx, state.NewAccount{Name: "alice", PwHash: "x"}, nil)
	if !errors.Is(err, state.ErrNameTaken) {
		t.Fatalf("a duplicate name returned %v", err)
	}
}

// The name column collates case-insensitively, so two spellings of one name
// are one account rather than two people who both believe they own it.
func TestNamesCollideAcrossCase(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	newAccount(t, d, "alice", 0)

	if _, err := d.CreateAccount(ctx, state.NewAccount{Name: "ALICE", PwHash: "x"}, nil); !errors.Is(err, state.ErrNameTaken) {
		t.Fatalf("a name differing only in case returned %v", err)
	}
}

// The credential for the file-sharing protocol is sealed inside the creating
// transaction, because it comes from a plaintext that exists only then.
func TestCreationSealsTheSMBCredentialInTheSameTransaction(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)

	id, err := d.CreateAccount(ctx, state.NewAccount{Name: "alice", PwHash: "x"},
		func(userID int64) ([]byte, uint32, error) {
			if userID == 0 {
				t.Error("the seal callback was handed no account id")
			}
			return []byte("sealed"), 7, nil
		})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	sec, err := d.SMBSecretOf(ctx, id)
	if err != nil {
		t.Fatalf("SMBSecretOf: %v", err)
	}
	if string(sec.Ciphertext) != "sealed" || sec.KeyVer != 7 {
		t.Fatalf("the sealed credential read back as %+v", sec)
	}
}

// A seal that fails takes the account with it: an account row without the
// credential its creation promised is the half-write this transaction exists
// to prevent.
func TestAFailedSealLeavesNoAccount(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)

	boom := errors.New("no key")
	if _, err := d.CreateAccount(ctx, state.NewAccount{Name: "alice", PwHash: "x"},
		func(int64) ([]byte, uint32, error) { return nil, 0, boom }); !errors.Is(err, boom) {
		t.Fatalf("CreateAccount returned %v, want the seal's own error", err)
	}
	if _, err := d.AccountByName(ctx, "alice"); !errors.Is(err, state.ErrNoSuchAccount) {
		t.Fatal("the account survived a failed seal")
	}
}

func TestDisablingAnAccountDropsItsSessions(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	admin := newAccount(t, d, "admin", state.RoleAdmin)
	id := newAccount(t, d, "alice", 0)
	_ = admin

	if err := d.CreateSession(ctx, state.Session{
		IDHash: []byte("hash"), User: id, CreatedNs: 1, LastSeenNs: 1, AbsoluteNs: 100,
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := d.SetAccountDisabled(ctx, id, true); err != nil {
		t.Fatalf("SetAccountDisabled: %v", err)
	}
	if _, err := d.SessionByHash(ctx, []byte("hash")); !errors.Is(err, state.ErrNoSuchSession) {
		t.Fatalf("the session survived the disable: %v", err)
	}
}

// Recovering from a deployment nobody can administer means editing the
// database by hand, so the write that would cause it is refused instead.
func TestTheLastAdministratorCannotBeDisabledOrDeleted(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	admin := newAccount(t, d, "admin", state.RoleAdmin)

	if err := d.SetAccountDisabled(ctx, admin, true); !errors.Is(err, state.ErrLastAdmin) {
		t.Fatalf("disabling the last administrator returned %v", err)
	}
	if err := d.DeleteAccount(ctx, admin); !errors.Is(err, state.ErrLastAdmin) {
		t.Fatalf("deleting the last administrator returned %v", err)
	}

	second := newAccount(t, d, "admin2", state.RoleAdmin)
	if err := d.SetAccountDisabled(ctx, admin, true); err != nil {
		t.Fatalf("disabling one of two administrators: %v", err)
	}
	// With the first one disabled, the second is now the last: a disabled
	// administrator cannot re-enable anything, so it does not count.
	if err := d.DeleteAccount(ctx, second); !errors.Is(err, state.ErrLastAdmin) {
		t.Fatalf("deleting the last active administrator returned %v", err)
	}
}

func TestQuotaIsSetAndCleared(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	id := newAccount(t, d, "alice", 0)

	cap100 := int64(100)
	if err := d.SetAccountQuota(ctx, id, &cap100); err != nil {
		t.Fatalf("SetAccountQuota: %v", err)
	}
	acct, err := d.AccountByID(ctx, id)
	if err != nil {
		t.Fatalf("AccountByID: %v", err)
	}
	if acct.QuotaBytes == nil || *acct.QuotaBytes != 100 {
		t.Fatalf("the quota read back as %v", acct.QuotaBytes)
	}
	if err = d.SetAccountQuota(ctx, id, nil); err != nil {
		t.Fatalf("clearing the quota: %v", err)
	}
	if acct, err = d.AccountByID(ctx, id); err != nil {
		t.Fatalf("AccountByID: %v", err)
	}
	if acct.QuotaBytes != nil {
		t.Fatalf("a cleared quota read back as %v, want unlimited", *acct.QuotaBytes)
	}
}

// Opting out and being enabled are two facts. Collapsing them into one left
// the opt-out column unwritable, so the screen's own toggle never survived a
// reload.
func TestBothSMBSwitchesArePersisted(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	id := newAccount(t, d, "alice", 0)

	if err := d.SetAccountSMBAccess(ctx, id, true, false); err != nil {
		t.Fatalf("SetAccountSMBAccess: %v", err)
	}
	acct, err := d.AccountByID(ctx, id)
	if err != nil {
		t.Fatalf("AccountByID: %v", err)
	}
	if !acct.SMBOptOut || acct.SMBEnabled {
		t.Fatalf("the switches read back as opt-out %v, enabled %v", acct.SMBOptOut, acct.SMBEnabled)
	}
}

func TestGroupsAndMemberships(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	alice := newAccount(t, d, "alice", 0)

	id, err := d.CreateGroup(ctx, "staff")
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	if _, err = d.CreateGroup(ctx, "staff"); !errors.Is(err, state.ErrNameTaken) {
		t.Fatalf("a duplicate group name returned %v", err)
	}

	if err = d.AddMembership(ctx, alice, id); err != nil {
		t.Fatalf("AddMembership: %v", err)
	}
	// Adding twice is not an error: the caller asked for a state, and the
	// state is reached either way.
	if err = d.AddMembership(ctx, alice, id); err != nil {
		t.Fatalf("a repeated AddMembership: %v", err)
	}
	groups, err := d.GroupIDsOf(ctx, alice)
	if err != nil {
		t.Fatalf("GroupIDsOf: %v", err)
	}
	if len(groups) != 1 || groups[0] != id {
		t.Fatalf("the memberships read back as %v", groups)
	}

	if err = d.RenameGroup(ctx, id, "team"); err != nil {
		t.Fatalf("RenameGroup: %v", err)
	}
	if err = d.RenameGroup(ctx, 404, "team"); !errors.Is(err, state.ErrNoSuchGroup) {
		t.Fatalf("renaming a missing group returned %v", err)
	}

	// The rename moved the label and touched no membership, which is the
	// whole point of a membership naming an id.
	if groups, err = d.GroupIDsOf(ctx, alice); err != nil || len(groups) != 1 {
		t.Fatalf("after the rename the memberships are %v, %v", groups, err)
	}

	if err = d.DeleteGroup(ctx, id); err != nil {
		t.Fatalf("DeleteGroup: %v", err)
	}
	if groups, err = d.GroupIDsOf(ctx, alice); err != nil || len(groups) != 0 {
		t.Fatalf("the membership survived the group: %v, %v", groups, err)
	}
}

func TestSetMembershipsReplacesTheWholeSet(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	alice := newAccount(t, d, "alice", 0)
	one, err := d.CreateGroup(ctx, "one")
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	two, err := d.CreateGroup(ctx, "two")
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}

	if err = d.SetMemberships(ctx, alice, []int64{one, two}); err != nil {
		t.Fatalf("SetMemberships: %v", err)
	}
	if err = d.SetMemberships(ctx, alice, []int64{two}); err != nil {
		t.Fatalf("SetMemberships: %v", err)
	}
	groups, err := d.GroupIDsOf(ctx, alice)
	if err != nil {
		t.Fatalf("GroupIDsOf: %v", err)
	}
	if len(groups) != 1 || groups[0] != two {
		t.Fatalf("the replaced set reads as %v", groups)
	}
}

// A membership naming a group that does not exist is refused by the foreign
// key, and the whole replacement rolls back rather than landing half of it.
func TestAMembershipSetNamingAMissingGroupChangesNothing(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	alice := newAccount(t, d, "alice", 0)
	one, err := d.CreateGroup(ctx, "one")
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	if err = d.SetMemberships(ctx, alice, []int64{one}); err != nil {
		t.Fatalf("SetMemberships: %v", err)
	}
	if err = d.SetMemberships(ctx, alice, []int64{one, 404}); err == nil {
		t.Fatal("a set naming a missing group was accepted")
	}
	groups, err := d.GroupIDsOf(ctx, alice)
	if err != nil {
		t.Fatalf("GroupIDsOf: %v", err)
	}
	if len(groups) != 1 || groups[0] != one {
		t.Fatalf("the failed replacement left %v", groups)
	}
}

func TestTheSetupGateReadsCountsAndAdminExistence(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)

	n, err := d.CountAccounts(ctx)
	if err != nil || n != 0 {
		t.Fatalf("a fresh deployment counted %d accounts, %v", n, err)
	}
	has, err := d.AdminExists(ctx)
	if err != nil || has {
		t.Fatalf("a fresh deployment reported an administrator: %v, %v", has, err)
	}

	newAccount(t, d, "alice", 0)
	if has, err = d.AdminExists(ctx); err != nil || has {
		t.Fatalf("an ordinary account reported as an administrator: %v, %v", has, err)
	}
	newAccount(t, d, "admin", state.RoleAdmin)
	if has, err = d.AdminExists(ctx); err != nil || !has {
		t.Fatalf("an administrator was not seen: %v, %v", has, err)
	}
	if n, err = d.CountAccounts(ctx); err != nil || n != 2 {
		t.Fatalf("the count is %d, %v", n, err)
	}
}
