package state_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/task"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/dbfile"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/state"
)

func str(v string) *string { return &v }

func u32(v uint32) *uint32 { return &v }

func seedLinkOwner(t *testing.T, d *state.DB) int64 {
	t.Helper()
	seedUser(t, d, 1, "owner")
	return 1
}

func TestLinkRoundTripsEveryColumn(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	owner := seedLinkOwner(t, d)

	want := state.LinkRow{
		TokenHash: []byte{1, 2, 3}, TokenEnc: []byte{4, 5}, TokenKeyVer: u32(2),
		Share: 7, Path: "docs/report.pdf",
		Dev: id64(66306), Ino: id64(12345), Btime: id64(-42),
		Owner: owner, Perms: 0b1011,
		PasswordHash: str("$argon2id$..."), ExpiresNs: id64(1 << 40),
		MaxDown: id64(5), Label: str("for review"), Note: str("expires friday"),
		CreatedNs: 1_700_000_000,
	}
	id, err := d.Insert(ctx, want)
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}

	got, ok, err := d.ByID(ctx, id)
	if err != nil || !ok {
		t.Fatalf("ByID: %v (found %v)", err, ok)
	}
	want.ID, want.Downloads = id, 0
	if string(got.TokenHash) != string(want.TokenHash) ||
		string(got.TokenEnc) != string(want.TokenEnc) {
		t.Errorf("the token columns came back as %v / %v", got.TokenHash, got.TokenEnc)
	}
	if got.TokenKeyVer == nil || *got.TokenKeyVer != 2 {
		t.Errorf("the key version came back as %v", got.TokenKeyVer)
	}
	if got.Share != want.Share || got.Path != want.Path || got.Owner != want.Owner {
		t.Errorf("the target came back as share %d, path %q, owner %d", got.Share, got.Path, got.Owner)
	}
	if got.Dev == nil || *got.Dev != 66306 || got.Ino == nil || *got.Ino != 12345 ||
		got.Btime == nil || *got.Btime != -42 {
		t.Errorf("the pin came back as dev %v, ino %v, btime %v", got.Dev, got.Ino, got.Btime)
	}
	if got.Perms != want.Perms || got.Downloads != 0 || got.CreatedNs != want.CreatedNs {
		t.Errorf("read back %+v", got)
	}
	for name, pair := range map[string][2]*string{
		"password": {got.PasswordHash, want.PasswordHash},
		"label":    {got.Label, want.Label},
		"note":     {got.Note, want.Note},
	} {
		if pair[0] == nil || *pair[0] != *pair[1] {
			t.Errorf("%s came back as %v, want %q", name, pair[0], *pair[1])
		}
	}
	if got.ExpiresNs == nil || *got.ExpiresNs != 1<<40 || got.MaxDown == nil || *got.MaxDown != 5 {
		t.Errorf("the caps came back as expiry %v, max %v", got.ExpiresNs, got.MaxDown)
	}
}

// A link against a share root carries no pin and no optional column, and
// every nullable one has to come back nil rather than zero.
func TestALinkWithNoOptionalColumnsRoundTrips(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	owner := seedLinkOwner(t, d)

	id, err := d.Insert(ctx, state.LinkRow{
		TokenHash: []byte{9}, Share: 1, Path: "", Owner: owner, Perms: 1, CreatedNs: 1,
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	got, ok, err := d.ByID(ctx, id)
	if err != nil || !ok {
		t.Fatalf("ByID: %v (found %v)", err, ok)
	}
	for name, v := range map[string]any{
		"TokenEnc": got.TokenEnc, "TokenKeyVer": got.TokenKeyVer,
		"Dev": got.Dev, "Ino": got.Ino, "Btime": got.Btime,
		"PasswordHash": got.PasswordHash, "ExpiresNs": got.ExpiresNs,
		"MaxDown": got.MaxDown, "Label": got.Label, "Note": got.Note,
	} {
		switch typed := v.(type) {
		case []byte:
			if len(typed) != 0 {
				t.Errorf("%s came back as %v, want empty", name, typed)
			}
		case *int64:
			if typed != nil {
				t.Errorf("%s came back as %d, want nil", name, *typed)
			}
		case *uint32:
			if typed != nil {
				t.Errorf("%s came back as %d, want nil", name, *typed)
			}
		case *string:
			if typed != nil {
				t.Errorf("%s came back as %q, want nil", name, *typed)
			}
		}
	}
}

func TestByHashFindsTheSameRowAsByID(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	owner := seedLinkOwner(t, d)

	hash := []byte{7, 7, 7}
	id, err := d.Insert(ctx, state.LinkRow{
		TokenHash: hash, Share: 1, Path: "p", Owner: owner, Perms: 1, CreatedNs: 1,
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	got, ok, err := d.ByHash(ctx, hash)
	if err != nil || !ok {
		t.Fatalf("ByHash: %v (found %v)", err, ok)
	}
	if got.ID != id {
		t.Errorf("ByHash found link %d, want %d", got.ID, id)
	}
}

// Nothing matching is (row, false, nil): mapping that to a not-found error
// belongs one layer up.
func TestAMissingLinkIsNotAnError(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)

	if _, ok, err := d.ByID(ctx, 4242); err != nil || ok {
		t.Errorf("ByID of an unknown id: %v (found %v)", err, ok)
	}
	if _, ok, err := d.ByHash(ctx, []byte{9, 9}); err != nil || ok {
		t.Errorf("ByHash of an unknown hash: %v (found %v)", err, ok)
	}
}

func TestListByOwnerIsScopedAndOrdered(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	seedUser(t, d, 1, "one")
	seedUser(t, d, 2, "two")

	var mine []int64
	for i := range 3 {
		id, err := d.Insert(ctx, state.LinkRow{
			TokenHash: []byte{byte(i)}, Share: 1, Path: "p", Owner: 1, Perms: 1, CreatedNs: 1,
		})
		if err != nil {
			t.Fatalf("Insert: %v", err)
		}
		mine = append(mine, id)
	}
	if _, err := d.Insert(ctx, state.LinkRow{
		TokenHash: []byte{99}, Share: 1, Path: "p", Owner: 2, Perms: 1, CreatedNs: 1,
	}); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	got, err := d.ListByOwner(ctx, 1)
	if err != nil {
		t.Fatalf("ListByOwner: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("%d links, want 3", len(got))
	}
	for i, row := range got {
		if row.Owner != 1 {
			t.Errorf("another account's link appeared: %+v", row)
		}
		if row.ID != mine[i] {
			t.Errorf("link %d of the listing is %d, want %d (id order)", i, row.ID, mine[i])
		}
	}
}

// The ownership check and the delete are one statement, so the two cannot
// disagree about who owns the row.
func TestDeleteRequiresBothIDAndOwner(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	seedUser(t, d, 1, "one")
	seedUser(t, d, 2, "two")

	id, err := d.Insert(ctx, state.LinkRow{
		TokenHash: []byte{1}, Share: 1, Path: "p", Owner: 1, Perms: 1, CreatedNs: 1,
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}

	// The wrong owner removes nothing.
	if err := d.Delete(ctx, id, 2); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok, err := d.ByID(ctx, id); err != nil || !ok {
		t.Fatalf("another account's delete removed the link: %v (found %v)", err, ok)
	}

	if err := d.Delete(ctx, id, 1); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok, err := d.ByID(ctx, id); err != nil || ok {
		t.Errorf("the owner's delete left the link: %v (found %v)", err, ok)
	}
}

// The cap check and the increment are one statement, so a cap of one admits
// exactly one download however many callers race for it.
func TestConsumeDownloadHonorsTheCapUnderConcurrency(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	owner := seedLinkOwner(t, d)

	id, err := d.Insert(ctx, state.LinkRow{
		TokenHash: []byte{1}, Share: 1, Path: "p", Owner: owner, Perms: 1,
		MaxDown: id64(3), CreatedNs: 1,
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}

	var (
		consumed atomic.Int64
		wg       sync.WaitGroup
	)
	errs := make([]error, 16)
	for i := range errs {
		wg.Add(1)
		task.Go(ctx, "link: racing download", func() {
			defer wg.Done()
			ok, cerr := d.ConsumeDownload(ctx, id)
			errs[i] = cerr
			if ok {
				consumed.Add(1)
			}
		})
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("download %d: %v", i, err)
		}
	}
	if got := consumed.Load(); got != 3 {
		t.Errorf("%d downloads were admitted against a cap of 3", got)
	}

	row, ok, err := d.ByID(ctx, id)
	if err != nil || !ok {
		t.Fatalf("ByID: %v (found %v)", err, ok)
	}
	if row.Downloads != 3 {
		t.Errorf("the counter reads %d, want 3", row.Downloads)
	}
}

// An uncapped link never refuses, and a gone row reads the same as a reached
// cap: the caller disambiguates with ByID.
func TestConsumeDownloadWithoutACapAndOnAMissingRow(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	owner := seedLinkOwner(t, d)

	id, err := d.Insert(ctx, state.LinkRow{
		TokenHash: []byte{1}, Share: 1, Path: "p", Owner: owner, Perms: 1, CreatedNs: 1,
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	for range 5 {
		if ok, err := d.ConsumeDownload(ctx, id); err != nil || !ok {
			t.Fatalf("an uncapped download: %v (consumed %v)", err, ok)
		}
	}

	if ok, err := d.ConsumeDownload(ctx, 4242); err != nil || ok {
		t.Errorf("a download against a missing row: %v (consumed %v)", err, ok)
	}
}

func TestPasswordHashReadsOneColumn(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	owner := seedLinkOwner(t, d)

	bare, err := d.Insert(ctx, state.LinkRow{
		TokenHash: []byte{1}, Share: 1, Path: "p", Owner: owner, Perms: 1, CreatedNs: 1,
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	locked, err := d.Insert(ctx, state.LinkRow{
		TokenHash: []byte{2}, Share: 1, Path: "p", Owner: owner, Perms: 1,
		PasswordHash: str("hashed"), CreatedNs: 1,
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}

	switch got, perr := d.PasswordHash(ctx, bare); {
	case perr != nil:
		t.Fatalf("PasswordHash: %v", perr)
	case got != nil:
		t.Errorf("a link with no password reports %q", *got)
	}
	got, err := d.PasswordHash(ctx, locked)
	if err != nil {
		t.Fatalf("PasswordHash: %v", err)
	}
	if got == nil || *got != "hashed" {
		t.Errorf("the stored hash reads back as %v", got)
	}
	if got, err := d.PasswordHash(ctx, 4242); err != nil || got != nil {
		t.Errorf("an unknown link's password: %v (%v)", err, got)
	}
}

// An outer nil leaves the column, an inner nil sets it NULL.
func TestUpdateAppliesOnlyThePresentFields(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	owner := seedLinkOwner(t, d)

	id, err := d.Insert(ctx, state.LinkRow{
		TokenHash: []byte{1}, Share: 1, Path: "p", Owner: owner, Perms: 0b0001,
		PasswordHash: str("old"), ExpiresNs: id64(100), MaxDown: id64(3),
		Label: str("before"), Note: str("note"), CreatedNs: 1,
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}

	// Only the permissions move.
	wide := uint16(0b1111)
	if uerr := d.Update(ctx, id, state.LinkRowPatch{Perms: &wide}); uerr != nil {
		t.Fatalf("Update: %v", uerr)
	}
	got, _, err := d.ByID(ctx, id)
	if err != nil {
		t.Fatalf("ByID: %v", err)
	}
	if got.Perms != 0b1111 {
		t.Errorf("perms are %b, want 1111", got.Perms)
	}
	if got.PasswordHash == nil || *got.PasswordHash != "old" || got.ExpiresNs == nil {
		t.Errorf("a permissions-only patch moved something else: %+v", got)
	}

	// An inner nil clears.
	var noPassword *string
	var noExpiry *int64
	if uerr := d.Update(ctx, id, state.LinkRowPatch{
		PasswordHash: &noPassword,
		ExpiresNs:    &noExpiry,
	}); uerr != nil {
		t.Fatalf("Update: %v", uerr)
	}
	got, _, err = d.ByID(ctx, id)
	if err != nil {
		t.Fatalf("ByID: %v", err)
	}
	if got.PasswordHash != nil {
		t.Errorf("the password survived being cleared: %q", *got.PasswordHash)
	}
	if got.ExpiresNs != nil {
		t.Errorf("the expiry survived being cleared: %d", *got.ExpiresNs)
	}
	if got.MaxDown == nil || *got.MaxDown != 3 {
		t.Errorf("clearing two fields moved the download cap: %v", got.MaxDown)
	}
	if got.Label == nil || *got.Label != "before" {
		t.Errorf("clearing two fields moved the label: %v", got.Label)
	}
}

// A partial pin reads as no pin, which makes the link stop working rather
// than match a file it was never made against. The schema refuses to store
// one, and the scan refuses to interpret one.
func TestAPartialPinCannotBeStored(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	seedLinkOwner(t, d)

	// The CHECK constraint refuses a dev with no ino.
	err := d.Write(ctx, func(tx *sql.Tx) error {
		_, xerr := tx.ExecContext(ctx,
			`INSERT INTO share_link(token_hash, share, path, dev, ino, btime_present, btime_ns,
			                        owner, perms, created_ns)
			 VALUES (X'01', 1, 'p', 5, NULL, NULL, NULL, 1, 1, 1)`)
		return xerr
	})
	if err == nil {
		t.Fatal("a link with half an identity pin was stored")
	}

	// And a birth time marked absent beside a set dev and ino.
	err = d.Write(ctx, func(tx *sql.Tx) error {
		_, xerr := tx.ExecContext(ctx,
			`INSERT INTO share_link(token_hash, share, path, dev, ino, btime_present, btime_ns,
			                        owner, perms, created_ns)
			 VALUES (X'02', 1, 'p', 5, 6, 0, 0, 1, 1, 1)`)
		return xerr
	})
	if err == nil {
		t.Fatal("a link pinned to a file with no birth time was stored")
	}
}

// The migration's precondition names the offending link, because a
// constraint failure names the constraint and an operator needs the row.
func TestTheMigrationRefusesAMalformedPinByName(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")

	// Open at version 1, where the four identity columns were unconstrained.
	spec := state.Spec(path)
	spec.Migrations = spec.Migrations[:1]
	old, err := dbfile.Open(ctx, spec)
	if err != nil {
		t.Fatalf("opening at version 1: %v", err)
	}
	if werr := old.Write(ctx, func(tx *sql.Tx) error {
		if _, xerr := tx.ExecContext(ctx,
			`INSERT INTO user(id, name, pw_hash, created_ns) VALUES (1, 'u', '', 0)`); xerr != nil {
			return xerr
		}
		_, xerr := tx.ExecContext(ctx,
			`INSERT INTO share_link(id, token_hash, share, path, dev, ino, btime_present, btime_ns,
			                        owner, perms, created_ns)
			 VALUES (77, X'01', 1, 'p', 5, NULL, NULL, NULL, 1, 1, 1)`)
		return xerr
	}); werr != nil {
		t.Fatalf("planting the malformed link: %v", werr)
	}
	if cerr := old.Close(); cerr != nil {
		t.Fatalf("closing: %v", cerr)
	}

	_, err = dbfile.Open(ctx, state.Spec(path))
	if !errors.Is(err, state.ErrLinkTargetMalformed) {
		t.Fatalf("the migration returned %v, want ErrLinkTargetMalformed", err)
	}
	if !errors.Is(err, dbfile.ErrMigrationFailed) {
		t.Errorf("the refusal is not reported as a migration failure: %v", err)
	}
	if !strings.Contains(err.Error(), "77") {
		t.Errorf("the refusal does not name the link: %v", err)
	}
}

// A deployment that has never sealed a token has no row, which is version
// zero rather than an error.
func TestKeyVersionDefaultsToZero(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)

	switch ver, err := d.KeyVersion(ctx); {
	case err != nil:
		t.Fatalf("KeyVersion: %v", err)
	case ver != 0:
		t.Errorf("a fresh database reports key version %d, want 0", ver)
	}

	if err := d.Write(ctx, func(tx *sql.Tx) error {
		_, xerr := tx.ExecContext(ctx, `INSERT INTO key_version(id, ver) VALUES (1, 4)`)
		return xerr
	}); err != nil {
		t.Fatalf("seeding the key version: %v", err)
	}
	ver, err := d.KeyVersion(ctx)
	if err != nil {
		t.Fatalf("KeyVersion: %v", err)
	}
	if ver != 4 {
		t.Errorf("key version %d, want 4", ver)
	}
}

// A stored value that no longer fits is a corrupt row, which is worth saying
// rather than truncating into a different set of permissions.
func TestAStoredValueThatDoesNotFitErrorsTheRead(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	seedLinkOwner(t, d)

	if err := d.Write(ctx, func(tx *sql.Tx) error {
		_, xerr := tx.ExecContext(ctx,
			`INSERT INTO share_link(id, token_hash, share, path, owner, perms, created_ns)
			 VALUES (1, X'01', 1, 'p', 1, 70000, 1)`)
		return xerr
	}); err != nil {
		t.Fatalf("planting the row: %v", err)
	}

	if _, _, err := d.ByID(ctx, 1); err == nil {
		t.Fatal("a link carrying perms past the column width read back without complaint")
	}
}

// Deleting the owner takes their links with it.
func TestDeletingTheOwnerCascadesToTheirLinks(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	owner := seedLinkOwner(t, d)

	if _, err := d.Insert(ctx, state.LinkRow{
		TokenHash: []byte{1}, Share: 1, Path: "p", Owner: owner, Perms: 1, CreatedNs: 1,
	}); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := d.Write(ctx, func(tx *sql.Tx) error {
		_, xerr := tx.ExecContext(ctx, `DELETE FROM user WHERE id = ?`, owner)
		return xerr
	}); err != nil {
		t.Fatalf("deleting the owner: %v", err)
	}

	got, err := d.ListByOwner(ctx, owner)
	if err != nil {
		t.Fatalf("ListByOwner: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("%d links outlived their owner", len(got))
	}
}
