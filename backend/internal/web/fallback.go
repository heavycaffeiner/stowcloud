// Linux only, because it serves a Linux-only engine.
//go:build linux

package web

import (
	"fmt"
	"strings"

	"github.com/gin-gonic/gin"
)

// ReservedPrefixes are the mounts the interface fallback may never answer for.
func ReservedPrefixes() []string {
	return []string{"/api", "/dav", "/remote.php", "/ocs", "/s", "/c", "/emergency"}
}

// IsReserved reports whether a path belongs to a mount the fallback must not claim.
func IsReserved(path string) bool {
	for _, prefix := range ReservedPrefixes() {
		if underPrefix(path, prefix) {
			return true
		}
	}
	return false
}

func underPrefix(path, prefix string) bool {
	p := strings.TrimRight(path, "/")
	pre := strings.TrimRight(prefix, "/")
	if p == pre {
		return true
	}
	return len(p) > len(pre) && strings.HasPrefix(p, pre) && p[len(pre)] == '/'
}

// InstallFallback mounts the interface shell for unmatched paths.
func InstallFallback(app *gin.Engine, shell gin.HandlerFunc) error {
	if shell == nil {
		return fmt.Errorf("the interface fallback needs a handler")
	}
	app.NoRoute(func(c *gin.Context) {
		if IsReserved(c.Request.URL.Path) {
			c.JSON(404, gin.H{"error": "not_found"})
			return
		}
		shell(c)
	})
	return nil
}
