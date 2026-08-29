//go:build linux

// The account family: what a signed-in person can see and revoke about their
// own credentials.
//
// Everything here is scoped to the caller. No route takes an account as an
// argument, because the identity is what the chain already proved and an
// account parameter is how one person reads another's sessions.
package lifecycle

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"

	"github.com/gofiber/fiber/v2"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/apierr"
	"github.com/heavycaffeiner/stowcloud/go/engine/http/handler"
	"github.com/heavycaffeiner/stowcloud/go/engine/http/middleware"
)

// accountSessions lists the caller's live sessions.
func (e *Engine) accountSessions(c *fiber.Ctx) error {
	owner, ok := ownerOf(c)
	if !ok {
		return refuse(c, apierr.Classified{Class: apierr.AuthRequired})
	}

	rows, err := e.Auth.Sessions(c.UserContext(), int64(owner))
	if err != nil {
		return fail(c, err)
	}

	// The session making this request is marked, so a person revoking one can
	// see which is theirs. The stored digest never leaves: the view compares
	// derived handles, and this is nil when the caller authenticated with an
	// app password, which has no session row to mark.
	//
	// The digest is computed here rather than read from the chain because the
	// chain keeps the principal, not the credential. It used to pass nil
	// unconditionally, which marked nothing while the comment said otherwise:
	// every row came back current:false, and a screen offering "sign out my
	// other devices" would have signed the person out of the one they were
	// using.
	return writeJSON(c, fiber.StatusOK, handler.SessionsOf(rows, currentDigest(c)))
}

// currentDigest is the stored digest of the session this request presented,
// or nil when it presented something else.
func currentDigest(c *fiber.Ctx) []byte {
	cookie := c.Cookies(middleware.SessionCookieName)
	if cookie == "" {
		return nil
	}
	raw, err := hex.DecodeString(cookie)
	if err != nil {
		return nil
	}
	sum := sha256.Sum256(raw)
	return sum[:]
}

// accountAppPasswords lists the caller's app passwords.
//
// The tokens are not stored, so no listing can return one. What a person sees
// is what they named it, when it was made and what it may do.
func (e *Engine) accountAppPasswords(c *fiber.Ctx) error {
	owner, ok := ownerOf(c)
	if !ok {
		return refuse(c, apierr.Classified{Class: apierr.AuthRequired})
	}

	rows, err := e.Auth.AppPasswords(c.UserContext(), int64(owner))
	if err != nil {
		return fail(c, err)
	}
	return writeJSON(c, fiber.StatusOK, handler.AppPasswordsOf(rows))
}

// accountAppPasswordDelete revokes one of the caller's app passwords.
//
// The account is the caller's, not a parameter: the service takes both, so a
// row belonging to someone else is refused by the service rather than by a
// check here that could be forgotten.
func (e *Engine) accountAppPasswordDelete(c *fiber.Ctx) error {
	owner, ok := ownerOf(c)
	if !ok {
		return refuse(c, apierr.Classified{Class: apierr.AuthRequired})
	}
	id, ok := pathID(c)
	if !ok {
		return notFound(c)
	}

	if err := e.Auth.RevokeAppPassword(c.UserContext(), int64(owner), id); err != nil {
		return fail(c, err)
	}
	return c.SendStatus(fiber.StatusNoContent)
}

// pathID reads a decimal id from the path.
//
// Decimal on the wire for the same reason a job id is: a JavaScript number
// loses exactness past 2^53, so an id a client round-trips comes back
// different.
func pathID(c *fiber.Ctx) (int64, bool) {
	n, err := strconv.ParseInt(c.Params("id"), 10, 64)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}
