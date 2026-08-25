package auth

import "errors"

// The credential-failure surface. One error covers every wrong credential so
// that distinguishing them is not an account oracle; a second one is
// deliberately distinct because a client cannot prompt for a code without it.
var (
	// ErrCredentials is the single credential-failure error: unknown user,
	// wrong password, wrong second factor, expired session.
	ErrCredentials = errors.New("the credentials were not accepted")

	// ErrSecondFactor is distinct on purpose: reaching it requires the
	// password to have just verified, so it discloses nothing an attacker did
	// not already have, and the client must be told to ask for a code.
	ErrSecondFactor = errors.New("a second factor is required")

	// ErrAccountDisabled is a disabled account. Named distinctly from the
	// WebDAV resource lock error so the two packages' sentinels do not get
	// conflated.
	ErrAccountDisabled = errors.New("the account is disabled")

	// ErrRateLimited is the login bucket exhausted, or too many second-factor
	// attempts.
	ErrRateLimited = errors.New("too many attempts, try again later")

	// ErrLastAdmin is a write that would leave the deployment with no
	// administrator anybody can sign in as. Recovering from that state means
	// editing the database by hand, so the write is refused.
	ErrLastAdmin = errors.New("that would leave no administrator who can sign in")

	// ErrWeakPassword is a password shorter than MinPasswordLen. It lives in
	// this package so that every path that stores a password is covered, not
	// just the ones a browser reaches.
	ErrWeakPassword = errors.New("the password is too short")
)

// MinPasswordLen is the floor for any password this service stores. It is the
// same floor the first-run bootstrap applies, so an account created afterwards
// cannot be weaker than the first one.
const MinPasswordLen = 10
