package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/internal/server"
)

// runHealthcheck probes the TLS listener over 127.0.0.1 and verifies the
// presented certificate against the one in data/tls. Verifying properly
// rather than skipping verification is the point: a cert that no longer
// matches what the server holds is a server answering with material a
// healthcheck cannot account for.
func runHealthcheck(args []string, w io.Writer) int {
	if len(args) != 1 {
		say(w, "usage: stowcloud healthcheck <sc.toml>\n\n")
		say(w, "  Dials the server over 127.0.0.1, verifies its certificate against\n")
		say(w, "  the pair in data/tls, and answers 0 on ok or degraded, 1 when\n")
		say(w, "  nothing answered at all.\n")
		return exitUsage
	}
	cfg, err := server.Load(args[0])
	if err != nil {
		say(w, "stowcloud %s: healthcheck: %v\n", version, err)
		return exitConfig
	}

	// The data directory is the operator's own argument, never request input.
	certPEM, err := os.ReadFile(filepath.Join(cfg.DataDir, "tls", "cert.pem")) //nolint:gosec // G703 reads the variable: the path is the operator's config.
	if err != nil {
		// No certificate material yet means the server has never started;
		// that is the "nothing answered" case, not a degraded one.
		return exitNoAnswer
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(certPEM) {
		say(w, "stowcloud %s: healthcheck: the stored certificate does not parse\n", version)
		return exitNoAnswer
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
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://127.0.0.1"+tlsPortOf(cfg.Listen)+"/", nil) //nolint:gosec // G704 reads the variable: the address is the operator's config.
	if err != nil {
		return exitNoAnswer
	}
	resp, err := client.Do(req) //nolint:gosec // G704 reads the variable: the address is the operator's config.
	if err != nil {
		return exitNoAnswer
	}
	_ = resp.Body.Close() //nolint:errcheck // the probe reads no body.
	return exitOK
}

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
