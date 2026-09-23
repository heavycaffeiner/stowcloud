//go:build linux

// The auth family: getting a session, holding one, and giving it up.
//
// Signing in is two requests when the account has a second factor, and the
// server carries the accepted password between them in a signed challenge
// rather than a stored row. Nothing here decides whether a credential is good:
// that is the auth service's, and this reads its answer.
package app

import (
	"encoding/hex"
	"math"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	core "github.com/heavycaffeiner/stowcloud/go/internal/feature/files"
	"github.com/heavycaffeiner/stowcloud/go/internal/kit/limits"
	"github.com/heavycaffeiner/stowcloud/go/internal/kit/secret"
	"github.com/heavycaffeiner/stowcloud/go/internal/platform/storage/objstore"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/apierr"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/handler"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/middleware"
)

// defaultUploadParallel is how many chunks a client is told to send at once.
//
// A client-side hint, not a server limit: the server accepts what arrives and
// the rate limiter decides the rest. Four keeps a single upload from occupying
// every connection a browser will open to one origin.
const defaultUploadParallel = 4

// rootViews lists the folders this account can reach.
// Never nil: an account with no grants answers an empty list.
func (e *Engine) rootViews(owner core.UserID) []handler.RootView {
	roots := e.Core.Roots(owner)
	out := make([]handler.RootView, 0, len(roots))
	for _, r := range roots {
		out = append(out, handler.RootView{
			Label:            r.Label,
			Perms:            core.PermNames(r.Perms),
			SharedExternally: r.SharedExternally,
			TrashEnabled:     r.TrashEnabled,
			BrokenReason:     r.BrokenReason,
		})
	}
	return out
}

// limitsView reports what an upload may do, read from the running engine
// rather than from configuration: an operator who changed the chunk size gets
// the value in force, not the one the process started with.
func (e *Engine) limitsView() handler.LimitsView {
	if e.Upload == nil {
		// No resumable transfer on this deployment. The floor and the default
		// are still reported, because a client plans against them before it
		// discovers the route is absent.
		return handler.LimitsView{
			ChunkSize: limits.UploadChunkSizeDefault,
			ChunkMin:  limits.UploadChunkMinDefault,
			Parallel:  defaultUploadParallel,
		}
	}
	minBytes, defaultBytes := e.Upload.Settings().Snapshot()
	return handler.LimitsView{
		ChunkSize: chunkBytes(defaultBytes),
		ChunkMin:  chunkBytes(minBytes),
		Parallel:  defaultUploadParallel,
	}
}

// chunkBytes narrows a configured size for the wire. A value past the signed
// range cannot be a real chunk size, and reporting a negative one would have a
// client plan an upload against nonsense.
func chunkBytes(v uint64) int64 {
	if v > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(v)
}

func (e *Engine) featuresView() handler.FeaturesView {
	directUploads := false
	for _, share := range e.Core.Shares() {
		if share.BrokenReason != "" || share.Backend != core.BackendS3 {
			continue
		}
		if root, ok := e.Core.ShareRoot(share.ID); ok {
			if provider, ok := root.(objstore.DirectTransferProvider); ok && provider.DirectTransfer() {
				directUploads = true
				break
			}
		}
	}
	return handler.FeaturesView{
		WebDAV: true, SMB: e.smbPublisherOf() != nil, Preview: e.thumbnailEnabled(),
		Trash: true, Shares: true, Search: searchTierName(e.Search.HasIndex()), DirectUploads: directUploads,
	}
}

// searchTierName names the tier a query would run on right now.
func searchTierName(hasIndex bool) string {
	if hasIndex {
		return "name"
	}
	return "walk"
}

// sessionCookieMaxAge bounds the cookie in the browser. Shorter than the
// server's absolute window on purpose: the browser forgetting first costs a
// sign-in, while the server forgetting first would leave a cookie presenting
// a session that no longer exists.
const sessionCookieMaxAge = 30 * 24 * time.Hour

// setSessionCookie is the one place the attributes are written, so the two
// sign-in paths cannot disagree about them.
//
// The __Host- prefix is part of the name and the browser enforces what it
// implies: Secure, Path=/, and no Domain. SameSite=Lax rather than Strict
// because Strict withholds the cookie on a top-level navigation back into the
// app, which reads as being signed out to anyone arriving from a link.
func (e *Engine) setSessionCookie(c *gin.Context, printable string) {
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(middleware.SessionCookieName, printable, int(sessionCookieMaxAge/time.Second), "/", "", true, true)
}

// failKnown renders an error whose subject the caller already knows about.
//
// The auth family is where Hidden is wrong: a caller signing in named the
// account themselves, so rendering a disabled account as not-found tells them
// nothing they did not supply and hides an answer they can act on.
func failKnown(c *gin.Context, err error) {
	refuse(c, apierr.Classify(err, apierr.VisibilityKnown))
}

// clientAddr is the address the chain resolved, as the audit log records it.
func clientAddr(c *gin.Context) string {
	return middleware.ClientOf(c).String()
}

// printableToken renders a session token for the cookie.
//
// Hex rather than the secret's String, which redacts: a redacted cookie value
// is a session nobody can present. The credential step decodes the same way.
func printableToken(t secret.Secret) string {
	return hex.EncodeToString(t.Reveal())
}
