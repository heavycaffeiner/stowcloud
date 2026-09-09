//go:build linux && compat_nc

package nc

import "github.com/gofiber/fiber/v2"

// The two answers a client reads before it has a credential.
//
// Both are addressed by every client that has never heard of this deployment,
// so neither carries anything beyond what a stranger could already infer: a
// version number and a product name repeated in the capabilities document
// once the caller signs in.

// status answers the pre-sign-in probe.
//
// Plain JSON, no OCS envelope: none of these clients know that vocabulary
// yet. The Android client parses "version" with an unguarded split on dots,
// so it is rendered as the four-part form here while "versionstring" stays
// the three-part one the capabilities document also reports. The product
// name must never contain "owncloud": the iOS client greps for that
// substring and refuses to proceed if it finds it.
func (s *Server) status(c *fiber.Ctx) error {
	f := s.deps.Features()
	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"installed":       true,
		"maintenance":     false,
		"needsDbUpgrade":  false,
		"version":         f.Version + ".1",
		"versionstring":   f.Version,
		"edition":         "",
		"productname":     "Nextcloud",
		"extendedSupport": false,
		"instanceid":      f.InstanceID,
	})
}

// probe answers the captive-portal check.
//
// 204 with no body, and never a redirect: the desktop client treats a
// 301..307 here as a captive portal and aborts the whole connection check
// before it ever tries the real server.
func (s *Server) probe(c *fiber.Ctx) error {
	return c.SendStatus(fiber.StatusNoContent)
}
