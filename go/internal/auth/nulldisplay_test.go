package auth

import (
	"context"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/internal/secret"
)

// An account with no display name must be able to log in.
//
// The column is optional and an imported account carries none, and reading it
// into a plain string failed the scan: the account could not log in at all,
// and the refusal was a server error rather than anything a person could act
// on. Every account this build creates has one, which is why no test had a row
// without it until a migrated database produced one.
func TestAnAccountWithNoDisplayNameCanLogIn(t *testing.T) {
	s, _ := openService(t, nil)
	ctx := context.Background()

	if _, err := s.CreateUser(ctx, "alice", "Alice", secret.New([]byte("correct-horse-battery"))); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	// What an imported row looks like: the name is set and the display name is
	// absent rather than empty.
	if _, err := s.st.SQL().ExecContext(ctx, `UPDATE user SET display = NULL WHERE id = 1`); err != nil {
		t.Fatalf("clearing the display name: %v", err)
	}

	u, err := s.userByName(ctx, "alice")
	if err != nil {
		t.Fatalf("reading an account with no display name: %v", err)
	}
	if u.name != "alice" {
		t.Errorf("name = %q", u.name)
	}
	if u.display != "" {
		t.Errorf("display = %q, want the empty string", u.display)
	}

	// And by id, which is the path every authenticated request takes.
	if _, err := s.userByID(ctx, 1); err != nil {
		t.Fatalf("reading by id: %v", err)
	}
}
