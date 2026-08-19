//go:build linux

package upload

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/internal/limits"
)

// A transfer id is the client's own string, so it is guessable and collidable.
// The account scoping is the whole of what makes the lookup safe.

func TestAnAliasResolvesOnlyInsideItsOwnAccount(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	s := f.create(t, "file.bin", 4, SessionSpec{Mode: SpoolNameOrdered})

	if err := f.engine.BindAlias(ctx, "transfer-1", testUser, s.ID); err != nil {
		t.Fatalf("BindAlias: %v", err)
	}
	got, err := f.engine.LookupAlias(ctx, "transfer-1", testUser)
	if err != nil {
		t.Fatalf("LookupAlias: %v", err)
	}
	if got.Session != s.ID {
		t.Fatalf("the alias resolved to %s, want %s", got.Session, s.ID)
	}
	if got.Dest.String() != "file.bin" {
		t.Fatalf("the alias names %q, want file.bin", got.Dest)
	}

	// The same id under another account is missing, identically to one that
	// was never bound, so the lookup is not an existence oracle.
	_, err = f.engine.LookupAlias(ctx, "transfer-1", testUser+1)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("another account's lookup returned %v, want ErrNotFound", err)
	}
	_, err = f.engine.LookupAlias(ctx, "never-bound", testUser)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("an unbound id returned %v, want ErrNotFound", err)
	}
}

// Rebinding is refused rather than replacing: it would orphan the first
// session's spool with nothing left naming it.
func TestRebindingATransferIdIsRefused(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	first := f.create(t, "a.bin", 4, SessionSpec{Mode: SpoolNameOrdered})
	second := f.create(t, "b.bin", 4, SessionSpec{Mode: SpoolNameOrdered})

	if err := f.engine.BindAlias(ctx, "transfer-1", testUser, first.ID); err != nil {
		t.Fatalf("the first bind: %v", err)
	}
	if err := f.engine.BindAlias(ctx, "transfer-1", testUser, second.ID); !errors.Is(err, ErrAliasTaken) {
		t.Fatalf("the rebind returned %v, want ErrAliasTaken", err)
	}
	got, err := f.engine.LookupAlias(ctx, "transfer-1", testUser)
	if err != nil {
		t.Fatalf("LookupAlias: %v", err)
	}
	if got.Session != first.ID {
		t.Fatalf("the refused rebind moved the alias to %s", got.Session)
	}

	// Two accounts may each hold the same id, because the namespace is
	// per-account and a client does not know what another one picked.
	if err := f.engine.BindAlias(ctx, "transfer-1", testUser, second.ID); !errors.Is(err, ErrAliasTaken) {
		t.Fatalf("the second rebind returned %v, want ErrAliasTaken", err)
	}
}

func TestAnAliasCannotBeBoundToAnotherAccountsSession(t *testing.T) {
	f := newFixture(t)
	s := f.create(t, "file.bin", 4, SessionSpec{Mode: SpoolNameOrdered})
	err := f.engine.BindAlias(context.Background(), "transfer-1", testUser+1, s.ID)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("binding another account's session returned %v, want ErrNotFound", err)
	}
}

// The id arrives in a URL path segment, so it is bounded and refused for the
// shapes that cannot name anything, before it reaches a query.
func TestATransferIdIsValidatedAtTheBoundary(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	s := f.create(t, "file.bin", 4, SessionSpec{Mode: SpoolNameOrdered})

	for _, tc := range []struct {
		name string
		tid  string
	}{
		{"empty", ""},
		{"a separator", "a/b"},
		{"a control character", "a\x00b"},
		{"over the bound", strings.Repeat("x", limits.NameBytes+1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := f.engine.BindAlias(ctx, tc.tid, testUser, s.ID); err == nil {
				t.Fatalf("binding %q was accepted", tc.tid)
			}
			if _, err := f.engine.LookupAlias(ctx, tc.tid, testUser); err == nil {
				t.Fatalf("looking up %q was accepted", tc.tid)
			}
		})
	}
}

// The alias dies with the session it names, or a transfer id keeps addressing
// a session id nothing holds.
func TestAnAliasGoesWhenItsSessionIsSweptAway(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	s := f.create(t, "file.bin", 4, SessionSpec{Mode: SpoolNameOrdered})
	if err := f.engine.BindAlias(ctx, "transfer-1", testUser, s.ID); err != nil {
		t.Fatalf("BindAlias: %v", err)
	}

	if err := f.store.State().DeleteUploadSession(ctx, s.ID.Bytes()); err != nil {
		t.Fatalf("removing the session: %v", err)
	}
	if _, err := f.engine.LookupAlias(ctx, "transfer-1", testUser); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the alias outlived its session: %v", err)
	}
}
