//go:build linux

// Serving the constructed engine.
package server

import (
	"context"
	"fmt"
	"io"

	"github.com/gin-gonic/gin"

	"github.com/heavycaffeiner/stowcloud/backend/internal/files"
	"github.com/heavycaffeiner/stowcloud/backend/internal/jobs"
	"github.com/heavycaffeiner/stowcloud/backend/internal/server/apierr"
	"github.com/heavycaffeiner/stowcloud/backend/internal/server/middleware"
	"github.com/heavycaffeiner/stowcloud/backend/internal/shares"
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

func (e *Engine) newPublicLinks() *shares.Public {
	return shares.NewPublic(shares.PublicDeps{
		Core:     e.Core,
		State:    e.State,
		ClaimKey: e.claimKey.Key,
		Limiter:  e.linkLimiter,
		Now:      e.now,
		Audit: func(ctx context.Context, event, target, ip, ua string, ok bool) error {
			return e.Auth.Audit(ctx, nil, event, target, ip, ua, ok)
		},
		Logger:          e.logger,
		Frontend:        web.Page(),
		CloseStream:     func(stream *files.Stream, name string) { files.CloseStream(stream, name, e.logger) },
		SendStreamRange: files.SendStreamRange,
		AcquireArchive:  e.acquireArchive,
		WriteArchive: func(ctx context.Context, w io.Writer, link files.Link, sub, name string) {
			if err := files.BuildArchive(ctx, w, name, func(ctx context.Context, visit files.ArchiveVisit) error {
				return e.Core.LinkArchiveWalk(ctx, link, sub, visit)
			}, e.logger); err != nil {
				e.logger.Warn("a link archive ended early", "name", name, "error", err)
			}
		},
	})
}

// acquireArchive takes a slot of the gate every archive build and listing
// shares.
func (e *Engine) acquireArchive() (func(), bool) {
	if !e.archiveGate.TryAcquire() {
		return nil, false
	}
	return e.archiveGate.Release, true
}

// health answers the probe.
//
// Only to a client on a private network, which is what the container's own
// check is: the probe runs inside the deployment and reaches the server over
// the loopback. To anyone else the address is not there, for the reason the
// rest of this API is not there to them. Liveness is a small thing to leak,
// and a surface that answers one stranger answers every scanner.
type healthOutput struct{ Body Health }

// health answers only a client on a private address; anyone else sees a 404.
func (e *Engine) health(ctx context.Context, _ *struct{}) (*healthOutput, error) {
	if !middleware.IsPrivateClient(middleware.ClientFrom(ctx)) {
		return nil, apierr.AsClassified(apierr.NotFound, "")
	}

	var reasons []HealthReason
	status := HealthOK
	if e.Journal == nil {
		status = HealthDegraded
		reasons = append(reasons, ReasonJournalDatabase)
	}

	h := HealthOf(status, reasons)
	h.Revision = e.Revision
	return &healthOutput{Body: h}, nil
}

func (e *Engine) guardDavLock(ctx context.Context, share uint32, path string, principal int64) error {
	if e.davLocks == nil {
		return nil
	}
	return e.davLocks.Guard(ctx, share, path, principal, nil)
}
