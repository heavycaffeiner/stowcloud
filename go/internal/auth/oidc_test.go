package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/internal/limits"
	"github.com/heavycaffeiner/stowcloud/go/internal/secret"
)

// The link, and the flow that carries one across the round trip to the
// provider.
//
// Two properties matter more than the rest. A flow is consumed, so an
// authorization code cannot be replayed. And the binding has to match, which is
// what stops somebody delivering a legitimate callback URL to another person's
// browser: the state travels in a URL, through logs and referrer headers, and
// the binding does not.

func oidcUser(t *testing.T, s *Service, name string) int64 {
	t.Helper()
	id, err := s.CreateUser(context.Background(), name, name, secret.New([]byte("correct horse battery staple")))
	if err != nil {
		t.Fatalf("creating an account: %v", err)
	}
	return id
}

func TestALinkIsStoredAndReadBack(t *testing.T) {
	s, _ := openService(t, nil)
	ctx := context.Background()
	uid := oidcUser(t, s, "alice")

	if _, err := s.OIDCLinkOf(ctx, uid); !errors.Is(err, ErrNoOIDCLink) {
		t.Fatalf("a fresh account reported a link: %v", err)
	}

	if err := s.CreateOIDCLink(ctx, uid, "https://idp.example.com", "sub-123"); err != nil {
		t.Fatalf("linking: %v", err)
	}

	link, err := s.OIDCLinkOf(ctx, uid)
	if err != nil {
		t.Fatalf("reading the link: %v", err)
	}
	if link.Issuer != "https://idp.example.com" || link.Subject != "sub-123" {
		t.Errorf("the link reads as %+v", link)
	}
	if link.LastLoginNs != nil {
		t.Error("a link that has never been used reports a last sign-in")
	}

	// The identity resolves back to the account, which is what a sign-in does.
	got, rerr := s.UserForOIDCIdentity(ctx, "https://idp.example.com", "sub-123")
	if rerr != nil || got != uid {
		t.Fatalf("the identity resolved to %d (%v), want %d", got, rerr, uid)
	}
}

// The identity is the issuer and the subject together. A subject that matches
// under a different issuer is a different person.
func TestTheIssuerIsPartOfTheIdentity(t *testing.T) {
	s, _ := openService(t, nil)
	ctx := context.Background()
	uid := oidcUser(t, s, "alice")

	if err := s.CreateOIDCLink(ctx, uid, "https://idp.example.com", "sub-123"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UserForOIDCIdentity(ctx, "https://other.example.com", "sub-123"); !errors.Is(err, ErrNoOIDCLink) {
		t.Fatal("the same subject under a different issuer resolved to the account")
	}
}

// An identity already linked elsewhere is refused. Taking it would move an
// account's only way in to a different person.
func TestAnIdentityAlreadyLinkedIsRefused(t *testing.T) {
	s, _ := openService(t, nil)
	ctx := context.Background()
	alice := oidcUser(t, s, "alice")
	bob := oidcUser(t, s, "bob")

	if err := s.CreateOIDCLink(ctx, alice, "https://idp.example.com", "sub-123"); err != nil {
		t.Fatal(err)
	}
	err := s.CreateOIDCLink(ctx, bob, "https://idp.example.com", "sub-123")
	if !errors.Is(err, ErrOIDCLinkTaken) {
		t.Fatalf("linking somebody else's identity answered %v", err)
	}

	// Still the first account's.
	got, rerr := s.UserForOIDCIdentity(ctx, "https://idp.example.com", "sub-123")
	if rerr != nil {
		t.Fatalf("resolving the identity: %v", rerr)
	}
	if got != alice {
		t.Errorf("the identity moved to %d", got)
	}
}

// Re-linking the same account replaces what it had, so a provider migration
// does not need a separate unlink first.
func TestRelinkingReplacesTheOldIdentity(t *testing.T) {
	s, _ := openService(t, nil)
	ctx := context.Background()
	uid := oidcUser(t, s, "alice")

	if err := s.CreateOIDCLink(ctx, uid, "https://idp.example.com", "old"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateOIDCLink(ctx, uid, "https://idp.example.com", "new"); err != nil {
		t.Fatalf("re-linking: %v", err)
	}

	link, err := s.OIDCLinkOf(ctx, uid)
	if err != nil || link.Subject != "new" {
		t.Fatalf("the link reads as %+v", link)
	}
	// The old identity no longer resolves, or it would still be a way in.
	if _, rerr := s.UserForOIDCIdentity(ctx, "https://idp.example.com", "old"); !errors.Is(rerr, ErrNoOIDCLink) {
		t.Fatal("the replaced identity still resolves to the account")
	}
}

func TestRemovingALinkDetachesTheIdentity(t *testing.T) {
	s, _ := openService(t, nil)
	ctx := context.Background()
	uid := oidcUser(t, s, "alice")

	if err := s.CreateOIDCLink(ctx, uid, "https://idp.example.com", "sub-123"); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveOIDCLink(ctx, uid); err != nil {
		t.Fatalf("removing: %v", err)
	}
	if _, err := s.OIDCLinkOf(ctx, uid); !errors.Is(err, ErrNoOIDCLink) {
		t.Fatal("the link survived its own removal")
	}
	if _, err := s.UserForOIDCIdentity(ctx, "https://idp.example.com", "sub-123"); !errors.Is(err, ErrNoOIDCLink) {
		t.Fatal("the identity still resolves after the link was removed")
	}
}

// A flow is consumed. One that can be redeemed twice is an authorization code
// that can be replayed.
func TestAFlowIsConsumedByTheFirstCallback(t *testing.T) {
	s, _ := openService(t, nil)
	ctx := context.Background()
	uid := oidcUser(t, s, "alice")

	if err := s.StartOIDCFlow(ctx, uid, "state", "nonce", "binding", "verifier", "https://x/cb", "/settings"); err != nil {
		t.Fatalf("starting: %v", err)
	}

	flow, err := s.TakeOIDCFlow(ctx, "state", "binding")
	if err != nil {
		t.Fatalf("taking: %v", err)
	}
	if flow.User != uid || flow.CodeVerifier != "verifier" || flow.ReturnTo != "/settings" {
		t.Errorf("the flow reads as %+v", flow)
	}

	if _, err := s.TakeOIDCFlow(ctx, "state", "binding"); !errors.Is(err, ErrNoOIDCFlow) {
		t.Fatal("the same flow was redeemed twice, so a code can be replayed")
	}
}

// The binding has to match. It is what stops a callback URL delivered to
// somebody else's browser from completing.
func TestAFlowWithTheWrongBindingIsRefusedAndBurned(t *testing.T) {
	s, _ := openService(t, nil)
	ctx := context.Background()
	uid := oidcUser(t, s, "alice")

	if err := s.StartOIDCFlow(ctx, uid, "state", "nonce", "binding", "verifier", "https://x/cb", "/"); err != nil {
		t.Fatal(err)
	}

	if _, err := s.TakeOIDCFlow(ctx, "state", "not-the-binding"); !errors.Is(err, ErrNoOIDCFlow) {
		t.Fatalf("a mismatched binding answered %v", err)
	}
	// Burned rather than left for another attempt: a flow whose binding failed
	// is not one to leave available.
	if _, err := s.TakeOIDCFlow(ctx, "state", "binding"); !errors.Is(err, ErrNoOIDCFlow) {
		t.Fatal("a flow survived a failed binding, so the attempt can be retried")
	}
}

// An empty binding does not match a real one, which is what a callback
// arriving with no cookie at all presents.
func TestAMissingBindingDoesNotMatch(t *testing.T) {
	s, _ := openService(t, nil)
	ctx := context.Background()
	uid := oidcUser(t, s, "alice")

	if err := s.StartOIDCFlow(ctx, uid, "state", "nonce", "binding", "verifier", "https://x/cb", "/"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TakeOIDCFlow(ctx, "state", ""); !errors.Is(err, ErrNoOIDCFlow) {
		t.Fatalf("a callback with no binding answered %v", err)
	}
}

// A flow expires, so a state value lifted from a log is not usable later.
func TestAFlowExpires(t *testing.T) {
	clk := &mutableClock{t: time.Unix(0, 0)}
	s, _ := openService(t, clk)
	ctx := context.Background()
	uid := oidcUser(t, s, "alice")

	if err := s.StartOIDCFlow(ctx, uid, "state", "nonce", "binding", "verifier", "https://x/cb", "/"); err != nil {
		t.Fatal(err)
	}

	clk.advance(limits.OIDCFlowLifetime + time.Second)
	if _, err := s.TakeOIDCFlow(ctx, "state", "binding"); !errors.Is(err, ErrNoOIDCFlow) {
		t.Fatalf("a flow past its lifetime answered %v", err)
	}
}

// Just inside the lifetime still works, which is what proves the bound is what
// refused above rather than the flow never having been stored.
func TestAFlowInsideItsLifetimeStillWorks(t *testing.T) {
	clk := &mutableClock{t: time.Unix(0, 0)}
	s, _ := openService(t, clk)
	ctx := context.Background()
	uid := oidcUser(t, s, "alice")

	if err := s.StartOIDCFlow(ctx, uid, "state", "nonce", "binding", "verifier", "https://x/cb", "/"); err != nil {
		t.Fatal(err)
	}

	clk.advance(limits.OIDCFlowLifetime - time.Second)
	if _, err := s.TakeOIDCFlow(ctx, "state", "binding"); err != nil {
		t.Fatalf("a flow inside its lifetime was refused: %v", err)
	}
}

// The nonce survives the round trip, because the token verifier is handed it
// and refuses a token that does not carry it.
//
// It comes back whole rather than as a digest for exactly that reason: the
// check belongs beside the issuer, the audience and the validity window, and a
// nonce checked separately is a check that can be forgotten. This started as a
// digest, which meant the verifier was called with an empty nonce and refused
// every sign-in.
func TestTheNonceComesBackWholeForTheVerifier(t *testing.T) {
	s, _ := openService(t, nil)
	ctx := context.Background()
	uid := oidcUser(t, s, "alice")

	if err := s.StartOIDCFlow(ctx, uid, "state", "the-nonce", "binding", "verifier", "https://x/cb", "/"); err != nil {
		t.Fatal(err)
	}
	flow, err := s.TakeOIDCFlow(ctx, "state", "binding")
	if err != nil {
		t.Fatal(err)
	}

	if flow.Nonce != "the-nonce" {
		t.Fatalf("the nonce came back as %q; the verifier refuses a token when it is empty", flow.Nonce)
	}
	if flow.CodeVerifier != "verifier" {
		t.Errorf("the code verifier came back as %q; the exchange has to send it", flow.CodeVerifier)
	}
}

// Signing in stamps the link, which is what an administrator reads when working
// out whether an identity is in use.
func TestSigningInStampsTheLink(t *testing.T) {
	s, _ := openService(t, nil)
	ctx := context.Background()
	uid := oidcUser(t, s, "alice")

	if err := s.CreateOIDCLink(ctx, uid, "https://idp.example.com", "sub-123"); err != nil {
		t.Fatal(err)
	}
	if err := s.TouchOIDCLink(ctx, "https://idp.example.com", "sub-123"); err != nil {
		t.Fatalf("stamping: %v", err)
	}

	link, err := s.OIDCLinkOf(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if link.LastLoginNs == nil {
		t.Fatal("the link records no sign-in after one happened")
	}
}

// A link needs both halves. Storing one with an empty subject would make every
// identity with no subject resolve to that account.
func TestALinkNeedsBothHalves(t *testing.T) {
	s, _ := openService(t, nil)
	ctx := context.Background()
	uid := oidcUser(t, s, "alice")

	if err := s.CreateOIDCLink(ctx, uid, "https://idp.example.com", ""); err == nil {
		t.Error("a link with no subject was stored")
	}
	if err := s.CreateOIDCLink(ctx, uid, "", "sub-123"); err == nil {
		t.Error("a link with no issuer was stored")
	}
}
