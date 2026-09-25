//go:build linux

package emergency

import (
	"context"
	"net/http"
	"net/netip"

	"github.com/gin-gonic/gin"

	"github.com/heavycaffeiner/stowcloud/backend/internal/http/middleware"
)

// Mount registers the emergency door before the product middleware chain.
func Mount(app *gin.Engine, d Deps) {
	door := detachContext(Handler(d))
	bridge := func(c *gin.Context) {
		door.ServeHTTP(c.Writer, c.Request)
		c.Abort()
	}
	app.Any(Prefix, bridge)
	app.Any(Prefix+"/*path", bridge)
}

// ClientAddr resolves the request peer through the live trusted proxy set.
func ClientAddr(trusted func() []netip.Prefix) func(*http.Request) netip.Addr {
	return func(r *http.Request) netip.Addr {
		peer, err := netip.ParseAddrPort(r.RemoteAddr)
		if err != nil {
			return middleware.Unroutable()
		}
		return middleware.ClientAddr(peer.Addr(), trusted(),
			r.Header.Get("CF-Connecting-IP"), r.Header.Get("X-Forwarded-For"))
	}
}

// A detached context avoids retaining Gin's recycled request object.
func detachContext(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r.WithContext(context.WithoutCancel(r.Context())))
	})
}
