//go:build linux

package preview

import (
	"fmt"
	"os"

	"github.com/stowcloud/sandbox-worker"
)

// Preview keeps this narrow adapter for product callsites. Neutral descriptor
// transport is implemented and released by github.com/stowcloud/sandbox-worker.

// SocketPair creates the connected control socket pair used by the preview
// worker.
func SocketPair() (a, b *os.File, err error) {
	a, b, err = sandboxworker.SocketPair()
	if err != nil {
		return nil, nil, fmt.Errorf("preview: %w", err)
	}
	return a, b, nil
}

// SendMessage forwards neutral message bytes and optional descriptor rights.
func SendMessage(sock *os.File, msg []byte, pass ...*os.File) error {
	if err := sandboxworker.SendMessage(sock, msg, pass...); err != nil {
		return fmt.Errorf("preview: %w", err)
	}
	return nil
}

// RecvMessage forwards neutral message bytes and descriptor rights. Returned
// files are owned by the caller and should be closed with CloseFiles.
func RecvMessage(sock *os.File, buf []byte, maxFiles int) (n int, files []*os.File, err error) {
	n, files, err = sandboxworker.RecvMessage(sock, buf, maxFiles)
	if err != nil {
		return 0, nil, fmt.Errorf("preview: %w", err)
	}
	return n, files, nil
}

// CloseFiles closes descriptors received from a worker message.
func CloseFiles(files []*os.File) { sandboxworker.CloseFiles(files) }
