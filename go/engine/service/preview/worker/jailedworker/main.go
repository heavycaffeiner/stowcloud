//go:build linux

// Command jailedworker is the shipped preview worker: it applies the jail
// before it reads its first message.
//
// It exists as its own command so the proof can exec the real thing rather
// than a stand-in. A sandbox proved against a copy is a sandbox nobody proved.
package main

import (
	"log/slog"
	"os"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/jail"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/preview/worker"
)

func main() {
	// Preferred rather than required: a kernel without Landlock still gets the
	// seccomp half, and the proof asserts what the kernel actually did rather
	// than refusing to run where one layer is absent.
	if _, err := worker.Run(jail.Preferred); err != nil {
		slog.Error("the jailed preview worker stopped", slog.Any("error", err))
		os.Exit(1)
	}
}
