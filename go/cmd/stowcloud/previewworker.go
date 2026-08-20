//go:build linux

package main

import (
	"errors"
	"io"

	"github.com/heavycaffeiner/stowcloud/go/internal/jail"
	"github.com/heavycaffeiner/stowcloud/go/internal/preview/worker"
)

// runPreviewWorker is the jailed decoder, and it is never run by hand.
//
// It takes no arguments and reads no configuration. That is the design rather
// than an omission: an argv is a place to put a path, and this process must
// have no way to name a file. Its control socket arrives on a fixed descriptor
// and the two descriptors for each job arrive beside the job itself.
func runPreviewWorker(args []string, stderr io.Writer) int {
	if len(args) != 0 {
		say(stderr, "stowcloud %s: preview-worker takes no arguments\n", version)
		return exitUsage
	}

	// Required: a worker that could not be confined must not decode anything.
	// This is the one process where the jail is the feature rather than a
	// second line of defence, so a kernel that cannot provide it is a refusal.
	st, err := worker.Run(jail.Required)
	if err != nil {
		if errors.Is(err, jail.ErrHardeningRefused) {
			// The operator gets the step, the errno and the kernel, because
			// "hardening failed" is not something anyone can act on.
			return jail.Refuse(stderr, st)
		}
		say(stderr, "stowcloud %s: preview-worker: %v\n", version, err)
		return exitNoAnswer
	}
	return 0
}
