//go:build linux

// Shared account path parsing for app-owned routes.
package app

import (
	"strconv"

	"github.com/gin-gonic/gin"
)

func pathID(c *gin.Context) (int64, bool) {
	n, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}
