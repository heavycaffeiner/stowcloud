//go:build linux && !compat_nc

package lifecycle

import "github.com/gofiber/fiber/v2"

// mountCompatTagged mounts nothing: a build without the compatibility tag
// serves no second product's paths, and the client that asks for one reads a
// 404 from the router rather than a half-served surface.
func (e *Engine) mountCompatTagged(*fiber.App) {}
