// Linux only, because it serves a Linux-only engine.
//go:build linux

// The probe snapshot: where the server settled, written for whatever needs to
// find it later.
//
// Two fields and nothing else. This file is read by a healthcheck and by an
// operator's tooling, neither of which is authenticated, so it says where to
// knock and not what is behind the door.
package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

// ProbeMode is the file mode. Readable by the owner only: the address is not
// a secret, but a deployment's own directory is not a public noticeboard.
const ProbeMode = 0o600

// probeSizeMax bounds a read. The file this server writes is under a hundred
// bytes, so anything larger was not written by it.
const probeSizeMax = 64 << 10

// ProbeFallbackAddr is what a caller uses when the snapshot cannot be read.
//
// A guess rather than a failure: a healthcheck that cannot find the address is
// more useful trying the default than refusing to run, since the default is
// what an unconfigured deployment listens on.
const ProbeFallbackAddr = "0.0.0.0:8443"

// Probe is the snapshot's whole content.
type Probe struct {
	// Addr is the address the listener settled on, which is not always the one
	// configured: a port of zero becomes whatever the kernel assigned.
	Addr string `json:"addr"`
	// Host is the first configured app host, or empty on a deployment that has
	// named none yet.
	Host string `json:"host,omitempty"`
}

// WriteProbe publishes the snapshot durably.
//
// Durable because a concurrent reader must see the old snapshot or the new
// one, never a half-written file: a healthcheck reading a truncated address
// would report a healthy server as unreachable.
//
// publish is the same seam the TLS material uses, so this package still names
// no persistence package.
func WriteProbe(path string, p Probe, publish DurableWriter) error {
	body, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("encoding the probe snapshot: %w", err)
	}
	return publish([]string{path}, []uint32{ProbeMode}, func(_ int, f *os.File) error {
		_, werr := f.Write(body)
		return werr
	})
}

// ReadProbe reads the snapshot, falling back rather than failing.
//
// A missing or unreadable snapshot is not an error to the caller: it means
// this server has not written one yet, or wrote one this build cannot read,
// and in both cases guessing the default beats refusing to look.
func ReadProbe(path string) Probe {
	info, err := os.Stat(path)
	if err != nil || info.Size() > probeSizeMax {
		return Probe{Addr: ProbeFallbackAddr}
	}
	body, rerr := os.ReadFile(path) //nolint:gosec // the path is the caller's own data directory.
	if rerr != nil {
		return Probe{Addr: ProbeFallbackAddr}
	}

	var p Probe
	dec := json.NewDecoder(strings.NewReader(string(body)))
	if derr := dec.Decode(&p); derr != nil || strings.TrimSpace(p.Addr) == "" {
		return Probe{Addr: ProbeFallbackAddr}
	}
	return p
}

// HealthExit is what a healthcheck command exits with.
type HealthExit int

const (
	// HealthExitOK is a server that answered.
	HealthExitOK HealthExit = 0
	// HealthExitUnhealthy is a server that did not.
	HealthExitUnhealthy HealthExit = 1
)

// ErrHealth is a health response this command will not accept.
var ErrHealth = errors.New("the server did not answer with a valid health document")

// HealthExitFor decides the exit code from a health status.
//
// Only an answer that never arrived, or one this build cannot read, is
// unhealthy. Every status a running server reports exits zero, because a
// supervisor that restarts on a reported problem restarts a server whose
// configuration is wrong: that does not fix a configuration and costs every
// in-flight request each time round.
//
// "failing" exits zero for the same reason. A server saying so is a server
// that answered, and it may still be serving most of what it serves; the
// operator is told through the status and the reasons, and the supervisor is
// not asked to act on them.
func HealthExitFor(status string, err error) HealthExit {
	if err != nil {
		return HealthExitUnhealthy
	}
	switch status {
	case "ok", "degraded", "failing":
		return HealthExitOK
	default:
		// A status this build does not know is not a valid answer. It is the
		// shape of a different server, or of something answering on that port
		// that is not this server at all.
		return HealthExitUnhealthy
	}
}
