//go:build linux

// The repair door.
//
// It is mounted outside the middleware chain on purpose. This is the screen an
// operator reaches when the deployment is misconfigured, and a repair screen
// behind the guard it exists to repair is a screen nobody can open: a wrong
// app host or a broken proxy set would refuse the very request that fixes it.
//
// What guards it instead is its own check on the resolved peer address, and it
// serves exactly four things: login, read the settings, write one section,
// restart. There is no file browsing and no account management behind it.
package app

import (
	"context"
	"errors"
	"net/http"
	"net/netip"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/heavycaffeiner/stowcloud/go/internal/feature/auth"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/emergency"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/middleware"
)

// emergencyAuthenticator adds the ordinary one-use recovery-code fallback to
// the repair door without creating a second password check. Login first asks
// the service to verify the password with no factor. Once that returns
// ErrSecondFactor, this wrapper applies the ordinary TOTP-first contract and
// then tries one-use recovery. The clock is supplied by the engine so the
// factor window agrees with the password flow.
type emergencyAuthenticator struct {
	*auth.Service
	now func() int64
}

func (a emergencyAuthenticator) Login(
	ctx context.Context, req auth.LoginRequest, ttl time.Duration,
) (auth.Session, error) {
	passwordOnly := req
	passwordOnly.Factor = ""
	session, err := a.Service.Login(ctx, passwordOnly, ttl)
	if err == nil || !errors.Is(err, auth.ErrSecondFactor) || req.Factor == "" {
		return session, err
	}

	userID, lookupErr := a.UserIDByName(ctx, req.Name)
	if lookupErr != nil {
		return session, lookupErr
	}

	accepted, factorErr := a.VerifyTOTP(ctx, userID, req.Factor, a.now())
	if factorErr != nil {
		return session, factorErr
	}
	if !accepted {
		accepted, factorErr = a.UseRecoveryCode(ctx, userID, req.Factor)
		if factorErr != nil {
			return session, factorErr
		}
	}
	if !accepted {
		a.Record(ctx, userID, auth.EventLogin, req.Name, req.IP, req.UA, false)
		return session, auth.ErrCredentials
	}
	return a.CreateSession(ctx, userID, req.IP, req.UA, req.AMR, ttl)
}

// mountEmergency claims the door's one prefix.
//
// Registered before the chain, because Gin runs middleware in registration
// order and the chain is what this has to sit in front of. Both exact and
// descendant paths are claimed so the repair door owns its whole surface.
func (e *Engine) mountEmergency(app *gin.Engine) {
	door := detachContext(emergency.Handler(emergency.Deps{
		Auth:       emergencyAuthenticator{Service: e.Auth, now: e.clk().Nanos},
		State:      e.State,
		Page:       spaPage(),
		DataDir:    e.dataDir,
		Reason:     func() string { return "" },
		ClientAddr: e.doorClient,
		Restart:    nil,
	}))
	handler := func(c *gin.Context) {
		door.ServeHTTP(c.Writer, c.Request)
		c.Abort()
	}
	app.Any(emergency.Prefix, handler)
	app.Any(emergency.Prefix+"/*path", handler)
}

// detachContext replaces the framework's request context with a plain one
// carrying the same deadline behaviour and none of its identity.
//
// The adaptor hands the door an http.Request whose context is the framework's
// own request object. Anything derived from it keeps a reference after the
// request is recycled, and the race detector sees the shutdown path writing
// the same field a derived context is still reading: measured, a data race on
// every door request. A plain context has no such field.
func detachContext(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r.WithContext(context.WithoutCancel(r.Context())))
	})
}

// doorClient resolves a caller for the door.
//
// It re-runs the chain's own resolution against the live trusted set rather
// than trusting a header, so a forwarded address is honoured only from a peer
// the deployment actually trusts. Falling back to the peer trusts no proxy,
// which is the safe direction for a screen guarded by address.
func (e *Engine) doorClient(r *http.Request) netip.Addr {
	peer, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return middleware.Unroutable()
	}
	return middleware.ClientAddr(peer.Addr(), e.trustedPrefixes(),
		r.Header.Get("CF-Connecting-IP"), r.Header.Get("X-Forwarded-For"))
}

// trustedPrefixes reads the deployment's proxy set under the lock the
// settings path writes it with.
func (e *Engine) trustedPrefixes() []netip.Prefix {
	e.settingsMu.RLock()
	defer e.settingsMu.RUnlock()
	return e.trusted
}
