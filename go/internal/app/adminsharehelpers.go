//go:build linux

package app

import (
	"github.com/gin-gonic/gin"

	core "github.com/heavycaffeiner/stowcloud/go/internal/feature/files"
	"github.com/heavycaffeiner/stowcloud/go/internal/feature/shares/acl"
	num "github.com/heavycaffeiner/stowcloud/go/internal/platform/number"
)

// shareIDOf reads a path share id without allowing a wider decimal to wrap.
func shareIDOf(c *gin.Context) (core.ShareID, bool) {
	raw, ok := pathID(c)
	if !ok {
		return 0, false
	}
	narrowed, err := num.Narrow[uint32](raw)
	if err != nil {
		return 0, false
	}
	return core.ShareID(narrowed), true
}

// permsOf converts the complete permission vocabulary, refusing unknown names.
func permsOf(names []string) (acl.Perms, bool) {
	var out acl.Perms
	for _, name := range names {
		bit, known := acl.PermByName(name)
		if !known {
			return 0, false
		}
		out |= bit
	}
	return out, true
}
