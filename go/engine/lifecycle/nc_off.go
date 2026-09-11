//go:build linux && !compat_nc

package lifecycle

import "github.com/gofiber/fiber/v2"

func (e *Engine) declarePublicLinkAliases(*fiber.App) {}

// A build without the tag carries no compatibility surface, so the mount
// claims nothing and the paths fall through to whatever else answers them.
func (e *Engine) mountNCTagged(*fiber.App) {}

// No compatibility surface means no direct stream, so no route belongs to a
// content host and a named one serves nothing.
func (e *Engine) contentRoute(string, string) bool { return false }
