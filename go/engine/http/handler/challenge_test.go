//go:build linux

package handler_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/handler"
)

// challengeKey is deployment material in production; any fixed value proves
// the same properties here. A function rather than a package variable, so no
// test can leave a mutated key behind for the next one.
func challengeKey() []byte {
	return []byte("a-derivation-key-of-adequate-length")
}

// A minted challenge opens to the account it named.
func TestAChallengeNamesItsAccount(t *testing.T) {
	const now = 1_700_000_000

	c, err := handler.MintChallenge(challengeKey(), 42, now)
	if err != nil {
		t.Fatalf("minting: %v", err)
	}

	got, err := handler.OpenChallenge(challengeKey(), c, now)
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	if got != 42 {
		t.Errorf("the challenge opened to %d", got)
	}
}

// Two challenges for the same account in the same second differ.
//
// Without the nonce they would be identical, and one observed in a log or a
// proxy would be replayable for the rest of that second.
func TestTwoChallengesInTheSameSecondDiffer(t *testing.T) {
	const now = 1_700_000_000

	first, err := handler.MintChallenge(challengeKey(), 7, now)
	if err != nil {
		t.Fatal(err)
	}
	second, err := handler.MintChallenge(challengeKey(), 7, now)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Error("the same account in the same second minted the same challenge")
	}
}

// The window closes. A challenge left on a screen is not a standing
// credential, and this is the only thing that ends it: nothing is stored, so
// nothing can be deleted to revoke one.
func TestAChallengeExpires(t *testing.T) {
	const issued = 1_700_000_000

	c, err := handler.MintChallenge(challengeKey(), 42, issued)
	if err != nil {
		t.Fatal(err)
	}

	// The last second inside the window still opens.
	if _, oerr := handler.OpenChallenge(challengeKey(), c, issued+handler.ChallengeTTL); oerr != nil {
		t.Errorf("a challenge inside its window was refused: %v", oerr)
	}

	// One second past it does not.
	if _, oerr := handler.OpenChallenge(challengeKey(), c, issued+handler.ChallengeTTL+1); oerr == nil {
		t.Error("an expired challenge still names its account")
	}
}

// A challenge stamped in the future is refused.
//
// Accepting one extends its life by however far ahead it claims to be, so a
// forger under a leaked key could mint something that outlives the window by
// years. It also catches a clock that moved backwards, where the safe answer
// is to make the person sign in again.
func TestAChallengeFromTheFutureIsRefused(t *testing.T) {
	const issued = 1_700_000_000

	c, err := handler.MintChallenge(challengeKey(), 42, issued)
	if err != nil {
		t.Fatal(err)
	}
	if _, oerr := handler.OpenChallenge(challengeKey(), c, issued-1); oerr == nil {
		t.Error("a challenge stamped one second ahead was accepted")
	}
}

// A challenge minted under one key does not open under another. This is what
// makes the signature worth computing: possession of a challenge from one
// deployment must not authorize anything in the next.
func TestAChallengeIsBoundToItsKey(t *testing.T) {
	const now = 1_700_000_000

	c, err := handler.MintChallenge(challengeKey(), 42, now)
	if err != nil {
		t.Fatal(err)
	}
	other := append([]byte(nil), challengeKey()...)
	other[0]++

	if _, oerr := handler.OpenChallenge(other, c, now); !errors.Is(oerr, handler.ErrChallenge) {
		t.Errorf("a foreign key opened the challenge: %v", oerr)
	}
}

// Neither half works without a key, in either direction.
func TestAnEmptyKeyIsRefused(t *testing.T) {
	if _, err := handler.MintChallenge(nil, 42, 1); err == nil {
		t.Error("minted a challenge with no key, which anyone could recompute")
	}

	c, err := handler.MintChallenge(challengeKey(), 42, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, oerr := handler.OpenChallenge(nil, c, 1); oerr == nil {
		t.Error("opened a challenge with no key")
	}
}

// Malformed input is refused rather than parsed. Each of these is something a
// client can send, so each has to end in the same one error.
func TestMalformedChallengesAreRefused(t *testing.T) {
	const now = 1_700_000_000

	valid, err := handler.MintChallenge(challengeKey(), 42, now)
	if err != nil {
		t.Fatal(err)
	}
	body, _, _ := strings.Cut(valid, ".")

	cases := map[string]string{
		"empty":              "",
		"no separator":       body,
		"body only":          body + ".",
		"signature only":     "." + strings.TrimPrefix(valid, body+"."),
		"not base64":         "!!!.!!!",
		"body not base64":    "!!!." + strings.TrimPrefix(valid, body+"."),
		"oversized":          strings.Repeat("A", 4096) + "." + strings.Repeat("A", 4096),
		"wrong field count":  signed(t, "42:1700000000"),
		"account not a numb": signed(t, "alice:1700000000:AAAA"),
		"account zero":       signed(t, "0:1700000000:AAAA"),
		"account negative":   signed(t, "-1:1700000000:AAAA"),
		"stamp not a number": signed(t, "42:soon:AAAA"),
	}

	for name, challenge := range cases {
		if _, oerr := handler.OpenChallenge(challengeKey(), challenge, now); oerr == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

// signed builds a correctly signed challenge around an arbitrary body.
//
// The MAC is computed here rather than taken from the package, so a malformed
// body is refused on its own merits: with a valid signature, the only thing
// left to reject it is the body check itself. A helper that signed with
// nothing would have every case fail at the signature and prove nothing about
// the parse.
func signed(t *testing.T, body string) string {
	t.Helper()

	mac := hmac.New(sha256.New, challengeKey())
	if _, err := mac.Write([]byte(body)); err != nil {
		t.Fatalf("signing: %v", err)
	}
	return base64.RawURLEncoding.EncodeToString([]byte(body)) + "." +
		base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// The independently computed signature is the same one the package verifies.
// Without this, every case in the malformed table could be passing because the
// signature failed, and the body checks would be untested.
func TestTheTestSignerAgreesWithThePackage(t *testing.T) {
	const now = 1_700_000_000

	c := signed(t, "42:1700000000:AAAAAAAAAAA")
	got, err := handler.OpenChallenge(challengeKey(), c, now)
	if err != nil {
		t.Fatalf("the package rejected a challenge this test signed: %v", err)
	}
	if got != 42 {
		t.Errorf("opened to %d", got)
	}
}
