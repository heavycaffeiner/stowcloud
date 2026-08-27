package state_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/store/dbfile"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/ident"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/state"
)

func open(t *testing.T) (*state.DB, *dbfile.DB) {
	t.Helper()
	f, err := dbfile.Open(context.Background(), state.Spec(filepath.Join(t.TempDir(), "state.db")))
	if err != nil {
		t.Fatalf("opening the state database: %v", err)
	}
	t.Cleanup(func() {
		if err := f.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return state.New(f), f
}

func btime(v int64) *int64 { return &v }

func id64(v int64) *int64 { return &v }

// seedUser inserts a user, since most aggregates reference one. Repeating it
// for the same id is a no-op, so a table test can seed per case.
func seedUser(t *testing.T, d *state.DB, id int64, name string) {
	t.Helper()
	if err := d.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(),
			`INSERT INTO user(id, name, pw_hash, created_ns) VALUES (?, ?, '', 0)
			 ON CONFLICT(id) DO NOTHING`, id, name)
		return err
	}); err != nil {
		t.Fatalf("seeding user %d: %v", id, err)
	}
}

func seedGroup(t *testing.T, d *state.DB, id int64, name string) {
	t.Helper()
	if err := d.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(),
			`INSERT INTO "group"(id, name) VALUES (?, ?)`, id, name)
		return err
	}); err != nil {
		t.Fatalf("seeding group %d: %v", id, err)
	}
}

// The migration runner refuses a discard against this database whatever the
// step names, because nothing rebuilds what it holds.
func TestTheStateDatabaseIsNotRebuildable(t *testing.T) {
	spec := state.Spec(filepath.Join(t.TempDir(), "state.db"))
	if spec.Rebuildable {
		t.Fatal("the state database declares itself rebuildable")
	}
	spec.Migrations = append(spec.Migrations,
		dbfile.Migration{Name: "a discard", Discard: true, SQL: `DELETE FROM user`})
	if _, err := dbfile.Open(context.Background(), spec); !errors.Is(err, dbfile.ErrMigrationFailed) {
		t.Fatalf("a discard against the state database returned %v, want a refusal", err)
	}
}

func TestEveryMigrationApplies(t *testing.T) {
	ctx := context.Background()
	_, f := open(t)

	v, err := f.Version(ctx)
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if v != len(state.Spec("x").Migrations) {
		t.Errorf("version %d after opening, want %d", v, len(state.Spec("x").Migrations))
	}

	// A handful of tables from different migrations, to prove the whole list
	// ran rather than only the first.
	for _, table := range []string{
		"user", "grant", "share_link", "dav_prop", "favorite", "fileid_override",
		"key_version", "share_definition", "operation", "upload_alias",
		"compat_kv", "oidc_flow", "operation_item", "upload_cache_settings",
		"config_secret",
	} {
		var n int
		if err := f.SQL().QueryRowContext(ctx,
			`SELECT count(*) FROM sqlite_schema WHERE type = 'table' AND name = ?`,
			table).Scan(&n); err != nil {
			t.Fatalf("looking for %s: %v", table, err)
		}
		if n != 1 {
			t.Errorf("table %s is missing after every migration ran", table)
		}
	}

	// Step 10 dropped these.
	for _, gone := range []string{"share_identity_override", "share_trash_override"} {
		var n int
		if err := f.SQL().QueryRowContext(ctx,
			`SELECT count(*) FROM sqlite_schema WHERE type = 'table' AND name = ?`,
			gone).Scan(&n); err != nil {
			t.Fatalf("looking for %s: %v", gone, err)
		}
		if n != 0 {
			t.Errorf("table %s survived the step that drops it", gone)
		}
	}
}

// The foreign keys the schema declares only enforce anything on a connection
// that ran the pragma, so the pragma is what this proves.
func TestForeignKeysAreEnforced(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)

	err := d.Write(ctx, func(tx *sql.Tx) error {
		_, xerr := tx.ExecContext(ctx,
			`INSERT INTO session(id_hash, user, created_ns, last_seen_ns, absolute_expiry_ns, amr)
			 VALUES (X'00', 4242, 0, 0, 0, 0)`)
		return xerr
	})
	if err == nil {
		t.Fatal("a session referencing a user that does not exist was accepted")
	}
}

// The guard gates growth and never recovery, so the two halves are one table
// test over the methods on each side of the rule.
func TestTheSizeGuardCoversEveryInsertPathAndNothingElse(t *testing.T) {
	ctx := context.Background()

	// Each case runs against its own database, since a refused write and a
	// completed one leave different state behind.
	gated := map[string]func(t *testing.T, d *state.DB) error{
		"InsertShare": func(_ *testing.T, d *state.DB) error {
			_, err := d.InsertShare(ctx, state.ShareRow{Name: "s", Host: "/h"}, 1)
			return err
		},
		"CreateOp": func(t *testing.T, d *state.DB) error {
			seedUser(t, d, 1, "u")
			_, err := d.CreateOp(ctx, 1, state.OpCopy, 1, 1, []string{"a"})
			return err
		},
		"PutLoginFlow": func(_ *testing.T, d *state.DB) error {
			return d.PutLoginFlow(ctx, state.LoginFlowRow{
				PollDigest: []byte{1}, LoginDigest: []byte{2}, CreatedNs: 1,
			})
		},
		"RecordFileIDs": func(_ *testing.T, d *state.DB) error {
			return d.RecordFileIDs(ctx, ident.Assignment{
				Ident: ident.Ident{Share: 1, Dev: 2, Ino: 3}, ID: 9,
			})
		},
		"MergeSettings": func(_ *testing.T, d *state.DB) error {
			return d.MergeSettings(ctx, "network", map[string]any{"bind": ":8080"})
		},
		"PersistGrant": func(t *testing.T, d *state.DB) error {
			seedUser(t, d, 1, "u")
			_, err := d.PersistGrant(ctx, state.GrantRow{
				User: id64(1), Share: 5, Allow: 1,
			}, 1)
			return err
		},
		"Insert (share link)": func(t *testing.T, d *state.DB) error {
			seedUser(t, d, 1, "u")
			_, err := d.Insert(ctx, state.LinkRow{
				TokenHash: []byte{1}, Share: 1, Path: "p", Owner: 1, Perms: 1,
			})
			return err
		},
		"SetFavorite": func(t *testing.T, d *state.DB) error {
			seedUser(t, d, 1, "u")
			return d.SetFavorite(ctx, 1, state.Favorite{
				Ident: ident.Ident{Share: 1, Dev: 2, Ino: 3}, Path: "p",
			}, true)
		},
		"SetDavProps": func(_ *testing.T, d *state.DB) error {
			return d.SetDavProps(ctx, ident.Ident{Share: 1, Dev: 2, Ino: 3},
				[]state.DavPropOp{{NS: "n", Name: "a", Value: "v"}})
		},
		"PutDavLock": func(t *testing.T, d *state.DB) error {
			seedUser(t, d, 1, "u")
			return d.PutDavLock(ctx, state.DavLock{
				Token: "t", Ident: ident.Ident{Share: 1, Dev: 2, Ino: 3},
				Path: "p", Principal: 1, ExpiresNs: 1 << 40,
			}, 0)
		},
		"WriteConfigSecret": func(_ *testing.T, d *state.DB) error {
			return d.WriteConfigSecret(ctx, "oidc", state.ConfigSecret{Value: []byte{1}, KeyVer: 0})
		},
		"WriteChunkSettings": func(_ *testing.T, d *state.DB) error {
			return d.WriteChunkSettings(ctx, 1<<20, 2<<20)
		},
		"WriteUploadCacheEnabled": func(_ *testing.T, d *state.DB) error {
			return d.WriteUploadCacheEnabled(ctx, true)
		},
		"TouchUploadDir": func(_ *testing.T, d *state.DB) error {
			return d.TouchUploadDir(ctx, 1, "/d")
		},
		"CreateUploadSession": func(t *testing.T, d *state.DB) error {
			seedUser(t, d, 1, "u")
			return d.CreateUploadSession(ctx, sampleSession([]byte{1}))
		},
	}

	for name, run := range gated {
		t.Run("gated/"+name, func(t *testing.T) {
			d, f := open(t)
			f.SetWritesBlocked(true)
			if err := run(t, d); !errors.Is(err, dbfile.ErrWritesBlocked) {
				t.Fatalf("%s under a tripped guard returned %v, want ErrWritesBlocked", name, err)
			}
			f.SetWritesBlocked(false)
			if err := run(t, d); err != nil {
				t.Errorf("%s with the guard cleared: %v", name, err)
			}
		})
	}

	// The ungated half seeds with the guard clear, then acts with it
	// tripped: what is under test is the act, not the fixture.
	type ungatedCase struct {
		seed func(t *testing.T, d *state.DB) int64
		act  func(t *testing.T, d *state.DB, seeded int64) error
	}
	ungated := map[string]ungatedCase{
		"UpdateShare": {
			seed: func(t *testing.T, d *state.DB) int64 { return seedShare(t, d) },
			act: func(_ *testing.T, d *state.DB, id int64) error {
				return d.UpdateShare(ctx, id, state.ShareRow{Name: "s2", Host: "/h2"})
			},
		},
		"DeleteShare": {
			seed: func(t *testing.T, d *state.DB) int64 { return seedShare(t, d) },
			act: func(_ *testing.T, d *state.DB, id int64) error {
				return d.DeleteShare(ctx, id, 1_000_000+id)
			},
		},
		"UpdateGrant": {
			seed: func(t *testing.T, d *state.DB) int64 { return seedGrant(t, d) },
			act: func(_ *testing.T, d *state.DB, id int64) error {
				return d.UpdateGrant(ctx, id, 3, 0, true, "label")
			},
		},
		"DeleteGrant": {
			seed: func(t *testing.T, d *state.DB) int64 { return seedGrant(t, d) },
			act:  func(_ *testing.T, d *state.DB, id int64) error { return d.DeleteGrant(ctx, id) },
		},
		"Quota.Reserve": {
			seed: func(t *testing.T, d *state.DB) int64 { seedUser(t, d, 1, "u"); return 1 },
			act: func(_ *testing.T, d *state.DB, user int64) error {
				_, err := state.NewQuota(d).Reserve(ctx, user, 100)
				return err
			},
		},
		"Quota.Release": {
			seed: func(t *testing.T, d *state.DB) int64 { seedUser(t, d, 1, "u"); return 1 },
			act: func(_ *testing.T, d *state.DB, user int64) error {
				return state.NewQuota(d).Release(ctx, user, 100)
			},
		},
		"SweepDavLocks": {
			act: func(_ *testing.T, d *state.DB, _ int64) error {
				_, err := d.SweepDavLocks(ctx, 1<<40)
				return err
			},
		},
		"SweepLoginFlows": {
			act: func(_ *testing.T, d *state.DB, _ int64) error {
				_, err := d.SweepLoginFlows(ctx, 1<<40)
				return err
			},
		},
		"DropDavProps": {
			act: func(_ *testing.T, d *state.DB, _ int64) error {
				return d.DropDavProps(ctx, ident.Ident{Share: 1, Dev: 2, Ino: 3})
			},
		},
		"DeleteConfigSecret": {
			act: func(_ *testing.T, d *state.DB, _ int64) error {
				return d.DeleteConfigSecret(ctx, "oidc")
			},
		},
		"SetFavorite (unstar)": {
			seed: func(t *testing.T, d *state.DB) int64 { seedUser(t, d, 1, "u"); return 1 },
			act: func(_ *testing.T, d *state.DB, user int64) error {
				return d.SetFavorite(ctx, user, state.Favorite{
					Ident: ident.Ident{Share: 1, Dev: 2, Ino: 3},
				}, false)
			},
		},
		"AdvanceUploadCacheMerged": {
			act: func(_ *testing.T, d *state.DB, _ int64) error {
				return d.AdvanceUploadCacheMerged(ctx, []byte{1}, 10)
			},
		},
	}

	for name, tc := range ungated {
		t.Run("ungated/"+name, func(t *testing.T) {
			d, f := open(t)
			var seeded int64
			if tc.seed != nil {
				seeded = tc.seed(t, d)
			}
			f.SetWritesBlocked(true)
			if err := tc.act(t, d, seeded); err != nil {
				t.Errorf("%s was refused under the guard: %v", name, err)
			}
		})
	}
}

func seedShare(t *testing.T, d *state.DB) int64 {
	t.Helper()
	id, err := d.InsertShare(context.Background(), state.ShareRow{Name: "s", Host: "/h"}, 1)
	if err != nil {
		t.Fatalf("seeding a share: %v", err)
	}
	return id
}

func seedGrant(t *testing.T, d *state.DB) int64 {
	t.Helper()
	seedUser(t, d, 1, "u")
	id, err := d.PersistGrant(context.Background(),
		state.GrantRow{User: id64(1), Share: 5, Allow: 1}, 1)
	if err != nil {
		t.Fatalf("seeding a grant: %v", err)
	}
	return id
}

func sampleSession(id []byte) state.UploadSession {
	return state.UploadSession{
		ID: id, User: 1, Share: 1, Dest: "d/f", PartName: ".part",
		ChunkSize: 1 << 20, ChunkMinAtCreation: 1 << 20,
		Filename: "f", CreatedNs: 1, ExpiresNs: 1 << 40,
	}
}

func TestSharesRoundTrip(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)

	want := state.ShareRow{
		Name: "docs", Host: "/srv/docs",
		SharedExternally: true, TrashEnabled: true, SymlinkPolicy: "within_share",
	}
	id, err := d.InsertShare(ctx, want, 4242)
	if err != nil {
		t.Fatalf("InsertShare: %v", err)
	}

	got, err := d.ListShares(ctx)
	if err != nil {
		t.Fatalf("ListShares: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("%d shares, want 1", len(got))
	}
	want.ID, want.Created = id, 4242
	if got[0] != want {
		t.Errorf("read back %+v, want %+v", got[0], want)
	}
}

// The identity round trip has to survive the full unsigned range, since that
// is what a real filesystem hands out.
func TestFavoritesRoundTripThroughIdent(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	seedUser(t, d, 1, "u")

	for _, f := range []state.Favorite{
		{Ident: ident.Ident{Share: 2, Dev: 1 << 63, Ino: ^uint64(0), Btime: btime(-7)}, Path: "a"},
		{Ident: ident.Ident{Share: 3, Dev: 5, Ino: 6}, Path: "b"},
		{Ident: ident.Ident{Share: 3, Dev: 5, Ino: 6, Btime: btime(0)}, Path: "c"},
	} {
		if err := d.SetFavorite(ctx, 1, f, true); err != nil {
			t.Fatalf("SetFavorite(%+v): %v", f.Ident, err)
		}
	}

	got, err := d.Favorites(ctx, 1)
	if err != nil {
		t.Fatalf("Favorites: %v", err)
	}
	// The absent and the zero birth time are different rows, which is the
	// whole reason the presence flag is stored.
	if len(got) != 3 {
		t.Fatalf("%d favorites, want 3", len(got))
	}
	for _, f := range got {
		switch f.Path {
		case "a":
			if !f.Ident.Equal(ident.Ident{
				Share: 2, Dev: 1 << 63, Ino: ^uint64(0), Btime: btime(-7),
			}) {
				t.Errorf("favorite a came back as %+v", f.Ident)
			}
		case "b":
			if f.Ident.Btime != nil {
				t.Errorf("favorite b grew a birth time of %d", *f.Ident.Btime)
			}
		case "c":
			if f.Ident.Btime == nil || *f.Ident.Btime != 0 {
				t.Errorf("favorite c lost its zero birth time")
			}
		}
	}
}

func TestUnstarRemovesExactlyOneRow(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	seedUser(t, d, 1, "u")

	keep := state.Favorite{Ident: ident.Ident{Share: 1, Dev: 1, Ino: 1, Btime: btime(1)}, Path: "keep"}
	drop := state.Favorite{Ident: ident.Ident{Share: 1, Dev: 1, Ino: 2, Btime: btime(2)}, Path: "drop"}
	for _, f := range []state.Favorite{keep, drop} {
		if err := d.SetFavorite(ctx, 1, f, true); err != nil {
			t.Fatalf("SetFavorite: %v", err)
		}
	}
	if err := d.SetFavorite(ctx, 1, drop, false); err != nil {
		t.Fatalf("unstarring: %v", err)
	}

	got, err := d.Favorites(ctx, 1)
	if err != nil {
		t.Fatalf("Favorites: %v", err)
	}
	if len(got) != 1 || got[0].Path != "keep" {
		t.Errorf("after unstarring one, got %d rows: %+v", len(got), got)
	}
}

func TestDavPropsAndLocksRoundTripThroughIdent(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	seedUser(t, d, 1, "u")

	id := ident.Ident{Share: 4, Dev: 1 << 63, Ino: ^uint64(0), Btime: btime(-1)}
	if err := d.SetDavProps(ctx, id, []state.DavPropOp{
		{NS: "DAV:", Name: "a", Value: "1"},
		{NS: "DAV:", Name: "b", Value: "2"},
	}); err != nil {
		t.Fatalf("SetDavProps: %v", err)
	}
	props, err := d.DavProps(ctx, id)
	if err != nil {
		t.Fatalf("DavProps: %v", err)
	}
	if len(props) != 2 {
		t.Fatalf("%d properties, want 2", len(props))
	}

	if rerr := d.SetDavProps(ctx, id,
		[]state.DavPropOp{{NS: "DAV:", Name: "a", Remove: true}}); rerr != nil {
		t.Fatalf("removing a property: %v", rerr)
	}
	props, err = d.DavProps(ctx, id)
	if err != nil {
		t.Fatalf("DavProps: %v", err)
	}
	if len(props) != 1 || props[0].Name != "b" {
		t.Errorf("after a removal, got %+v", props)
	}

	lock := state.DavLock{
		Token: "urn:uuid:1", Ident: id, Path: "d/f", Principal: 1,
		Owner: "somebody", Depth: 0, Scope: 1, ExpiresNs: 1 << 40, TimeoutS: 600,
	}
	if lerr := d.PutDavLock(ctx, lock, 0); lerr != nil {
		t.Fatalf("PutDavLock: %v", lerr)
	}
	locks, err := d.DavLocks(ctx, 0)
	if err != nil {
		t.Fatalf("DavLocks: %v", err)
	}
	if len(locks) != 1 {
		t.Fatalf("%d locks, want 1", len(locks))
	}
	if !locks[0].Ident.Equal(id) {
		t.Errorf("the lock's identity came back as %+v, want %+v", locks[0].Ident, id)
	}
	if locks[0].Owner != "somebody" || locks[0].TimeoutS != 600 {
		t.Errorf("the lock came back as %+v", locks[0])
	}
}

// A lock past its deadline is gone whether or not the sweep has run, so a
// reader never honors one.
func TestAnExpiredLockIsNeverRead(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	seedUser(t, d, 1, "u")

	id := ident.Ident{Share: 1, Dev: 1, Ino: 1}
	if err := d.PutDavLock(ctx, state.DavLock{
		Token: "t", Ident: id, Path: "p", Principal: 1, ExpiresNs: 100,
	}, 0); err != nil {
		t.Fatalf("PutDavLock: %v", err)
	}

	locks, err := d.DavLocks(ctx, 101)
	if err != nil {
		t.Fatalf("DavLocks: %v", err)
	}
	if len(locks) != 0 {
		t.Errorf("an expired lock was read back: %+v", locks)
	}

	// And the sweep then reclaims the row it was already ignoring.
	n, err := d.SweepDavLocks(ctx, 101)
	if err != nil {
		t.Fatalf("SweepDavLocks: %v", err)
	}
	if n != 1 {
		t.Errorf("the sweep removed %d rows, want 1", n)
	}
}

func TestRefreshingAnUnknownLockIsARefusal(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	if err := d.RefreshDavLock(ctx, "nothing", 1<<40, 600, 0); !errors.Is(err, state.ErrNoSuchLock) {
		t.Fatalf("refreshing an unknown lock returned %v, want ErrNoSuchLock", err)
	}
}

// A save is a patch: the fields the caller did not mention survive it, which
// is the regression a whole-section replace caused.
func TestMergeSettingsKeepsWhatTheCallerDidNotMention(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)

	if err := d.MergeSettings(ctx, "network", map[string]any{
		"bind": ":8443", "app_hosts": []any{"a"},
	}); err != nil {
		t.Fatalf("MergeSettings: %v", err)
	}
	if err := d.MergeSettings(ctx, "network", map[string]any{"app_hosts": []any{"b"}}); err != nil {
		t.Fatalf("MergeSettings: %v", err)
	}
	if err := d.MergeSettings(ctx, "search", map[string]any{"name_index_enabled": true}); err != nil {
		t.Fatalf("MergeSettings: %v", err)
	}

	all, err := d.Settings(ctx)
	if err != nil {
		t.Fatalf("Settings: %v", err)
	}
	network, ok := all["network"].(map[string]any)
	if !ok {
		t.Fatalf("the network section came back as %T", all["network"])
	}
	if network["bind"] != ":8443" {
		t.Errorf("the bind address became %v after a save that did not mention it", network["bind"])
	}
	if _, ok := all["search"]; !ok {
		t.Error("writing one section dropped another")
	}
}

func TestSearchSettingsDoNotDropEachOther(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)

	if err := d.SetIndexBuildRate(ctx, 1234); err != nil {
		t.Fatalf("SetIndexBuildRate: %v", err)
	}
	if err := d.SetIndexNameEnabled(ctx, true); err != nil {
		t.Fatalf("SetIndexNameEnabled: %v", err)
	}

	rate, err := d.IndexBuildRate(ctx)
	if err != nil {
		t.Fatalf("IndexBuildRate: %v", err)
	}
	if rate != 1234 {
		t.Errorf("the measured rate became %d after the switch was stored", rate)
	}
	on, err := d.IndexNameEnabled(ctx)
	if err != nil {
		t.Fatalf("IndexNameEnabled: %v", err)
	}
	if !on {
		t.Error("the switch did not survive")
	}
}

func TestUnsetSettingsReadAsAbsentRatherThanErroring(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)

	switch all, err := d.Settings(ctx); {
	case err != nil:
		t.Fatalf("Settings on a fresh database: %v", err)
	case len(all) != 0:
		t.Errorf("a fresh database reports settings: %v", all)
	}
	if on, err := d.IndexNameEnabled(ctx); err != nil || on {
		t.Errorf("the unset index switch reads %v (err %v), want off", on, err)
	}
	if rate, err := d.IndexBuildRate(ctx); err != nil || rate != 0 {
		t.Errorf("the unset build rate reads %d (err %v), want 0", rate, err)
	}
}

func TestConfigSecretRoundTrips(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)

	if _, ok, err := d.ReadConfigSecret(ctx, "absent"); err != nil || ok {
		t.Fatalf("an unset secret reported found %v (err %v)", ok, err)
	}

	want := state.ConfigSecret{Value: []byte{1, 2, 3}, KeyVer: 7}
	if err := d.WriteConfigSecret(ctx, "oidc", want); err != nil {
		t.Fatalf("WriteConfigSecret: %v", err)
	}
	got, ok, err := d.ReadConfigSecret(ctx, "oidc")
	if err != nil || !ok {
		t.Fatalf("ReadConfigSecret: %v (found %v)", err, ok)
	}
	if string(got.Value) != string(want.Value) || got.KeyVer != want.KeyVer {
		t.Errorf("read back %+v, want %+v", got, want)
	}

	if err := d.DeleteConfigSecret(ctx, "oidc"); err != nil {
		t.Fatalf("DeleteConfigSecret: %v", err)
	}
	if _, ok, err := d.ReadConfigSecret(ctx, "oidc"); err != nil || ok {
		t.Errorf("a deleted secret still reads back (found %v, err %v)", ok, err)
	}
}

func TestActiveWorkCountsWhatARestartWouldInterrupt(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	seedUser(t, d, 1, "u")

	switch w, err := d.CountActiveWork(ctx); {
	case err != nil:
		t.Fatalf("CountActiveWork: %v", err)
	case w.Uploads != 0 || w.Jobs != 0:
		t.Fatalf("a quiet server reports %+v", w)
	}

	if err := d.CreateUploadSession(ctx, sampleSession([]byte{1})); err != nil {
		t.Fatalf("CreateUploadSession: %v", err)
	}
	if _, err := d.CreateOp(ctx, 1, state.OpCopy, 1, 1, []string{"a"}); err != nil {
		t.Fatalf("CreateOp: %v", err)
	}

	w, err := d.CountActiveWork(ctx)
	if err != nil {
		t.Fatalf("CountActiveWork: %v", err)
	}
	if w.Uploads != 1 || w.Jobs != 1 {
		t.Errorf("counted %+v, want one of each", w)
	}
}

func TestFileBytesMeasuresTheFile(t *testing.T) {
	d, _ := open(t)
	n, err := d.FileBytes()
	if err != nil {
		t.Fatalf("FileBytes: %v", err)
	}
	if n <= 0 {
		t.Errorf("the state database measures %d bytes", n)
	}
}

// A helper the operation tests share.
func mustCreateOp(t *testing.T, d *state.DB, paths []string) int64 {
	t.Helper()
	id, err := d.CreateOp(context.Background(), 1, state.OpCopy, int64(len(paths)), 100, paths)
	if err != nil {
		t.Fatalf("CreateOp: %v", err)
	}
	return id
}

func TestOperationLifecycle(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	seedUser(t, d, 1, "u")

	id := mustCreateOp(t, d, []string{"a", "b", "c"})
	if err := d.SetOpProgress(ctx, id, 1, "working"); err != nil {
		t.Fatalf("SetOpProgress: %v", err)
	}

	op, results, err := d.GetOp(ctx, id)
	if err != nil {
		t.Fatalf("GetOp: %v", err)
	}
	if op.State != state.OpRunning || op.Progress != 1 || op.Message != "working" {
		t.Errorf("mid-run the operation reads %+v", op)
	}
	if len(results) != 0 {
		t.Errorf("%d results before anything finished", len(results))
	}

	if ferr := d.FinishOp(ctx, id, state.OpDone, 3, "", 200, []state.OpResult{
		{Operation: id, Idx: 0, Path: "a", OK: true, Reason: state.ReasonItemOk},
		{Operation: id, Idx: 1, Path: "b", OK: false, Reason: state.ReasonItemDenied, Text: "no"},
		{Operation: id, Idx: 2, Path: "c", OK: false, Reason: state.ReasonItemConflict},
	}); ferr != nil {
		t.Fatalf("FinishOp: %v", ferr)
	}

	op, results, err = d.GetOp(ctx, id)
	if err != nil {
		t.Fatalf("GetOp: %v", err)
	}
	if op.State != state.OpDone || op.FinishedNs != 200 || op.Progress != 3 {
		t.Errorf("the finished operation reads %+v", op)
	}
	if len(results) != 3 {
		t.Fatalf("%d results, want 3", len(results))
	}
	if results[1].Reason != state.ReasonItemDenied || results[1].Text != "no" {
		t.Errorf("the second result came back as %+v", results[1])
	}
}

func TestGetOpOfAnUnknownIDIsErrNoSuchOp(t *testing.T) {
	d, _ := open(t)
	if _, _, err := d.GetOp(context.Background(), 4242); !errors.Is(err, state.ErrNoSuchOp) {
		t.Fatalf("reading an unknown operation returned %v, want ErrNoSuchOp", err)
	}
}

// A job that stopped short can say which items it never reached, which is
// what recording the paths up front buys.
func TestUnfinishedItemsSplitAttemptingFromPending(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	seedUser(t, d, 1, "u")

	id := mustCreateOp(t, d, []string{"a", "b", "c"})
	if err := d.StartOpItem(ctx, id, 0); err != nil {
		t.Fatalf("StartOpItem: %v", err)
	}
	if err := d.FinishOp(ctx, id, state.OpInterrupted, 1, "", 200, []state.OpResult{
		{Operation: id, Idx: 0, Path: "a", OK: true},
	}); err != nil {
		t.Fatalf("FinishOp: %v", err)
	}
	// The runner was holding "b" when it died.
	if err := d.StartOpItem(ctx, id, 1); err != nil {
		t.Fatalf("StartOpItem: %v", err)
	}

	attempting, pending, err := d.UnfinishedOpItems(ctx, id)
	if err != nil {
		t.Fatalf("UnfinishedOpItems: %v", err)
	}
	if len(attempting) != 1 || attempting[0] != "b" {
		t.Errorf("attempting is %v, want [b]", attempting)
	}
	if len(pending) != 1 || pending[0] != "c" {
		t.Errorf("pending is %v, want [c]", pending)
	}
}

// A client re-attaching wants what is in flight, not a copy it watched
// finish an hour ago.
func TestListOpsReturnsOnlyUnfinishedWork(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	seedUser(t, d, 1, "u")
	seedUser(t, d, 2, "other")

	running := mustCreateOp(t, d, []string{"a"})
	done := mustCreateOp(t, d, []string{"b"})
	if err := d.FinishOp(ctx, done, state.OpDone, 1, "", 200, nil); err != nil {
		t.Fatalf("FinishOp: %v", err)
	}
	interrupted := mustCreateOp(t, d, []string{"c"})
	if err := d.InterruptOp(ctx, interrupted, 300); err != nil {
		t.Fatalf("InterruptOp: %v", err)
	}
	if _, err := d.CreateOp(ctx, 2, state.OpCopy, 1, 1, []string{"x"}); err != nil {
		t.Fatalf("CreateOp for another account: %v", err)
	}

	got, err := d.ListOps(ctx, 1, 0)
	if err != nil {
		t.Fatalf("ListOps: %v", err)
	}
	seen := map[int64]state.OpState{}
	for _, op := range got {
		if op.User != 1 {
			t.Errorf("another account's operation %d appeared", op.ID)
		}
		seen[op.ID] = op.State
	}
	if len(seen) != 2 || seen[running] != state.OpRunning || seen[interrupted] != state.OpInterrupted {
		t.Errorf("listed %v, want the running and the interrupted one", seen)
	}
}

func TestRequestOpCancelIsVisibleToTheRunner(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	seedUser(t, d, 1, "u")

	id := mustCreateOp(t, d, []string{"a"})
	if err := d.RequestOpCancel(ctx, id); err != nil {
		t.Fatalf("RequestOpCancel: %v", err)
	}
	op, _, err := d.GetOp(ctx, id)
	if err != nil {
		t.Fatalf("GetOp: %v", err)
	}
	if !op.Cancellation {
		t.Error("the cancellation request is not visible on the row")
	}
}

func TestUploadSessionRoundTripsIncludingNulls(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	seedUser(t, d, 1, "u")

	want := state.UploadSession{
		ID: []byte{1, 2, 3}, User: 1, Share: 4, Dest: "d/f", PartName: ".part",
		SpoolDir: "/spool", CacheDir: "/cache", CacheMerged: 7, Mode: 1,
		TotalLen: id64(1 << 30), ChunkSize: 1 << 20, ChunkMinAtCreation: 5 << 20,
		RandomAccess: true, NextName: 3, WriteHead: 99,
		SpooledNames: []uint32{4, 5, 6}, IfMatch: `"etag"`, Filename: "f.bin",
		MtimeNs: id64(4242), Mime: "application/octet-stream", RelativePath: "a/b",
		Verify: id64(1), VerifyDigest: []byte{9, 9}, CreatedNs: 1, ExpiresNs: 2, State: 0,
	}
	if err := d.CreateUploadSession(ctx, want); err != nil {
		t.Fatalf("CreateUploadSession: %v", err)
	}
	got, err := d.ReadUploadSession(ctx, want.ID)
	if err != nil {
		t.Fatalf("ReadUploadSession: %v", err)
	}
	if diff := sessionDiff(got, want); diff != "" {
		t.Errorf("read back a different session: %s", diff)
	}

	// And the all-optional-absent shape.
	bare := state.UploadSession{
		ID: []byte{9}, User: 1, Share: 1, Dest: "d", PartName: ".p",
		ChunkSize: 1, ChunkMinAtCreation: 1, Filename: "f", CreatedNs: 1, ExpiresNs: 2,
	}
	if cerr := d.CreateUploadSession(ctx, bare); cerr != nil {
		t.Fatalf("CreateUploadSession (bare): %v", cerr)
	}
	gotBare, err := d.ReadUploadSession(ctx, bare.ID)
	if err != nil {
		t.Fatalf("ReadUploadSession (bare): %v", err)
	}
	if gotBare.TotalLen != nil || gotBare.MtimeNs != nil || gotBare.Verify != nil {
		t.Errorf("absent optionals came back set: %+v", gotBare)
	}
	if gotBare.SpoolDir != "" || gotBare.Mime != "" || len(gotBare.VerifyDigest) != 0 {
		t.Errorf("absent strings came back set: %+v", gotBare)
	}
}

// sessionDiff compares by value rather than by pointer, since the optional
// columns are pointers and two equal sessions never share one.
func sessionDiff(got, want state.UploadSession) string {
	var out []string
	cmp := func(name string, a, b any) {
		if fmt.Sprint(a) != fmt.Sprint(b) {
			out = append(out, fmt.Sprintf("%s = %v, want %v", name, a, b))
		}
	}
	cmp("ID", got.ID, want.ID)
	cmp("User", got.User, want.User)
	cmp("Share", got.Share, want.Share)
	cmp("Dest", got.Dest, want.Dest)
	cmp("PartName", got.PartName, want.PartName)
	cmp("SpoolDir", got.SpoolDir, want.SpoolDir)
	cmp("CacheDir", got.CacheDir, want.CacheDir)
	cmp("CacheMerged", got.CacheMerged, want.CacheMerged)
	cmp("Mode", got.Mode, want.Mode)
	cmp("TotalLen", deref(got.TotalLen), deref(want.TotalLen))
	cmp("ChunkSize", got.ChunkSize, want.ChunkSize)
	cmp("ChunkMinAtCreation", got.ChunkMinAtCreation, want.ChunkMinAtCreation)
	cmp("RandomAccess", got.RandomAccess, want.RandomAccess)
	cmp("NextName", got.NextName, want.NextName)
	cmp("WriteHead", got.WriteHead, want.WriteHead)
	cmp("SpooledNames", got.SpooledNames, want.SpooledNames)
	cmp("IfMatch", got.IfMatch, want.IfMatch)
	cmp("Filename", got.Filename, want.Filename)
	cmp("MtimeNs", deref(got.MtimeNs), deref(want.MtimeNs))
	cmp("Mime", got.Mime, want.Mime)
	cmp("RelativePath", got.RelativePath, want.RelativePath)
	cmp("Verify", deref(got.Verify), deref(want.Verify))
	cmp("VerifyDigest", got.VerifyDigest, want.VerifyDigest)
	cmp("CreatedNs", got.CreatedNs, want.CreatedNs)
	cmp("ExpiresNs", got.ExpiresNs, want.ExpiresNs)
	cmp("State", got.State, want.State)
	return strings.Join(out, "; ")
}

func deref(v *int64) any {
	if v == nil {
		return "nil"
	}
	return *v
}

func TestReadingAnUnknownUploadSessionIsARefusal(t *testing.T) {
	d, _ := open(t)
	if _, err := d.ReadUploadSession(context.Background(), []byte{7}); !errors.Is(err, state.ErrNoSuchUploadSession) {
		t.Fatalf("reading an unknown session returned %v, want ErrNoSuchUploadSession", err)
	}
}

func TestUploadIntervalsAreRewrittenWholeAndSurviveTheFullRange(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	seedUser(t, d, 1, "u")
	if err := d.CreateUploadSession(ctx, sampleSession([]byte{1})); err != nil {
		t.Fatalf("CreateUploadSession: %v", err)
	}

	if err := d.RecordUploadInterval(ctx, []byte{1}, [][2]uint64{{0, 10}, {20, 30}}, 500); err != nil {
		t.Fatalf("RecordUploadInterval: %v", err)
	}
	// A merge that folded the two runs into one has to delete what it
	// absorbed, which is why the set is written whole.
	if err := d.RecordUploadInterval(ctx, []byte{1}, [][2]uint64{{0, 30}}, 600); err != nil {
		t.Fatalf("RecordUploadInterval: %v", err)
	}

	got, err := d.ReadUploadIntervals(ctx, []byte{1})
	if err != nil {
		t.Fatalf("ReadUploadIntervals: %v", err)
	}
	if len(got) != 1 || got[0] != [2]uint64{0, 30} {
		t.Errorf("intervals came back as %v, want [[0 30]]", got)
	}

	// Recording a range is also what proves the session is alive.
	s, err := d.ReadUploadSession(ctx, []byte{1})
	if err != nil {
		t.Fatalf("ReadUploadSession: %v", err)
	}
	if s.ExpiresNs != 600 {
		t.Errorf("the lifetime is %d, want the 600 the last record pushed it to", s.ExpiresNs)
	}
}

func TestAnIntervalThatDoesNotFitIsRefused(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	seedUser(t, d, 1, "u")
	if err := d.CreateUploadSession(ctx, sampleSession([]byte{1})); err != nil {
		t.Fatalf("CreateUploadSession: %v", err)
	}
	err := d.RecordUploadInterval(ctx, []byte{1}, [][2]uint64{{0, ^uint64(0)}}, 1)
	if err == nil {
		t.Fatal("an interval past the signed range was accepted")
	}
}

// The frontier only ever moves forward: the merger runs while chunks are
// still arriving.
func TestTheCacheMergeFrontierNeverRetreats(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	seedUser(t, d, 1, "u")
	if err := d.CreateUploadSession(ctx, sampleSession([]byte{1})); err != nil {
		t.Fatalf("CreateUploadSession: %v", err)
	}

	for _, merged := range []int64{100, 50, 200, 150} {
		if err := d.AdvanceUploadCacheMerged(ctx, []byte{1}, merged); err != nil {
			t.Fatalf("AdvanceUploadCacheMerged(%d): %v", merged, err)
		}
	}
	s, err := d.ReadUploadSession(ctx, []byte{1})
	if err != nil {
		t.Fatalf("ReadUploadSession: %v", err)
	}
	if s.CacheMerged != 200 {
		t.Errorf("the frontier is at %d, want the highest value written, 200", s.CacheMerged)
	}
}

// A transfer id is client-chosen, so it is scoped by account and a rebind is
// refused rather than silently orphaning the first session's spool.
func TestUploadAliasesAreScopedByAccountAndNeverRebound(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	seedUser(t, d, 1, "one")
	seedUser(t, d, 2, "two")
	for _, id := range [][]byte{{1}, {2}} {
		s := sampleSession(id)
		if err := d.CreateUploadSession(ctx, s); err != nil {
			t.Fatalf("CreateUploadSession: %v", err)
		}
	}

	bound, err := d.BindUploadAlias(ctx, "tid", 1, state.UploadAlias{
		Session: []byte{1}, Share: 1, Dest: "d/f",
	}, 1)
	if err != nil || !bound {
		t.Fatalf("BindUploadAlias: %v (bound %v)", err, bound)
	}

	// The same id again, same account: refused rather than rebound.
	again, err := d.BindUploadAlias(ctx, "tid", 1, state.UploadAlias{
		Session: []byte{2}, Share: 1, Dest: "d/g",
	}, 2)
	if err != nil {
		t.Fatalf("BindUploadAlias: %v", err)
	}
	if again {
		t.Error("a second bind of the same transfer id was accepted")
	}

	// Another account's namespace is its own, and cannot see the first's.
	if _, lerr := d.LookupUploadAlias(ctx, "tid", 2); !errors.Is(lerr, state.ErrNoSuchUploadSession) {
		t.Fatalf("another account's lookup returned %v, want a plain not-found", lerr)
	}

	a, err := d.LookupUploadAlias(ctx, "tid", 1)
	if err != nil {
		t.Fatalf("LookupUploadAlias: %v", err)
	}
	if string(a.Session) != string([]byte{1}) || a.Dest != "d/f" {
		t.Errorf("the alias resolved to %+v", a)
	}
}

// Deleting a session takes its aliases with it: a transfer id outliving the
// session it names would keep addressing a freed id.
func TestDeletingASessionTakesItsAliasesAndIntervals(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	seedUser(t, d, 1, "u")
	if err := d.CreateUploadSession(ctx, sampleSession([]byte{1})); err != nil {
		t.Fatalf("CreateUploadSession: %v", err)
	}
	if _, err := d.BindUploadAlias(ctx, "tid", 1, state.UploadAlias{
		Session: []byte{1}, Share: 1, Dest: "d/f",
	}, 1); err != nil {
		t.Fatalf("BindUploadAlias: %v", err)
	}
	if err := d.RecordUploadInterval(ctx, []byte{1}, [][2]uint64{{0, 5}}, 10); err != nil {
		t.Fatalf("RecordUploadInterval: %v", err)
	}

	if err := d.DeleteUploadSession(ctx, []byte{1}); err != nil {
		t.Fatalf("DeleteUploadSession: %v", err)
	}
	if _, err := d.LookupUploadAlias(ctx, "tid", 1); !errors.Is(err, state.ErrNoSuchUploadSession) {
		t.Errorf("the alias outlived its session: %v", err)
	}
	got, err := d.ReadUploadIntervals(ctx, []byte{1})
	if err != nil {
		t.Fatalf("ReadUploadIntervals: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("%d intervals outlived their session", len(got))
	}
}

// The row's absence is the fact that matters: it separates an admin's stored
// numbers from the compiled-in defaults.
func TestChunkSettingsReportWhetherAnAdminStoredThem(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)

	switch c, err := d.ReadChunkSettings(ctx); {
	case err != nil:
		t.Fatalf("ReadChunkSettings: %v", err)
	case c.Override:
		t.Fatal("a fresh database claims an admin override")
	}

	if err := d.WriteChunkSettings(ctx, 5<<20, 10<<20); err != nil {
		t.Fatalf("WriteChunkSettings: %v", err)
	}
	c, err := d.ReadChunkSettings(ctx)
	if err != nil {
		t.Fatalf("ReadChunkSettings: %v", err)
	}
	if !c.Override || c.Min != 5<<20 || c.Default != 10<<20 {
		t.Errorf("read back %+v", c)
	}
}

func TestTouchedDirsAccumulate(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)

	for range 3 {
		if err := d.TouchUploadDir(ctx, 1, "/a"); err != nil {
			t.Fatalf("TouchUploadDir: %v", err)
		}
	}
	if err := d.TouchUploadDir(ctx, 2, "/b"); err != nil {
		t.Fatalf("TouchUploadDir: %v", err)
	}

	got, err := d.ListUploadTouchedDirs(ctx)
	if err != nil {
		t.Fatalf("ListUploadTouchedDirs: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("%d touched directories, want 2: %+v", len(got), got)
	}
}

func TestUploadCountsAndReservations(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	seedUser(t, d, 1, "u")

	for i, total := range []int64{100, 200} {
		s := sampleSession([]byte{byte(i + 1)})
		s.TotalLen = id64(total)
		if err := d.CreateUploadSession(ctx, s); err != nil {
			t.Fatalf("CreateUploadSession: %v", err)
		}
	}

	n, err := d.CountUploadSessionsForUser(ctx, 1)
	if err != nil {
		t.Fatalf("CountUploadSessionsForUser: %v", err)
	}
	if n != 2 {
		t.Errorf("counted %d sessions, want 2", n)
	}
	sum, err := d.SumUploadReservedForUser(ctx, 1)
	if err != nil {
		t.Fatalf("SumUploadReservedForUser: %v", err)
	}
	if sum != 300 {
		t.Errorf("reserved %d bytes, want 300", sum)
	}
}

// One login URL opened twice must mint exactly one credential.
func TestALoginFlowIsApprovedExactlyOnce(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	seedUser(t, d, 1, "u")

	poll, login := []byte{1}, []byte{2}
	if err := d.PutLoginFlow(ctx, state.LoginFlowRow{
		PollDigest: poll, LoginDigest: login, CreatedNs: 10,
	}); err != nil {
		t.Fatalf("PutLoginFlow: %v", err)
	}

	if err := d.ApproveLoginFlow(ctx, login, 1, "somebody"); err != nil {
		t.Fatalf("the first approval: %v", err)
	}
	if err := d.ApproveLoginFlow(ctx, login, 1, "somebody"); !errors.Is(err, state.ErrLoginFlowApproved) {
		t.Fatalf("the second approval returned %v, want ErrLoginFlowApproved", err)
	}
	if err := d.ApproveLoginFlow(ctx, []byte{9}, 1, "x"); !errors.Is(err, state.ErrLoginFlowUnknown) {
		t.Fatalf("approving an unknown flow returned %v, want ErrLoginFlowUnknown", err)
	}

	got, err := d.LoginFlowByPoll(ctx, poll)
	if err != nil {
		t.Fatalf("LoginFlowByPoll: %v", err)
	}
	if got.ApprovedUser == nil || *got.ApprovedUser != 1 || got.ApprovedLogin != "somebody" {
		t.Errorf("the approved flow reads %+v", got)
	}
}

func TestPollingTooFastIsRefused(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)

	poll := []byte{1}
	if err := d.PutLoginFlow(ctx, state.LoginFlowRow{
		PollDigest: poll, LoginDigest: []byte{2}, CreatedNs: 10,
	}); err != nil {
		t.Fatalf("PutLoginFlow: %v", err)
	}

	if err := d.TouchLoginFlowPoll(ctx, poll, 1000, 100); err != nil {
		t.Fatalf("the first poll: %v", err)
	}
	if err := d.TouchLoginFlowPoll(ctx, poll, 1050, 100); !errors.Is(err, state.ErrLoginFlowTooSoon) {
		t.Fatalf("a poll inside the interval returned %v, want ErrLoginFlowTooSoon", err)
	}
	if err := d.TouchLoginFlowPoll(ctx, poll, 1200, 100); err != nil {
		t.Errorf("a poll past the interval: %v", err)
	}
	if err := d.TouchLoginFlowPoll(ctx, []byte{9}, 2000, 100); !errors.Is(err, state.ErrLoginFlowUnknown) {
		t.Errorf("polling an unknown flow returned %v, want ErrLoginFlowUnknown", err)
	}
}

func TestSweepingLoginFlowsRemovesTheAbandonedOnes(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)

	for i, created := range []int64{10, 20, 500} {
		if err := d.PutLoginFlow(ctx, state.LoginFlowRow{
			PollDigest: []byte{byte(i)}, LoginDigest: []byte{byte(100 + i)}, CreatedNs: created,
		}); err != nil {
			t.Fatalf("PutLoginFlow: %v", err)
		}
	}
	n, err := d.SweepLoginFlows(ctx, 100)
	if err != nil {
		t.Fatalf("SweepLoginFlows: %v", err)
	}
	if n != 2 {
		t.Errorf("swept %d flows, want 2", n)
	}
	if _, err := d.LoginFlowByPoll(ctx, []byte{2}); err != nil {
		t.Errorf("the young flow was swept too: %v", err)
	}
}
