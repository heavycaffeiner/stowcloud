package state_test

import (
	"context"
	"errors"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/store/state"
)

func startFlow(t *testing.T, d *state.DB, user int64, stateDigest, binding []byte, createdNs int64) {
	t.Helper()
	if err := d.StartOIDCFlow(context.Background(), state.NewOIDCFlow{
		StateDigest:   stateDigest,
		User:          user,
		Nonce:         "nonce",
		BindingDigest: binding,
		CodeVerifier:  "verifier",
		RedirectURI:   "https://example.test/cb",
		ReturnTo:      "/files",
		CreatedNs:     createdNs,
	}, 0); err != nil {
		t.Fatalf("StartOIDCFlow: %v", err)
	}
}

// A flow that can be redeemed twice is a code that can be replayed, and the
// exchange is the only thing it is for.
func TestTakingAFlowConsumesIt(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	user := newAccount(t, d, "alice", 0)
	startFlow(t, d, user, []byte("state"), []byte("binding"), 100)

	flow, err := d.TakeOIDCFlow(ctx, []byte("state"), 0)
	if err != nil {
		t.Fatalf("TakeOIDCFlow: %v", err)
	}
	if flow.User != user || flow.Nonce != "nonce" || flow.CodeVerifier != "verifier" {
		t.Fatalf("the flow read back as %+v", flow)
	}
	if flow.RedirectURI != "https://example.test/cb" || flow.ReturnTo != "/files" {
		t.Fatalf("the flow read back as %+v", flow)
	}
	if _, err = d.TakeOIDCFlow(ctx, []byte("state"), 0); !errors.Is(err, state.ErrNoOIDCFlow) {
		t.Fatalf("the second take returned %v", err)
	}
}

// An unknown state and an expired one are one answer, because telling them
// apart would say whether a state value was ever real.
func TestAnExpiredFlowAnswersLikeAnUnknownOne(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	user := newAccount(t, d, "alice", 0)
	startFlow(t, d, user, []byte("state"), []byte("binding"), 100)

	if _, err := d.TakeOIDCFlow(ctx, []byte("state"), 500); !errors.Is(err, state.ErrNoOIDCFlow) {
		t.Fatalf("an expired flow returned %v", err)
	}
	if _, err := d.TakeOIDCFlow(ctx, []byte("never"), 0); !errors.Is(err, state.ErrNoOIDCFlow) {
		t.Fatalf("an unknown flow returned %v", err)
	}
}

// Starting one sweeps what has expired, so a deployment nobody links on
// accumulates nothing and there is no timer to forget.
func TestStartingAFlowSweepsTheExpiredOnes(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	user := newAccount(t, d, "alice", 0)
	startFlow(t, d, user, []byte("old"), []byte("b"), 10)

	if err := d.StartOIDCFlow(ctx, state.NewOIDCFlow{
		StateDigest: []byte("new"), User: user, Nonce: "n",
		BindingDigest: []byte("b"), CodeVerifier: "v", CreatedNs: 1000,
	}, 500); err != nil {
		t.Fatalf("StartOIDCFlow: %v", err)
	}
	if _, err := d.TakeOIDCFlow(ctx, []byte("old"), 0); !errors.Is(err, state.ErrNoOIDCFlow) {
		t.Fatalf("the expired flow survived the sweep: %v", err)
	}
}

// Both values go to the browser, so storing them whole would make a read of
// this table enough to complete somebody else's link.
func TestTheFlowStoresDigestsAndNotTheValues(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	user := newAccount(t, d, "alice", 0)
	startFlow(t, d, user, []byte("state-digest"), []byte("binding-digest"), 1)

	var stateCol, bindCol []byte
	if err := d.SQL().QueryRowContext(ctx,
		`SELECT state_digest, binding_digest FROM oidc_flow`).Scan(&stateCol, &bindCol); err != nil {
		t.Fatalf("reading the row: %v", err)
	}
	if string(stateCol) != "state-digest" || string(bindCol) != "binding-digest" {
		t.Fatalf("the columns hold %q and %q", stateCol, bindCol)
	}

	// The verifier and the nonce rest whole, because both have to be handed
	// back out and neither authenticates anything on its own.
	var nonce, verifier string
	if err := d.SQL().QueryRowContext(ctx,
		`SELECT nonce, code_verifier FROM oidc_flow`).Scan(&nonce, &verifier); err != nil {
		t.Fatalf("reading the row: %v", err)
	}
	if nonce != "nonce" || verifier != "verifier" {
		t.Fatalf("the nonce and verifier read back as %q and %q", nonce, verifier)
	}
}

func TestAnIdentityLinkRoundTripsAndReplaces(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	user := newAccount(t, d, "alice", 0)

	if err := d.CreateOIDCLink(ctx, user, "https://idp", "subject-1", 100); err != nil {
		t.Fatalf("CreateOIDCLink: %v", err)
	}
	link, err := d.OIDCLinkOf(ctx, user)
	if err != nil {
		t.Fatalf("OIDCLinkOf: %v", err)
	}
	if link.Issuer != "https://idp" || link.Subject != "subject-1" || link.LinkedNs != 100 {
		t.Fatalf("the link read back as %+v", link)
	}
	if link.LastLoginNs != nil {
		t.Fatalf("a link never used to sign in reports a stamp of %d", *link.LastLoginNs)
	}

	// Re-linking replaces, so a provider migration works with no separate
	// unlink: the identity is the primary key, and an update would leave the
	// old row linked.
	if err = d.CreateOIDCLink(ctx, user, "https://idp", "subject-2", 200); err != nil {
		t.Fatalf("re-linking: %v", err)
	}
	if _, err = d.UserForOIDCIdentity(ctx, "https://idp", "subject-1"); !errors.Is(err, state.ErrNoOIDCLink) {
		t.Fatalf("the replaced identity still resolves: %v", err)
	}
	got, err := d.UserForOIDCIdentity(ctx, "https://idp", "subject-2")
	if err != nil || got != user {
		t.Fatalf("the new identity resolves to %d, %v", got, err)
	}
}

// Claiming an identity owned by someone else would transfer that account's sole
// means of access to a different person.
func TestATakenIdentityIsRefused(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	alice := newAccount(t, d, "alice", 0)
	bob := newAccount(t, d, "bob", 0)

	if err := d.CreateOIDCLink(ctx, alice, "https://idp", "shared", 1); err != nil {
		t.Fatalf("CreateOIDCLink: %v", err)
	}
	if err := d.CreateOIDCLink(ctx, bob, "https://idp", "shared", 2); !errors.Is(err, state.ErrOIDCLinkTaken) {
		t.Fatalf("claiming another account's identity returned %v", err)
	}
	got, err := d.UserForOIDCIdentity(ctx, "https://idp", "shared")
	if err != nil || got != alice {
		t.Fatalf("the identity now resolves to %d, %v", got, err)
	}
}

func TestTouchingALinkStampsIt(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	user := newAccount(t, d, "alice", 0)
	if err := d.CreateOIDCLink(ctx, user, "https://idp", "s", 1); err != nil {
		t.Fatalf("CreateOIDCLink: %v", err)
	}
	if err := d.TouchOIDCLink(ctx, "https://idp", "s", 500); err != nil {
		t.Fatalf("TouchOIDCLink: %v", err)
	}
	link, err := d.OIDCLinkOf(ctx, user)
	if err != nil {
		t.Fatalf("OIDCLinkOf: %v", err)
	}
	if link.LastLoginNs == nil || *link.LastLoginNs != 500 {
		t.Fatalf("the stamp read back as %v", link.LastLoginNs)
	}
}

func TestUnlinkingDetachesTheIdentity(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	user := newAccount(t, d, "alice", 0)
	if err := d.CreateOIDCLink(ctx, user, "https://idp", "s", 1); err != nil {
		t.Fatalf("CreateOIDCLink: %v", err)
	}
	if err := d.DeleteOIDCLink(ctx, user); err != nil {
		t.Fatalf("DeleteOIDCLink: %v", err)
	}
	if _, err := d.OIDCLinkOf(ctx, user); !errors.Is(err, state.ErrNoOIDCLink) {
		t.Fatalf("the link survived the unlink: %v", err)
	}
	if _, err := d.UserForOIDCIdentity(ctx, "https://idp", "s"); !errors.Is(err, state.ErrNoOIDCLink) {
		t.Fatalf("the identity still resolves: %v", err)
	}
}

// The log outlives the accounts it names: who did this matters most for the
// account that no longer exists.
func TestTheAuditLogSurvivesTheAccountItNames(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	newAccount(t, d, "admin", state.RoleAdmin)
	newAccount(t, d, "admin2", state.RoleAdmin)
	user := newAccount(t, d, "alice", 0)

	if err := d.AppendAudit(ctx, state.AuditEntry{
		TsNs: 1, Actor: &user, Event: "login", IP: "192.0.2.1", UA: "client", OK: true,
	}); err != nil {
		t.Fatalf("AppendAudit: %v", err)
	}
	if err := d.DeleteAccount(ctx, user); err != nil {
		t.Fatalf("DeleteAccount: %v", err)
	}
	rows, err := d.AuditPage(ctx, 0, 10)
	if err != nil {
		t.Fatalf("AuditPage: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("the log holds %d rows after the deletion", len(rows))
	}
	if rows[0].Actor != nil {
		t.Fatalf("the actor reads as %d, want null after the cascade", *rows[0].Actor)
	}
	if rows[0].Event != "login" || !rows[0].OK {
		t.Fatalf("the row reads as %+v", rows[0])
	}
}

// A screen has to tell "no target" from "a target whose name is blank".
func TestAbsentAuditColumnsReadBackAsAbsent(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)

	if err := d.AppendAudit(ctx, state.AuditEntry{TsNs: 5, Event: "boot", OK: false}); err != nil {
		t.Fatalf("AppendAudit: %v", err)
	}
	rows, err := d.AuditPage(ctx, 0, 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("AuditPage returned %d rows, %v", len(rows), err)
	}
	r := rows[0]
	if r.Actor != nil || r.Target != nil || r.IP != nil || r.Detail != nil {
		t.Fatalf("the absent columns read back as %+v", r)
	}
	if r.UA != "" || r.OK {
		t.Fatalf("the row reads as %+v", r)
	}
}

// The cursor is the previous page's last rowid, so a boundary stays correct
// while new rows land ahead of it; an offset would shift every page.
func TestTheAuditCursorPagesWhileRowsLandAhead(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)

	for i := 1; i <= 6; i++ {
		if err := d.AppendAudit(ctx, state.AuditEntry{
			TsNs: int64(i), Event: "e", Target: string(rune('a' + i)), OK: true,
		}); err != nil {
			t.Fatalf("AppendAudit: %v", err)
		}
	}
	first, err := d.AuditPage(ctx, 0, 3)
	if err != nil || len(first) != 3 {
		t.Fatalf("the first page is %d rows, %v", len(first), err)
	}
	// Two more land between the pages.
	for i := 7; i <= 8; i++ {
		if err = d.AppendAudit(ctx, state.AuditEntry{TsNs: int64(i), Event: "e", OK: true}); err != nil {
			t.Fatalf("AppendAudit: %v", err)
		}
	}
	second, err := d.AuditPage(ctx, first[len(first)-1].RowID, 3)
	if err != nil || len(second) != 3 {
		t.Fatalf("the second page is %d rows, %v", len(second), err)
	}
	seen := map[int64]bool{}
	for _, r := range append(append([]state.AuditRecord{}, first...), second...) {
		if seen[r.RowID] {
			t.Fatalf("row %d appeared on both pages", r.RowID)
		}
		seen[r.RowID] = true
	}
	if second[0].RowID >= first[len(first)-1].RowID {
		t.Fatalf("the second page starts at %d, which is not before %d",
			second[0].RowID, first[len(first)-1].RowID)
	}
}

// One transaction over four tables: a row that will not open aborts it and
// changes nothing, so the database never names a version some of its rows
// were not brought to.
func TestResealWalksEveryKindAndRecordsTheVersion(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	user := newAccount(t, d, "alice", 0)

	if err := d.PutSMBSecret(ctx, user, state.SMBSecret{Ciphertext: []byte("nt"), KeyVer: 1}); err != nil {
		t.Fatalf("PutSMBSecret: %v", err)
	}
	if err := d.EnrollTOTP(ctx, user, state.TOTPSecret{Ciphertext: []byte("totp"), KeyVer: 1}, 1); err != nil {
		t.Fatalf("EnrollTOTP: %v", err)
	}
	// EnrollTOTP drops the SMB row, so it goes back after.
	if err := d.PutSMBSecret(ctx, user, state.SMBSecret{Ciphertext: []byte("nt"), KeyVer: 1}); err != nil {
		t.Fatalf("PutSMBSecret: %v", err)
	}
	if err := d.WriteConfigSecret(ctx, "oidc.client_secret",
		state.ConfigSecret{Value: []byte("cfg"), KeyVer: 1}); err != nil {
		t.Fatalf("WriteConfigSecret: %v", err)
	}
	if _, err := d.Insert(ctx, state.LinkRow{
		TokenHash: []byte("hash"), TokenEnc: []byte("enc"), TokenKeyVer: ptrU32(1),
		Share: 1, Path: "/f", Owner: user, Perms: 1, CreatedNs: 1,
	}); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	seen := map[state.SealedKind]int{}
	counts, err := d.Reseal(ctx, 2, func(row state.SealedRow) ([]byte, error) {
		seen[row.Kind]++
		if row.KeyVer != 1 {
			t.Errorf("a %s arrived at version %d", row.Kind, row.KeyVer)
		}
		return append([]byte("v2:"), row.Ciphertext...), nil
	})
	if err != nil {
		t.Fatalf("Reseal: %v", err)
	}
	if counts.SMB != 1 || counts.TOTP != 1 || counts.Links != 1 || counts.ConfigSecrets != 1 {
		t.Fatalf("the counts are %+v", counts)
	}

	ver, err := d.KeyVersionState(ctx)
	if err != nil || ver != 2 {
		t.Fatalf("the recorded version is %d, %v", ver, err)
	}
	sec, err := d.SMBSecretOf(ctx, user)
	if err != nil || string(sec.Ciphertext) != "v2:nt" || sec.KeyVer != 2 {
		t.Fatalf("the re-sealed credential is %+v, %v", sec, err)
	}
}

// A row that will not open aborts the whole transaction, so nothing is left
// at a version the database does not name.
func TestAFailedResealChangesNothing(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	user := newAccount(t, d, "alice", 0)
	if err := d.PutSMBSecret(ctx, user, state.SMBSecret{Ciphertext: []byte("nt"), KeyVer: 1}); err != nil {
		t.Fatalf("PutSMBSecret: %v", err)
	}
	if err := d.SetKeyVersion(ctx, 1); err != nil {
		t.Fatalf("SetKeyVersion: %v", err)
	}

	boom := errors.New("cannot open")
	if _, err := d.Reseal(ctx, 2, func(state.SealedRow) ([]byte, error) {
		return nil, boom
	}); !errors.Is(err, boom) {
		t.Fatalf("Reseal returned %v, want the caller's own error", err)
	}
	ver, err := d.KeyVersionState(ctx)
	if err != nil || ver != 1 {
		t.Fatalf("the version moved to %d, %v", ver, err)
	}
	sec, err := d.SMBSecretOf(ctx, user)
	if err != nil || string(sec.Ciphertext) != "nt" || sec.KeyVer != 1 {
		t.Fatalf("the credential moved to %+v, %v", sec, err)
	}
}

func TestAFreshDatabaseNamesNoKeyVersion(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)

	ver, err := d.KeyVersionState(ctx)
	if err != nil {
		t.Fatalf("KeyVersionState: %v", err)
	}
	if ver != state.MissingKeyVersion {
		t.Fatalf("a fresh database names version %d", ver)
	}
	if err = d.SetKeyVersion(ctx, 3); err != nil {
		t.Fatalf("SetKeyVersion: %v", err)
	}
	if ver, err = d.KeyVersionState(ctx); err != nil || ver != 3 {
		t.Fatalf("the version reads back as %d, %v", ver, err)
	}
}

// The startup check reads one row of each kind rather than walking the table,
// so a wrong key file is found before the first login rather than one failing
// account at a time.
func TestSampleSealedRowReportsPresenceAndAbsence(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	user := newAccount(t, d, "alice", 0)

	if _, found, err := d.SampleSealedRow(ctx, state.SealedTOTP); err != nil || found {
		t.Fatalf("an empty table reported found=%v, %v", found, err)
	}
	if err := d.EnrollTOTP(ctx, user, state.TOTPSecret{Ciphertext: []byte("s"), KeyVer: 4}, 1); err != nil {
		t.Fatalf("EnrollTOTP: %v", err)
	}
	row, found, err := d.SampleSealedRow(ctx, state.SealedTOTP)
	if err != nil || !found {
		t.Fatalf("SampleSealedRow reported found=%v, %v", found, err)
	}
	if row.User != user || row.KeyVer != 4 || string(row.Ciphertext) != "s" {
		t.Fatalf("the sample reads as %+v", row)
	}
}

func ptrU32(v uint32) *uint32 { return &v }
