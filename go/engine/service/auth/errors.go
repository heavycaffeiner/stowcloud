// Package auth is the three-tier verification path and the durable state
// around it: accounts, sessions, app passwords, second factors, recovery
// codes, groups, the audit log, the file-sharing credential, and the master
// key that seals every secret at rest.
//
// The question the package answers is how few times the memory-hard function
// must run, not how strong it is. A sync client sends hundreds of requests a
// minute carrying the same credential, and running Argon2id on each would
// make the server slower than the disk it fronts. The three tiers are that
// answer, and everything else hangs off them.
package auth

import "errors"

// The credential-failure surface. A single error represents every invalid
// credential, preventing distinctions that would turn into an account oracle.
// The second error is intentionally separate, since without it a client has no
// way to know it should prompt for a code.
var (
	// ErrCredentials is the single credential failure: an unknown account, a
	// wrong password, a wrong second factor, an expired session, a revoked
	// app password. One answer, by design.
	ErrCredentials = errors.New("the credentials were not accepted")

	// ErrSecondFactor is distinct on purpose. Reaching it requires the
	// password to have just verified, so it discloses nothing to a caller who
	// does not already hold it, and the client has to be told to ask.
	ErrSecondFactor = errors.New("a second factor is required")

	// ErrAccountDisabled is an account that exists and may not sign in. It is
	// answered only after the password verified, so it is not an oracle
	// either.
	ErrAccountDisabled = errors.New("the account is disabled")

	// ErrRateLimited is the login budget for one client address exhausted.
	ErrRateLimited = errors.New("too many attempts, try again later")

	// ErrWeakPassword is a password under MinPasswordLen. It lives here so
	// every path that stores one is covered, not only the ones a browser
	// reaches.
	ErrWeakPassword = errors.New("the password is too short")

	// ErrNameInvalid is an account name the one rule refuses. Its message
	// never echoes the input.
	ErrNameInvalid = errors.New("that account name is not allowed")

	// ErrNameTaken is a name another row already holds, mapped from the
	// store's typed refusal rather than a driver message.
	ErrNameTaken = errors.New("that name is already taken")

	// ErrLastAdmin refuses a write that would leave nobody who can sign in
	// and administer.
	ErrLastAdmin = errors.New("that would leave no administrator who can sign in")

	// ErrInvalidQuota is a cap that is not a cap. Zero or negative bytes
	// would leave the account unable to write anything, which is what
	// disabling it is for; unlimited is an absent cap, not a zero one.
	ErrInvalidQuota = errors.New("a quota must be greater than zero, or absent for unlimited")

	// ErrNotFound is a row the caller named that does not exist and is not a
	// credential: a group, an app password, a session of somebody else's.
	ErrNotFound = errors.New("no such record")
)

// MinPasswordLen is the floor for any password this service stores. The
// first-run bootstrap applies the same one, so an account created afterwards
// cannot be weaker than the first.
const MinPasswordLen = 10
