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
package lifecycle

import (
	"context"
	"net/http"
	"net/netip"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/adaptor"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/emergency"
	"github.com/heavycaffeiner/stowcloud/go/engine/http/middleware"
)

// mountEmergency claims the door's one prefix.
//
// Registered before the chain, because Fiber runs what was mounted in mount
// order and the chain is what this has to sit in front of. A door mounted
// after it would be refused by the boundary check it repairs.
func (e *Engine) mountEmergency(app *fiber.App) {
	door := adaptor.HTTPHandler(detachContext(emergency.Handler(emergency.Deps{
		Auth:  e.Auth,
		State: e.State,

		// The homes probe falls back to this when a submitted section names
		// no root of its own.
		DataDir: e.dataDir,

		// Empty: this is the always-on route on a healthy deployment, so
		// nobody was sent here and there is nothing to put in the banner. A
		// process with no engine at all is a different entrance that owns its
		// own reason.
		Reason: func() string { return "" },

		// The address the chain resolved, read the same way the chain reads
		// it. Deriving it again here would be a second opinion about who a
		// request is from, and the two only have to disagree once for the
		// door to admit somebody the rest of the server would refuse.
		ClientAddr: e.doorClient,

		// No restart: nothing in this process brings itself back, and a
		// button that claimed to would leave an operator waiting for a
		// server that never returns. The door says so instead.
		Restart: nil,
	})))

	app.Use(emergency.Prefix, func(c *fiber.Ctx) error {
		return door(c)
	})
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
