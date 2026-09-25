//go:build linux

package sandbox

import (
	"errors"
	"log/slog"
	"os"

	"github.com/heavycaffeiner/stowcloud/backend/internal/feature/preview/worker"
	"github.com/heavycaffeiner/stowcloud/backend/internal/platform/system/jail"
)

// RunPreviewWorker runs the jailed decoder worker. Confinement is required:
// this process parses untrusted image data, so a kernel that cannot confine it
// is a refusal rather than a degraded decoding service.
func RunPreviewWorker() int {
	status, err := worker.Run(jail.Required)
	if err != nil {
		if errors.Is(err, jail.ErrHardeningRefused) {
			return jail.Refuse(os.Stderr, status)
		}
		slog.Error("the preview worker stopped", "error", err)
		return 1
	}
	return 0
}
