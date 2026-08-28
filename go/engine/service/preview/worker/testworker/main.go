//go:build linux

// Command testworker is a preview worker without the jail, for the pool's
// tests.
//
// The jail is proved separately against a real kernel by the probe test.
// Applying it here would make the pool's tests depend on a kernel feature that
// is allowed to be absent, and would hide the behaviour they exist to check:
// what the parent does when a worker dies or stops answering.
package main

import (
	"os"

	"github.com/heavycaffeiner/stowcloud/go/engine/service/preview/worker"
)

func main() {
	if _, err := worker.Serve(os.NewFile(worker.ControlFD, "control"), os.Getenv("HELPER_MODE")); err != nil {
		os.Exit(1)
	}
}
