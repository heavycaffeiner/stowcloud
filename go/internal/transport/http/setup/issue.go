//go:build linux

// The setup token is published before serving the first-run routes.
package setup

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/server"
	fsatomic "github.com/stowcloud/durablefs"
)

const setupTokenFile = "setup-token"

func IssueToken(ctx context.Context, gate *server.SetupGate, dataDir string, logger *slog.Logger) {
	if gate == nil {
		return
	}
	open, err := gate.Open(ctx)
	if err != nil || !open {
		return
	}
	token, ierr := gate.Issue(ctx)
	if ierr != nil {
		logger.Error("no first-run setup token could be issued", "error", ierr)
		return
	}
	path := filepath.Join(dataDir, setupTokenFile)
	res, werr := fsatomic.ReplaceFileDurable(path, 0o600, func(f *os.File) error {
		_, w := f.WriteString(token + "\n")
		return w
	})
	if werr != nil {
		message := "the first-run setup token could not be durably published to data directory"
		if res.Outcome == fsatomic.NotPublished {
			message = "the first-run setup token could not be written to data directory"
		}
		logger.Warn(message, "path", path, "outcome", res.Outcome.String(), "error", werr)
	}
	logger.Info("this deployment needs setting up; initial setup token issued",
		"setup_token", token, "valid_for", server.SetupTokenLifetime.String())
}
