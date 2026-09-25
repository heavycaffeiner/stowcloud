//go:build linux && !compat_nc

package app

import "github.com/gin-gonic/gin"

func (e *Engine) declarePublicLinkAliases(*gin.Engine) {}

// A build without the tag carries no compatibility surface, so the mount
// claims nothing and the paths fall through to whatever else answers them.
func (e *Engine) mountNCTagged(*gin.Engine) {}

// No compatibility surface means no direct stream, so no route belongs to a
// content host and a named one serves nothing.
func (e *Engine) contentRoute(string, string) bool { return false }
