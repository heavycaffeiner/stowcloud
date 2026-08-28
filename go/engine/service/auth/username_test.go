package auth_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/service/auth"
)

func TestTheUsernameRuleAcceptsWhatEveryConsumerCanCarry(t *testing.T) {
	for _, name := range []string{
		"a",
		"_",
		"alice",
		"alice-2",
		"alice_smith",
		"a0",
		strings.Repeat("a", auth.UsernameMaxLen),
	} {
		if err := auth.ValidUsername(name); err != nil {
			t.Fatalf("ValidUsername(%q) = %v", name, err)
		}
	}
}

// The setup screen used to admit these and the passwd renderer refuses them,
// and because the renderer refuses the file rather than the name, one such
// account cost every account its file-sharing access at once.
func TestTheUsernameRuleRefusesWhatTheOlderScreensAdmitted(t *testing.T) {
	for _, name := range []string{
		"",
		strings.Repeat("a", auth.UsernameMaxLen+1),
		"Alice",
		"a.b",
		"a@example.test",
		"a+b",
		"0alice",
		"-alice",
		"alice bob",
		"alice:bob",
		"alice\nbob",
		"alice\rbob",
		"alice\x00bob",
		"álice",
	} {
		if err := auth.ValidUsername(name); !errors.Is(err, auth.ErrNameInvalid) {
			t.Fatalf("ValidUsername(%q) = %v, want a refusal", name, err)
		}
	}
}

// A validation message that echoes what was typed is a reflection primitive.
func TestTheUsernameRefusalDoesNotEchoTheInput(t *testing.T) {
	const hostile = "<script>alert(1)</script>"
	err := auth.ValidUsername(hostile)
	if err == nil {
		t.Fatal("the hostile name was accepted")
	}
	if strings.Contains(err.Error(), hostile) || strings.Contains(err.Error(), "script") {
		t.Fatalf("the refusal echoes its input: %q", err)
	}
}

// The rule gates creation whoever calls it: an account made through the
// administrative surface used to bypass validation entirely.
func TestCreationEnforcesTheUsernameRule(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)

	if _, err := f.svc.CreateUser(ctx, "Alice", "", pw(testPassword)); !errors.Is(err, auth.ErrNameInvalid) {
		t.Fatalf("CreateUser with an upper-case name returned %v", err)
	}
	if _, err := f.svc.CreateAdmin(ctx, "a.b", "", pw(testPassword)); !errors.Is(err, auth.ErrNameInvalid) {
		t.Fatalf("CreateAdmin with a dotted name returned %v", err)
	}
	// Nothing was written on the way to the refusal.
	if n, err := f.svc.CountUsers(ctx); err != nil || n != 0 {
		t.Fatalf("the refused creations left %d accounts, %v", n, err)
	}
}

func TestCreationRefusesAShortPasswordAndADuplicateName(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.account(t, "alice")

	if _, err := f.svc.CreateUser(ctx, "bob", "", pw("short")); !errors.Is(err, auth.ErrWeakPassword) {
		t.Fatalf("a short password returned %v", err)
	}
	if _, err := f.svc.CreateUser(ctx, "alice", "", pw(testPassword)); !errors.Is(err, auth.ErrNameTaken) {
		t.Fatalf("a duplicate name returned %v", err)
	}
}
