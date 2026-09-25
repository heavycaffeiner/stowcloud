//go:build linux

// The token carrying "this account's password was accepted" between the two
// halves of a sign-in.
//
// The password screen and the code screen are separate requests, so something
// has to remember that the first one succeeded. A stored row would need
// sweeping and would leave an abandoned sign-in behind; a signed token needs
// neither, because it expires by arithmetic rather than by deletion.
package handler

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
)

// ChallengeTTL bounds the code screen: long enough to open an authenticator
// and read six digits, short enough that a challenge left on a screen is not a
// standing credential.
const ChallengeTTL = 5 * 60

// challengeMaxLen caps what MintChallenge produces by a wide margin, so a
// caller cannot make the server hash an arbitrary amount of attacker input
// before rejecting it.
const challengeMaxLen = 512

// ErrChallenge is every way a challenge can fail to name an account.
//
// One error rather than several: an expired challenge, a forged signature and
// a truncated one all mean the person starts over, and telling them apart says
// which half a forger should keep working on.
var ErrChallenge = errors.New("the challenge is not usable")

// MintChallenge signs that userID's password verified at nowUnix.
//
// The nonce is what stops two sign-ins in the same second from producing the
// same token. Without it, a challenge observed once would be replayable for
// the rest of that second against the same account.
func MintChallenge(key []byte, userID, nowUnix int64) (string, error) {
	if len(key) == 0 {
		// An unkeyed HMAC is a checksum: anyone could compute one, and a
		// challenge is exactly the thing whose forgery skips the password.
		return "", errors.New("minting a challenge without a key")
	}

	nonce := make([]byte, 8)
	if _, err := rand.Read(nonce); err != nil {
		return "", errors.New("minting a challenge without randomness")
	}

	body := strconv.FormatInt(userID, 10) + ":" +
		strconv.FormatInt(nowUnix, 10) + ":" +
		base64.RawURLEncoding.EncodeToString(nonce)

	return base64.RawURLEncoding.EncodeToString([]byte(body)) + "." +
		base64.RawURLEncoding.EncodeToString(sign(key, []byte(body))), nil
}

// OpenChallenge verifies one and returns the account it names.
func OpenChallenge(key []byte, challenge string, nowUnix int64) (int64, error) {
	if len(key) == 0 || challenge == "" || len(challenge) > challengeMaxLen {
		return 0, ErrChallenge
	}

	encBody, encMAC, split := strings.Cut(challenge, ".")
	if !split {
		return 0, ErrChallenge
	}
	body, err := base64.RawURLEncoding.DecodeString(encBody)
	if err != nil {
		return 0, ErrChallenge
	}
	presented, err := base64.RawURLEncoding.DecodeString(encMAC)
	if err != nil {
		return 0, ErrChallenge
	}

	// The signature is checked before the body is parsed. Parsing first would
	// let an unsigned body steer the code that runs next, and the constant
	// time comparison is what decides whether somebody who never presented a
	// password gets a session.
	if !hmac.Equal(sign(key, body), presented) {
		return 0, ErrChallenge
	}

	userID, issued, ok := parseChallengeBody(string(body))
	if !ok {
		return 0, ErrChallenge
	}

	// Both directions. A challenge stamped in the future is either a clock
	// that moved backwards or a forgery under a leaked key, and accepting one
	// would extend its life by however far ahead it claims to be.
	if nowUnix < issued || nowUnix-issued > ChallengeTTL {
		return 0, ErrChallenge
	}
	return userID, nil
}

// parseChallengeBody reads the signed triple.
func parseChallengeBody(body string) (userID, issued int64, ok bool) {
	parts := strings.Split(body, ":")
	if len(parts) != 3 {
		return 0, 0, false
	}
	userID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || userID <= 0 {
		return 0, 0, false
	}
	issued, err = strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return 0, 0, false
	}
	return userID, issued, true
}

// sign is the one MAC construction, so minting and opening cannot drift.
func sign(key, body []byte) []byte {
	mac := hmac.New(sha256.New, key)
	// hash.Hash documents Write as never returning an error.
	mac.Write(body) //nolint:errcheck // see above.
	return mac.Sum(nil)
}
