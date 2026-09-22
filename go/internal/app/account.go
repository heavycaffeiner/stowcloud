//go:build linux

// The account family: what a signed-in person can see and revoke about their
// own credentials.
//
// Everything here is scoped to the caller. No route takes an account as an
// argument, because the identity is what the chain already proved and an
// account parameter is how one person reads another's sessions.
package app

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/apierr"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/handler"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/middleware"
)

// accountSessions lists the caller's live sessions.
func (e *Engine) accountSessions(c *gin.Context) {
	owner, ok := ownerOf(c)
	if !ok {
		refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	rows, err := e.Auth.Sessions(c.Request.Context(), int64(owner))
	if err != nil {
		fail(c, err)
		return
	}
	writeJSON(c, http.StatusOK, handler.SessionsOf(rows, currentDigest(c)))
}

// currentDigest is the stored digest of the session this request presented,
// or nil when it presented something else.
func currentDigest(c *gin.Context) []byte {
	cookie, err := c.Cookie(middleware.SessionCookieName)
	if err != nil || cookie == "" {
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
func (e *Engine) accountAppPasswords(c *gin.Context) {
	owner, ok := ownerOf(c)
	if !ok {
		refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	rows, err := e.Auth.AppPasswords(c.Request.Context(), int64(owner))
	if err != nil {
		fail(c, err)
		return
	}
	writeJSON(c, http.StatusOK, handler.AppPasswordsOf(rows))
}

// accountAppPasswordDelete revokes one of the caller's app passwords.
//
// The account is the caller's, not a parameter: the service takes both, so a
// row belonging to someone else is refused by the service rather than by a
// check here that could be forgotten.
func (e *Engine) accountAppPasswordDelete(c *gin.Context) {
	owner, ok := ownerOf(c)
	if !ok {
		refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	id, ok := pathID(c)
	if !ok {
		notFound(c)
		return
	}
	if err := e.Auth.RevokeAppPassword(c.Request.Context(), int64(owner), id); err != nil {
		fail(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func pathID(c *gin.Context) (int64, bool) {
	n, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}
