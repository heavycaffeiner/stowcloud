//go:build linux

// Serving the interface.
//
// The bundle is embedded at build time and mounted as the last thing on the
// application: every real route matches before the fallback does, and the
// fallback refuses the prefixes that own their own protocols, so an unknown
// API path can never come back as an HTML document.
package spa

import (
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/heavycaffeiner/stowcloud/backend/internal/http/server"
)

// Page returns the embedded interface, or nil when no bundle is present.
func Page() http.Handler {
	h, ok := Handler()
	if !ok {
		return nil
	}
	return h
}

// Install mounts the interface as the application's last match.
//
// Registered after everything else, so every real route wins first, and
// refusing the reserved prefixes itself, because a mount that declined a path
// and a fallback that then served it would hand a mistyped API call back to
// the browser as a document.
func Install(app *gin.Engine) error {
	shell := func(c *gin.Context) {
		if h, ok := Handler(); ok {
			h.ServeHTTP(c.Writer, c.Request)
			c.Abort()
			return
		}
		c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": "not_found"})
	}
	if err := server.InstallFallback(app, shell); err != nil {
		return fmt.Errorf("installing the interface: %w", err)
	}
	return nil
}
