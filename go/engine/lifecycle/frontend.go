//go:build linux

// Serving the interface.
//
// The bundle is embedded at build time and mounted as the last thing on the
// application: every real route matches before the fallback does, and the
// fallback refuses the prefixes that own their own protocols, so an unknown
// API path can never come back as an HTML document.
package lifecycle

import (
	"fmt"
	"net/http"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/adaptor"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/server"
	"github.com/heavycaffeiner/stowcloud/go/engine/http/spa"
)

// spaPage is the interface document the door draws, when the build embeds
// one. Nil is a build with no frontend, which the door renders as an API-only
// screen rather than a broken page.
func spaPage() http.Handler {
	h, ok := spa.Handler()
	if !ok {
		return nil
	}
	return h
}

// mountFrontend installs the interface as the application's last match.
//
// Registered after everything else, so every real route wins first, and
// refusing the reserved prefixes itself, because a mount that declined a path
// and a fallback that then served it would hand a mistyped API call back to
// the browser as a document.
func (e *Engine) mountFrontend(app *fiber.App) error {
	h, ok := spa.Handler()
	if !ok {
		// A build with no bundle leaves the root unmounted: the router's own
		// 404 is the answer, and it says more than a placeholder page would.
		return nil
	}
	if err := server.InstallFallback(app, adaptor.HTTPHandler(h)); err != nil {
		return fmt.Errorf("installing the interface: %w", err)
	}
	return nil
}
