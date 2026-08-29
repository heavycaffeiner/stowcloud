//go:build linux

package lifecycle_test

import (
	"context"
	"testing"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/middleware"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/secret"
	"github.com/heavycaffeiner/stowcloud/go/engine/lifecycle"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/auth"
)

// engineWithUser opens an engine holding one account.
func engineWithUser(t *testing.T) (*lifecycle.Engine, int64) {
	t.Helper()
	ctx := context.Background()

	e, err := lifecycle.Open(ctx, lifecycle.Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	t.Cleanup(func() {
		if cerr := e.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	})

	id, err := e.Auth.CreateUser(ctx, "alice", "Alice", secret.New([]byte("a-long-enough-password")))
	if err != nil {
		t.Fatalf("creating the account: %v", err)
	}
	return e, id
}

// A real session resolves to the account it was minted for, carrying every
// permission: a session is the account itself rather than a delegation of it.
func TestASessionResolvesToItsAccount(t *testing.T) {
	e, userID := engineWithUser(t)
	ctx := context.Background()

	sess, err := e.Auth.CreateSession(ctx, userID, "127.0.0.1", "test", 1, time.Hour)
	if err != nil {
		t.Fatalf("minting a session: %v", err)
	}

	got, ok := e.ResolvePrincipal(middleware.Credential{
		Kind:  middleware.CredentialSessionCookie,
		Token: sess.Token.Reveal(),
	})
	if !ok {
		t.Fatal("a live session did not resolve")
	}
	if got.UserID != userID {
		t.Errorf("resolved to account %d, want %d", got.UserID, userID)
	}
	if got.Kind != middleware.CredentialSessionCookie {
		t.Errorf("the kind became %v", got.Kind)
	}

	// Every bit. A session missing one would refuse an operation the account
	// itself is allowed to perform.
	for _, bit := range []acl.Perms{
		acl.Read, acl.Write, acl.Create, acl.Delete,
		acl.Rename, acl.Move, acl.Share, acl.Download,
	} {
		if !got.Mask.Has(bit) {
			t.Errorf("the session mask is missing %v", bit)
		}
	}
}

// An app password carries the mask it was granted and no more. That is the
// difference between a delegation and the account, and reading it as a session
// would hand every app password full control.
func TestAnAppPasswordCarriesOnlyItsScope(t *testing.T) {
	e, userID := engineWithUser(t)
	ctx := context.Background()

	token, err := e.Auth.CreateAppPassword(ctx, userID, "reader",
		auth.Scope{Perms: uint16(acl.Read | acl.Download)}, 0)
	if err != nil {
		t.Fatalf("minting an app password: %v", err)
	}

	got, ok := e.ResolvePrincipal(middleware.Credential{
		Kind:  middleware.CredentialBasicApp,
		Token: []byte(token),
	})
	if !ok {
		t.Fatal("a live app password did not resolve")
	}
	if got.UserID != userID {
		t.Errorf("resolved to account %d, want %d", got.UserID, userID)
	}

	if !got.Mask.Has(acl.Read) || !got.Mask.Has(acl.Download) {
		t.Errorf("the granted bits are missing: %v", got.Mask)
	}
	for _, bit := range []acl.Perms{acl.Write, acl.Create, acl.Delete, acl.Share} {
		if got.Mask.Has(bit) {
			t.Errorf("the mask carries %v, which was never granted", bit)
		}
	}
}

// The same token in the other header form resolves the same way. WebDAV and
// sync clients send whichever their library prefers.
func TestBothAppPasswordHeaderFormsResolve(t *testing.T) {
	e, userID := engineWithUser(t)
	ctx := context.Background()

	token, err := e.Auth.CreateAppPassword(ctx, userID, "both",
		auth.Scope{Perms: uint16(acl.Read)}, 0)
	if err != nil {
		t.Fatal(err)
	}

	for _, kind := range []middleware.CredentialKind{
		middleware.CredentialBasicApp,
		middleware.CredentialBearerApp,
	} {
		got, ok := e.ResolvePrincipal(middleware.Credential{Kind: kind, Token: []byte(token)})
		if !ok {
			t.Errorf("%v did not resolve", kind)
			continue
		}
		if got.UserID != userID {
			t.Errorf("%v resolved to %d", kind, got.UserID)
		}
	}
}

// Nothing that is not a live credential resolves. Each of these would be an
// account taken over by someone holding no secret at all.
func TestNothingElseResolves(t *testing.T) {
	e, userID := engineWithUser(t)
	ctx := context.Background()

	sess, err := e.Auth.CreateSession(ctx, userID, "127.0.0.1", "test", 1, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	token, err := e.Auth.CreateAppPassword(ctx, userID, "app", auth.Scope{Perms: uint16(acl.Read)}, 0)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		cred middleware.Credential
	}{
		{"no credential", middleware.Credential{Kind: middleware.CredentialNone}},
		{"an empty session token", middleware.Credential{Kind: middleware.CredentialSessionCookie}},
		{"a made-up session token", middleware.Credential{
			Kind: middleware.CredentialSessionCookie, Token: []byte("not-a-session"),
		}},
		{"an empty app password", middleware.Credential{Kind: middleware.CredentialBasicApp}},
		{"a made-up app password", middleware.Credential{
			Kind: middleware.CredentialBasicApp, Token: []byte("not-a-password"),
		}},
		{"a session token presented as an app password", middleware.Credential{
			Kind: middleware.CredentialBasicApp, Token: sess.Token.Reveal(),
		}},
		{"an app password presented as a session", middleware.Credential{
			Kind: middleware.CredentialSessionCookie, Token: []byte(token),
		}},
		{"a kind this build does not know", middleware.Credential{
			Kind: middleware.CredentialKind(200), Token: []byte(token),
		}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got, ok := e.ResolvePrincipal(c.cred); ok {
				t.Errorf("%s resolved to account %d", c.name, got.UserID)
			}
		})
	}
}

// A revoked session stops resolving. A session that outlived its revocation is
// a sign-out that did not sign anyone out.
func TestARevokedSessionStopsResolving(t *testing.T) {
	e, userID := engineWithUser(t)
	ctx := context.Background()

	sess, err := e.Auth.CreateSession(ctx, userID, "127.0.0.1", "test", 1, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := e.ResolvePrincipal(middleware.Credential{
		Kind: middleware.CredentialSessionCookie, Token: sess.Token.Reveal(),
	}); !ok {
		t.Fatal("the session did not resolve before revocation")
	}

	if rerr := e.Auth.RevokeSession(ctx, sess.Token); rerr != nil {
		t.Fatalf("revoking: %v", rerr)
	}

	if got, ok := e.ResolvePrincipal(middleware.Credential{
		Kind: middleware.CredentialSessionCookie, Token: sess.Token.Reveal(),
	}); ok {
		t.Errorf("a revoked session still resolves to account %d", got.UserID)
	}
}

// One account's credential never resolves to another. This is the property
// every other one rests on.
func TestACredentialNeverCrossesAccounts(t *testing.T) {
	e, alice := engineWithUser(t)
	ctx := context.Background()

	bob, err := e.Auth.CreateUser(ctx, "bob", "Bob", secret.New([]byte("another-long-password")))
	if err != nil {
		t.Fatalf("creating the second account: %v", err)
	}
	if bob == alice {
		t.Fatal("the two accounts share an id")
	}

	token, err := e.Auth.CreateAppPassword(ctx, bob, "bobs", auth.Scope{Perms: uint16(acl.Read)}, 0)
	if err != nil {
		t.Fatal(err)
	}

	got, ok := e.ResolvePrincipal(middleware.Credential{
		Kind: middleware.CredentialBasicApp, Token: []byte(token),
	})
	if !ok {
		t.Fatal("bob's credential did not resolve")
	}
	if got.UserID != bob {
		t.Errorf("bob's credential resolved to account %d, want %d", got.UserID, bob)
	}
}
