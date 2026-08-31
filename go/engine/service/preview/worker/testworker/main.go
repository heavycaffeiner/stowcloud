//go:build linux

// Command testworker runs a preview worker with no jail, serving the pool's
// tests.
//
// The probe test verifies the jail separately against a live kernel. Installing
// it here would tie the pool's tests to a kernel feature permitted to be
// missing, and would obscure exactly what they exist to observe: how the parent
// reacts when a worker dies or stops responding.
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
