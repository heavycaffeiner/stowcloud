//go:build linux && compat_nc

package nc

import (
	"github.com/gofiber/fiber/v2"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/middleware"
)

// Login Flow v2: a client opens a browser tab, a person approves it there,
// and the client collects a credential by polling. Every step here is plain
// JSON with no OCS envelope, because a client that has not signed in yet does
// not know that vocabulary.
//
// s.deps.Flow is nil where the feature is switched off, and every entry point
// below answers 404 in that case rather than a document describing a flow
// that cannot complete.

// loginBegin starts a device login.
func (s *Server) loginBegin(c *fiber.Ctx) error {
	if s.deps.Flow == nil {
		return c.SendStatus(fiber.StatusNotFound)
	}
	origin := s.deps.Origin(originRequestOf(c))
	tokens, err := s.deps.Flow.Begin(c.UserContext(), origin)
	if err != nil {
		return c.SendStatus(fiber.StatusInternalServerError)
	}
	// The two URLs are this vocabulary's own, so they are spelled here rather
	// than by the service that minted the tokens.
	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"poll": fiber.Map{
			"token":    tokens.PollToken,
			"endpoint": origin + frontPrefix + loginPollPath,
		},
		"login": origin + frontPrefix + "/login/v2/flow/" + tokens.LoginToken,
	})
}

// loginPoll delivers the credential once a person has approved it.
//
// A flow still waiting answers 404 with an empty body: that is the "not yet"
// a client polls against, once a second, forever, so this path stays cheap
// and silent. Any other failure answers the same way, since distinguishing
// them would tell a stranger holding a bare token which ones are live.
func (s *Server) loginPoll(c *fiber.Ctx) error {
	if s.deps.Flow == nil {
		return c.SendStatus(fiber.StatusNotFound)
	}
	token := c.FormValue("token")
	if token == "" {
		token = c.Query("token")
	}
	origin := s.deps.Origin(originRequestOf(c))
	delivery, err := s.deps.Flow.Poll(c.UserContext(), token, origin)
	if err != nil {
		return c.SendStatus(fiber.StatusNotFound)
	}
	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"server":      delivery.Server,
		"loginName":   delivery.LoginName,
		"appPassword": delivery.AppPassword,
	})
}

// loginGrant records a person's approval of a pending device login.
//
// Reached only from the consent page's own form submission. The chain has
// already enforced CSRF for a cookie-authenticated mutating request, so this
// checks only that the credential is a session cookie at all: a device
// credential must never be able to approve its own pairing, or a stolen app
// password could mint another one indefinitely.
func (s *Server) loginGrant(c *fiber.Ctx) error {
	if s.deps.Flow == nil {
		return c.SendStatus(fiber.StatusNotFound)
	}
	p, ok := principalOf(c)
	if !ok || p.Kind != middleware.CredentialSessionCookie {
		return c.SendStatus(fiber.StatusForbidden)
	}
	token := c.FormValue("token")
	login := s.loginNameOf(c.UserContext(), p)
	if err := s.deps.Flow.Approve(c.UserContext(), token, p.UserID, login); err != nil {
		return c.SendStatus(fiber.StatusForbidden)
	}
	return c.SendStatus(fiber.StatusNoContent)
}

// loginConsent renders the page a person approves a device login on.
//
// The page itself, its script nonce, its CSRF value and its redirect for a
// visitor with no session are all the assembly's concern; this surface owns
// only the URL. Nil answers 404 rather than a blank page.
func (s *Server) loginConsent(c *fiber.Ctx) error {
	if s.deps.ConsentPage == nil {
		return c.SendStatus(fiber.StatusNotFound)
	}
	return s.deps.ConsentPage(c)
}

// originRequestOf reads what the origin renderer needs from one request.
func originRequestOf(c *fiber.Ctx) OriginRequest {
	return OriginRequest{
		Host:           c.Hostname(),
		ForwardedHost:  c.Get("X-Forwarded-Host"),
		ForwardedProto: c.Get("X-Forwarded-Proto"),
		PeerAddr:       c.IP(),
		TLS:            c.Protocol() == "https",
	}
}
