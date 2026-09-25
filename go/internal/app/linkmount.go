//go:build linux

package app

import (
	"context"
	"io"

	core "github.com/heavycaffeiner/stowcloud/go/internal/feature/files"
	filehttp "github.com/heavycaffeiner/stowcloud/go/internal/transport/http/files"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/links"
)

func (e *Engine) newPublicLinks() *links.Public {
	return links.NewPublic(links.PublicDeps{
		Core:       e.Core,
		State:      e.State,
		ClaimKey:   e.claimKey.Key,
		Limiter:    e.linkLimiter,
		Now:        e.now,
		ClientAddr: clientAddr,
		Audit: func(ctx context.Context, event, target, ip, ua string, ok bool) error {
			return e.Auth.Audit(ctx, nil, event, target, ip, ua, ok)
		},
		Logger:      e.log(),
		Frontend:    spaPage(),
		Fail:        fail,
		Refuse:      refuse,
		WriteJSON:   writeJSON,
		Decode:      decodeBody,
		CloseStream: func(stream *core.Stream, name string) { filehttp.CloseStream(stream, name, e.log()) },
		SendStream:  filehttp.SendStream,
		AcquireArchive: func() (func(), bool) {
			if !e.archiveGate.TryAcquire() {
				return nil, false
			}
			return e.archiveGate.Release, true
		},
		WriteArchive: func(ctx context.Context, w io.Writer, link core.Link, sub, name string) {
			if err := filehttp.BuildArchive(ctx, w, name, func(ctx context.Context, visit filehttp.ArchiveVisit) error {
				return e.Core.LinkArchiveWalk(ctx, link, sub, visit)
			}, e.log()); err != nil {
				e.log().Warn("a link archive ended early", "name", name, "error", err)
			}
		},
	})
}
