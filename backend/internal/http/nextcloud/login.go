//go:build linux && compat_nc

package nc

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/heavycaffeiner/stowcloud/backend/internal/http/middleware"
)

func (s *Server) loginBegin(c *gin.Context) {
	if s.deps.Flow == nil {
		c.Status(http.StatusNotFound)
		return
	}
	origin := s.deps.Origin(originRequestOf(c))
	tokens, err := s.deps.Flow.Begin(c.Request.Context(), origin)
	if err != nil {
		c.Status(http.StatusInternalServerError)
		return
	}
	c.JSON(http.StatusOK, gin.H{"poll": gin.H{"token": tokens.PollToken, "endpoint": origin + frontPrefix + loginPollPath}, "login": origin + frontPrefix + "/login/v2/flow/" + tokens.LoginToken})
}

func (s *Server) loginPoll(c *gin.Context) {
	if s.deps.Flow == nil {
		c.Status(http.StatusNotFound)
		return
	}
	token := c.Request.FormValue("token")
	if token == "" {
		token = c.Query("token")
	}
	origin := s.deps.Origin(originRequestOf(c))
	delivery, err := s.deps.Flow.Poll(c.Request.Context(), token, origin)
	if err != nil {
		c.Status(http.StatusNotFound)
		return
	}
	c.JSON(http.StatusOK, gin.H{"server": delivery.Server, "loginName": delivery.LoginName, "appPassword": delivery.AppPassword})
}

func (s *Server) loginGrant(c *gin.Context) {
	if s.deps.Flow == nil {
		c.Status(http.StatusNotFound)
		return
	}
	p, ok := principalOf(c)
	if !ok || p.Kind != middleware.CredentialSessionCookie {
		c.Status(http.StatusForbidden)
		return
	}
	token := c.Request.FormValue("token")
	login := s.loginNameOf(c.Request.Context(), p)
	if err := s.deps.Flow.Approve(c.Request.Context(), token, p.UserID, login); err != nil {
		c.Status(http.StatusForbidden)
		return
	}
	c.Status(http.StatusNoContent)
}

func (s *Server) loginConsent(c *gin.Context) {
	if s.deps.ConsentPage == nil {
		c.Status(http.StatusNotFound)
		return
	}
	s.deps.ConsentPage(c)
}

func originRequestOf(c *gin.Context) OriginRequest {
	return OriginRequest{Host: c.Request.Host, ForwardedHost: c.GetHeader("X-Forwarded-Host"), ForwardedProto: c.GetHeader("X-Forwarded-Proto"), PeerAddr: c.ClientIP(), TLS: c.Request.TLS != nil}
}
