//go:build linux

// The deployment subcommands: serve, and the health probe the container
// orchestrator runs.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/server"
)

// deployDataDir is where the subcommands read and write when nothing names a
// directory.
//
// The image runs `serve`, `settings` and `healthcheck` with no --data-dir and
// a working directory of /, so a relative default resolves against the root
// of a read-only filesystem: the seed cannot write it and the server cannot
// create it. One value here means the three subcommands cannot disagree about
// where a deployment's state lives. The local development directory stays the
// flag default in main, where a checkout is the working directory.
const deployDataDir = "/var/lib/stowcloud"

// serveArgs is the deploy command line, parsed.
type serveArgs struct {
	// Addr is empty rather than an address: run resolves the stored bind
	// when nothing is passed, and a default here would silently outrank it.
	Addr    string
	DataDir string
	Plain   bool
}

// parseServeArgs reads the flags a deployment's entrypoint spells out
// longhand.
//
// A flag taking a value advances past both itself and the value. Advancing by
// one left the value to be read as the next flag, so every deployment passing
// --data-dir was told its own directory was an unknown argument.
func parseServeArgs(args []string) (serveArgs, error) {
	out := serveArgs{DataDir: deployDataDir}

	i := 0
	for i < len(args) {
		flag := args[i]
		value := func() (string, bool) {
			if i+1 >= len(args) {
				return "", false
			}
			return args[i+1], true
		}
		switch flag {
		case "--data-dir", "-data":
			v, ok := value()
			if !ok {
				return serveArgs{}, fmt.Errorf("%s needs a directory", flag)
			}
			out.DataDir = v
			i += 2
		case "--addr", "-addr":
			v, ok := value()
			if !ok {
				return serveArgs{}, fmt.Errorf("%s needs an address", flag)
			}
			out.Addr = v
			i += 2
		case "--plain":
			out.Plain = true
			i++
		default:
			return serveArgs{}, fmt.Errorf("unknown argument %q", flag)
		}
	}

	return out, nil
}

// runServeCmd is the `serve` spelling of the default behaviour: it accepts
// the flags a deployment's entrypoint spells out longhand, so the container
// command line reads the same way it always has.
//
//	serve --data-dir DIR [--addr HOST:PORT] [--plain]
func runServeCmd(args []string) int {
	parsed, err := parseServeArgs(args)
	if err != nil {
		log.New(os.Stderr, "", 0).Printf("sc-engine serve: %v\n", err)
		return 2
	}
	if rerr := run(parsed.Addr, parsed.DataDir, parsed.Plain); rerr != nil {
		return 1
	}
	return 0
}

// exit codes the healthcheck answers with, which is what an orchestrator's
// restart policy reads.
const (
	healthExitOK       = 0
	healthExitNoAnswer = 1
)

// runHealthcheck probes the TLS listener over 127.0.0.1 and verifies the
// presented certificate against the pair in data/tls.
//
// Verifying properly rather than skipping verification is the point: a cert
// that no longer matches what the server holds is a server answering with
// material a healthcheck cannot account for.
func runHealthcheck(args []string) int {
	errOut := log.New(os.Stderr, "", 0)
	dataDir := deployDataDir
	i := 0
	for i < len(args) {
		if args[i] == "-data" || args[i] == "--data-dir" {
			if i+1 < len(args) {
				dataDir = args[i+1]
				i++
			}
			continue
		}
		i++
	}

	// Where to dial and what name to ask under. The settings live in a
	// database the running server holds, so this reads the snapshot that
	// server writes beside the certificate it is about to verify.
	probe := server.ReadProbe(dataDir)

	// The data directory is the operator's own argument, never request input.
	certPEM, err := os.ReadFile(filepath.Join(dataDir, "tls", "cert.pem")) //nolint:gosec // G703 reads the variable: the path is the operator's argument.
	if err != nil {
		// No certificate material yet means the server has never started;
		// that is the "nothing answered" case, not a degraded one.
		return healthExitNoAnswer
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(certPEM) {
		errOut.Println("sc-engine healthcheck: the stored certificate does not parse")
		return healthExitNoAnswer
	}

	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				RootCAs:    roots,
				ServerName: "localhost",
			},
		},
		Timeout: 5 * time.Second,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// The dial target comes from the operator's config, never from a request.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://127.0.0.1"+tlsPortOf(probe.Addr)+"/api/v1/system/health", nil) //nolint:gosec // G704 reads the variable: the address is the server's own snapshot.
	if err != nil {
		return healthExitNoAnswer
	}
	// The host guard answers a request for a name it does not serve with a
	// refusal, and the loopback address is not one of the configured names.
	// The probe therefore asks under the first configured host, which is the
	// same name the certificate is checked against. Empty before setup has
	// named one, which leaves the request carrying the dial host and the
	// guard admitting it, because before setup there is no host list to
	// refuse from.
	if probe.Host != "" {
		req.Host = probe.Host
	}

	resp, err := client.Do(req) //nolint:gosec // G704 reads the variable: the address is the operator's config.
	if err != nil {
		return healthExitNoAnswer
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			errOut.Printf("sc-engine healthcheck: closing the body: %v\n", cerr)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		// A server that answered something other than the health document is
		// a server that did not answer the question.
		errOut.Printf("sc-engine healthcheck: the server answered %d\n", resp.StatusCode)
		return healthExitNoAnswer
	}

	var doc struct {
		Status  string `json:"status"`
		Reasons []struct {
			Kind   string `json:"kind"`
			Detail string `json:"detail"`
		} `json:"reasons"`
	}
	body, rerr := io.ReadAll(io.LimitReader(resp.Body, healthBodyLimit))
	if rerr != nil {
		return healthExitNoAnswer
	}
	if jerr := json.Unmarshal(body, &doc); jerr != nil {
		errOut.Println("sc-engine healthcheck: the health document did not parse")
		return healthExitNoAnswer
	}

	// Zero for degraded as well as ok. A degraded server is a configuration
	// state and restarting it does not fix one, so mapping it to unhealthy
	// would make the runtime restart-loop a problem forever without resolving
	// it. What the reasons are still gets printed, because the operator
	// running this by hand is asking exactly that.
	for _, r := range doc.Reasons {
		errOut.Printf("degraded: %s %s\n", r.Kind, r.Detail)
	}
	if doc.Status != "ok" && doc.Status != "degraded" {
		errOut.Printf("sc-engine healthcheck: unrecognised status %q\n", doc.Status)
		return healthExitNoAnswer
	}
	return healthExitOK
}

// healthBodyLimit bounds what the probe reads. The document is a status and a
// short list, and the probe is talking to a server that may be misbehaving.
const healthBodyLimit = 64 << 10

// tlsPortOf turns a listen address into a dial port. The healthcheck always
// dials the loopback address, whatever the bind address is.
func tlsPortOf(listen string) string {
	for i := len(listen) - 1; i >= 0; i-- {
		if listen[i] == ':' {
			return listen[i:]
		}
	}
	return ":8443"
}
