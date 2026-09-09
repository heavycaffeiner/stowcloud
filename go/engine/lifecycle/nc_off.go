//go:build linux && !compat_nc

package lifecycle

import "github.com/gofiber/fiber/v2"

// A build without the tag carries no compatibility surface, so the mount
// claims nothing and the paths fall through to whatever else answers them.
func (e *Engine) mountNCTagged(*fiber.App) {}
