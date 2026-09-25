//go:build linux

// First-run boot token issuance remains in app composition. Request handling
// lives in transport/http/setup so the application owns no HTTP setup logic.
package app

import (
	"context"
	"os"
	"path/filepath"

	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/server"
	fsatomic "github.com/stowcloud/durablefs"
)

const setupTokenFile = "setup-token"

func (e *Engine) issueSetupToken(ctx context.Context) {
	if e.setup == nil {
		return
	}
	open, err := e.setup.Open(ctx)
	if err != nil || !open {
		return
	}
	token, ierr := e.setup.Issue(ctx)
	if ierr != nil {
		e.logger.Error("no first-run setup token could be issued", "error", ierr)
		return
	}
	path := filepath.Join(e.dataDir, setupTokenFile)
	res, werr := fsatomic.ReplaceFileDurable(path, 0o600, func(f *os.File) error {
		_, w := f.WriteString(token + "\n")
		return w
	})
	if werr != nil {
		message := "the first-run setup token could not be durably published to data directory"
		if res.Outcome == fsatomic.NotPublished {
			message = "the first-run setup token could not be written to data directory"
		}
		e.logger.Warn(message, "path", path, "outcome", res.Outcome.String(), "error", werr)
	}
	e.logger.Info("this deployment needs setting up; initial setup token issued",
		"setup_token", token, "valid_for", server.SetupTokenLifetime.String())
}
