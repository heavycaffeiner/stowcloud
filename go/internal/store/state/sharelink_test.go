package state_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/internal/store/dbfile"
	"github.com/heavycaffeiner/stowcloud/go/internal/store/state"
)

const (
	insertUser = `
INSERT INTO user(id, name, pw_hash, created_ns) VALUES (1, 'alice', '$argon2id$a', 0)`

	insertLinkV1 = `
INSERT INTO share_link(id, token_hash, token_enc, share, path, dev, ino, btime_present, btime_ns,
                       owner, perms, downloads, created_ns)
VALUES (?, ?, ?, 7, ?, ?, ?, ?, ?, 1, 1, 0, 0)`
)

// openV1State builds a state database at the version that shipped, with the
// links the test wants in it, and closes it.
func openV1State(t *testing.T, path string, links func(context.Context, *sql.Tx) error) {
	t.Helper()
	ctx := context.Background()
	f, err := dbfile.Open(ctx, state.SpecV1(path))
	if err != nil {
		t.Fatalf("opening a version 1 state: %v", err)
	}
	if werr := f.Write(ctx, func(tx *sql.Tx) error {
		if _, uerr := tx.ExecContext(ctx, insertUser); uerr != nil {
			return uerr
		}
		return links(ctx, tx)
	}); werr != nil {
		t.Fatalf("writing to a version 1 state: %v", werr)
	}
	if cerr := f.Close(); cerr != nil {
		t.Fatalf("closing a version 1 state: %v", cerr)
	}
}

// The two representations a link may have, carried across with their columns
// intact and the token key version paired with the ciphertext.
func TestMigrationKeepsBothLinkRepresentations(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")

	openV1State(t, path, func(ctx context.Context, tx *sql.Tx) error {
		// A link on the share root, with no file to name.
		if _, err := tx.ExecContext(ctx, insertLinkV1,
			1, []byte{0xcc}, nil, "", nil, nil, nil, nil); err != nil {
			return err
		}
		// A link on one file, with an encrypted token the owner can still read.
		_, err := tx.ExecContext(ctx, insertLinkV1,
			2, []byte{0xdd}, []byte{0xee}, "docs/a.txt", 2, 3, 1, 4)
		return err
	})

	f, err := dbfile.Open(ctx, state.Spec(path))
	if err != nil {
		t.Fatalf("opening the migrated state: %v", err)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	}()

	if v, verr := f.Version(ctx); verr != nil || v != 2 {
		t.Fatalf("version %d, %v; want 2 and no error", v, verr)
	}

	var keyVer *int64
	var dev, ino *int64
	if qerr := f.SQL().QueryRowContext(ctx,
		`SELECT token_key_ver, dev, ino FROM share_link WHERE id = 1`).
		Scan(&keyVer, &dev, &ino); qerr != nil {
		t.Fatalf("reading the path-only link: %v", qerr)
	}
	if keyVer != nil || dev != nil || ino != nil {
		t.Errorf("the path-only link came across with a key version or an identity")
	}

	if qerr := f.SQL().QueryRowContext(ctx,
		`SELECT token_key_ver, dev, ino FROM share_link WHERE id = 2`).
		Scan(&keyVer, &dev, &ino); qerr != nil {
		t.Fatalf("reading the identity-bearing link: %v", qerr)
	}
	if keyVer == nil || *keyVer != 0 || dev == nil || *dev != 2 || ino == nil || *ino != 3 {
		t.Errorf("the identity-bearing link came across wrong")
	}
}

// The all-zero tuple the Phase 2 importer wrote meant two different things and
// nothing can now tell them apart, so it stops the migration and names the link
// rather than being read as either one.
func TestMigrationRefusesTheAmbiguousIdentity(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")

	openV1State(t, path, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, insertLinkV1,
			42, []byte{0xcc}, nil, "docs/a.txt", 0, 0, 0, 0)
		return err
	})

	_, err := dbfile.Open(ctx, state.Spec(path))
	if !errors.Is(err, state.ErrLinkTargetAmbiguous) {
		t.Fatalf("opening returned %v, want ErrLinkTargetAmbiguous", err)
	}
	if !strings.Contains(err.Error(), "42") {
		t.Errorf("the refusal does not name the link: %v", err)
	}
	if !strings.Contains(err.Error(), "migrate --from-rust") {
		t.Errorf("the refusal does not say how to recover: %v", err)
	}
}

// Any other combination is corruption in the durable half, and it is refused
// rather than weakened: a link whose identity is dropped becomes a link to
// whatever is created at that path next.
func TestMigrationRefusesAPartialIdentity(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")

	openV1State(t, path, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, insertLinkV1,
			7, []byte{0xcc}, nil, "docs/a.txt", 2, 3, nil, nil)
		return err
	})

	_, err := dbfile.Open(ctx, state.Spec(path))
	if !errors.Is(err, state.ErrLinkTargetMalformed) {
		t.Fatalf("opening returned %v, want ErrLinkTargetMalformed", err)
	}
	if !strings.Contains(err.Error(), "7") {
		t.Errorf("the refusal does not name the link: %v", err)
	}
}

// The constraint stands after the migration, not only during it: a link written
// later cannot invent a third representation either.
func TestTheMigratedSchemaRefusesAThirdRepresentation(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	openV1State(t, path, func(context.Context, *sql.Tx) error { return nil })

	f, err := dbfile.Open(ctx, state.Spec(path))
	if err != nil {
		t.Fatalf("opening the migrated state: %v", err)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	}()

	for _, tc := range []struct {
		name                     string
		dev, ino, present, btime any
		enc                      any
		keyVer                   any
	}{
		{"the all-zero tuple", 0, 0, 0, 0, nil, nil},
		{"a partial tuple", 2, 3, nil, nil, nil, nil},
		{"an absent birth time", 2, 3, 0, 0, nil, nil},
		{"a ciphertext with no key version", nil, nil, nil, nil, []byte{0xee}, nil},
		{"a key version with no ciphertext", nil, nil, nil, nil, nil, 1},
	} {
		werr := f.Write(ctx, func(tx *sql.Tx) error {
			_, ierr := tx.ExecContext(ctx, `
INSERT INTO share_link(id, token_hash, token_enc, token_key_ver, share, path,
                       dev, ino, btime_present, btime_ns, owner, perms, downloads, created_ns)
VALUES (99, X'ff', ?, ?, 7, 'docs/a.txt', ?, ?, ?, ?, 1, 1, 0, 0)`,
				tc.enc, tc.keyVer, tc.dev, tc.ino, tc.present, tc.btime)
			return ierr
		})
		if werr == nil {
			t.Errorf("%s was accepted", tc.name)
		}
	}
}
