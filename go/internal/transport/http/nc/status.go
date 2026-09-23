//go:build linux && compat_nc

package nc

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

func (s *Server) status(c *gin.Context) {
	f := s.deps.Features()
	c.JSON(http.StatusOK, gin.H{"installed": true, "maintenance": false, "needsDbUpgrade": false, "version": f.Version + ".1", "versionstring": f.Version, "edition": "", "productname": "Nextcloud", "extendedSupport": false, "instanceid": f.InstanceID})
}

func (s *Server) probe(c *gin.Context) { c.Status(http.StatusNoContent) }
