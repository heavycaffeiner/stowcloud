// Linux only: it depends on packages that are Linux only.
//go:build linux

package server

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
)

// What a second process needs to reach this one.
//
// The healthcheck runs as its own process while the server holds the store,
// so it cannot read the settings: it needs the port to dial and the host to
// ask under, and both now live in a database it must not open. So the server
// writes those two facts into the data directory whenever they change, and the
// healthcheck reads that. It already reads the TLS material from the same
// directory, so this is one more file in a pattern that exists.
//
// Nothing else may be put here. This is not a config file growing back: it is
// a snapshot of what the running server settled on, written by the server and
// read by a probe.

// probeFileName is the snapshot's name inside the data directory.
const probeFileName = ".probe.json"

// Probe is what the healthcheck needs and nothing more.
type Probe struct {
	// Listen is the address the server bound, so the probe knows the port.
	Listen string `json:"listen"`
	// AppHost is the name the certificate is issued for and the host guard
	// admits. Empty before setup has named one.
	AppHost string `json:"app_host"`
}

// WriteProbe records the snapshot. A failure is the caller's to log and
// continue from: the server is up either way, and a probe that cannot find
// the file falls back to the default port.
func WriteProbe(dataDir string, p Probe) error {
	body, err := json.Marshal(p)
	if err != nil {
		return err
	}
	path := filepath.Join(dataDir, probeFileName)
	// Staged and renamed into place, so a probe reading concurrently sees
	// either the old snapshot or the new one and never half of one.
	werr := vfs.ReplaceFileDurable(path, 0o600, func(f *os.File) error {
		_, err := f.Write(body)
		return err
	})
	if werr != nil {
		return fmt.Errorf("publishing the probe snapshot: %w", werr)
	}
	return nil
}

// ReadProbe reads the snapshot. An absent or unreadable one answers the
// defaults: a server that has never started leaves none, and that case is
// "nothing answered" rather than a broken probe.
func ReadProbe(dataDir string) Probe {
	out := Probe{Listen: defaultProbeListen}
	body, err := os.ReadFile(filepath.Join(dataDir, probeFileName)) //nolint:gosec // G703 reads the variable: the path is the operator's data directory.
	if err != nil {
		return out
	}
	var p Probe
	if jerr := json.Unmarshal(body, &p); jerr != nil {
		return out
	}
	if p.Listen == "" {
		p.Listen = defaultProbeListen
	}
	return p
}

// defaultProbeListen is where a server with no snapshot would be.
const defaultProbeListen = "0.0.0.0:8443"
