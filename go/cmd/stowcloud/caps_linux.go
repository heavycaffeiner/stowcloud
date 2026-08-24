//go:build linux

package main

import (
	"fmt"
	"io"

	"github.com/heavycaffeiner/stowcloud/go/internal/jail"
	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
)

// printCaps runs the runtime probe and prints it. A security property nothing
// executes is a sentence in a document, and this is the one an operator can run
// against the container that is actually deployed.
func printCaps(w io.Writer) int {
	report := vfs.Probe().String()

	abi, err := jail.ABIVersion()
	if err != nil {
		report += fmt.Sprintf("%-15s unavailable: %v\n", "landlock", err)
	} else {
		report += fmt.Sprintf("%-15s ABI %d\n", "landlock", abi)
	}

	say(w, "%s", report)
	return exitOK
}
