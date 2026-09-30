//go:build linux

// Serving the constructed engine.
package app

import (
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/heavycaffeiner/stowcloud/backend/internal/files"
	filehttp "github.com/heavycaffeiner/stowcloud/backend/internal/http/api/files"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/api/handler"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/publiclinks"
	"github.com/heavycaffeiner/stowcloud/backend/internal/jobs"
	"github.com/heavycaffeiner/stowcloud/backend/internal/server/middleware"
	"github.com/heavycaffeiner/stowcloud/backend/internal/web"
)

// Mount assembles the native Gin router over a constructed engine.
func (e *Engine) Mount(router *gin.Engine) error {
	if router == nil {
		return fmt.Errorf("mounting routes: Gin engine is nil")
	}
	periodic := e.tasks()
	if err := jobs.Validate(periodic); err != nil {
		return fmt.Errorf("mounting routes: %w", err)
	}
	if err := e.routes(router); err != nil {
		return err
	}
	e.startTasks(periodic)
	return nil
}

func (e *Engine) newPublicLinks() *publiclinks.Public {
	return publiclinks.NewPublic(publiclinks.PublicDeps{
		Core:       e.Core,
		State:      e.State,
		ClaimKey:   e.claimKey.Key,
		Limiter:    e.linkLimiter,
		Now:        e.now,
		ClientAddr: handler.ClientAddr,
		Audit: func(ctx context.Context, event, target, ip, ua string, ok bool) error {
			return e.Auth.Audit(ctx, nil, event, target, ip, ua, ok)
		},
		Logger:          e.log(),
		Frontend:        web.Page(),
		Fail:            handler.Fail,
		Refuse:          handler.Refuse,
		WriteJSON:       func(c *gin.Context, status int, v any) { c.JSON(status, v) },
		Decode:          filehttp.Decode,
		CloseStream:     func(stream *files.Stream, name string) { filehttp.CloseStream(stream, name, e.log()) },
		SendStreamRange: filehttp.SendStreamRange,
		AcquireArchive: func() (func(), bool) {
			if !e.archiveGate.TryAcquire() {
				return nil, false
			}
			return e.archiveGate.Release, true
		},
		WriteArchive: func(ctx context.Context, w io.Writer, link files.Link, sub, name string) {
			if err := filehttp.BuildArchive(ctx, w, name, func(ctx context.Context, visit filehttp.ArchiveVisit) error {
				return e.Core.LinkArchiveWalk(ctx, link, sub, visit)
			}, e.log()); err != nil {
				e.log().Warn("a link archive ended early", "name", name, "error", err)
			}
		},
	})
}

// health answers the probe.
//
// Only to a client on a private network, which is what the container's own
// check is: the probe runs inside the deployment and reaches the server over
// the loopback. To anyone else the address is not there, for the reason the
// rest of this API is not there to them. Liveness is a small thing to leak,
// and a surface that answers one stranger answers every scanner.
func (e *Engine) health(c *gin.Context) {
	if !middleware.IsPrivateClient(middleware.ClientOf(c)) {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	var reasons []handler.HealthReason
	status := handler.HealthOK
	if e.Journal == nil {
		status = handler.HealthDegraded
		reasons = append(reasons, handler.ReasonJournalDatabase)
	}

	h := handler.HealthOf(status, reasons)
	h.Revision = e.Revision
	c.JSON(http.StatusOK, h)
}

func (e *Engine) guardDavLock(ctx context.Context, share uint32, path string, principal int64) error {
	if e.davLocks == nil {
		return nil
	}
	return e.davLocks.Guard(ctx, share, path, principal, nil)
}
