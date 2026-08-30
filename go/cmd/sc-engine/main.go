// Linux only, because the engine it serves is Linux only.
//go:build linux

// A listener in front of the rebuilt engine.
//
// The shipped command still serves the old stack, and until it is rewired
// there is no way to reach engine/lifecycle over a socket. That makes the v1
// surface unreachable from a browser, so the frontend cannot be driven against
// it and nothing about the rewiring can be checked. This is that socket.
//
// It serves the API and nothing else. The SPA is embedded in a package this
// tier may not import, so the frontend runs under its own dev server and
// proxies here, which is how web/vite.config.ts is already set up.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/server"
	"github.com/heavycaffeiner/stowcloud/go/engine/infra/jail"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/clock"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/task"
	"github.com/heavycaffeiner/stowcloud/go/engine/lifecycle"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/preview/worker"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/fsatomic"
)

// The wind-down budget. Long enough for an in-flight upload to finish its
// current write, short enough that a stuck connection does not hold the
// process forever.
const shutdownBudget = 20 * time.Second

func main() {
	// The decoder runs as this same binary re-executed, which is what the
	// pool's default expects: it has no argv to put a path in, and its socket
	// arrives on a fixed descriptor. Checked before the flags, because it takes
	// none and must not be confused by one.
	if len(os.Args) > 1 && os.Args[1] == "preview-worker" {
		os.Exit(runPreviewWorker(os.Args[2:]))
	}

	var (
		addr    = flag.String("addr", "127.0.0.1:8081", "listen address")
		dataDir = flag.String("data", ".dev/data", "data directory")
		plain   = flag.Bool("plain", false, "serve HTTP instead of HTTPS")
		// The decoder confines itself before it parses anything, and a kernel
		// that refuses the confinement is a refusal to decode. Some development
		// environments cannot apply Landlock at all, where the choice is running
		// the decoder degraded or having no thumbnails to look at.
		//
		// Required by default, because the confinement is the feature here: this
		// is the one process whose whole job is parsing untrusted image data.
		hardening = flag.String("hardening", "required",
			"decoder confinement: required, preferred, or off")
	)
	flag.Parse()

	if err := run(*addr, *dataDir, *plain, *hardening); err != nil {
		slog.Error("sc-engine failed", "error", err)
		os.Exit(1)
	}
}

func run(addr, dataDir string, plain bool, hardening string) error {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	abs, err := filepath.Abs(dataDir)
	if err != nil {
		return fmt.Errorf("resolving the data directory: %w", err)
	}
	if mkErr := os.MkdirAll(abs, 0o700); mkErr != nil {
		return fmt.Errorf("creating the data directory: %w", mkErr)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// The preview worker is this binary re-executed with a subcommand it does
	// not answer, so name the shipped command's worker only when it is built
	// beside us. Absent, thumbnails report the decoder as missing rather than
	// failing at exec with nothing pointing at why.
	eng, err := lifecycle.Open(ctx, lifecycle.Options{
		DataDir: abs,
		Logger:  logger,
		// This binary is both halves: the pool re-executes it with the
		// subcommand, and the confinement policy rides beside it.
		PreviewWorkerArgs: []string{"preview-worker", hardening},
	})
	if err != nil {
		return fmt.Errorf("opening the engine: %w", err)
	}
	defer func() {
		if cerr := eng.Close(); cerr != nil {
			logger.Error("closing the engine", "error", cerr)
		}
	}()

	app, err := eng.Mount()
	if err != nil {
		return fmt.Errorf("mounting: %w", err)
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", addr, err)
	}

	scheme := "https"
	if plain {
		scheme = "http"
	} else {
		cert, terr := devCertificate(abs, addr)
		if terr != nil {
			return terr
		}
		ln = tls.NewListener(ln, &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
		})
	}

	logger.Info("serving the engine", "url", scheme+"://"+ln.Addr().String(), "data", abs)

	served := make(chan error, 1)
	task.Go(ctx, "engine listener", func() { served <- app.Listener(ln) })

	select {
	case err := <-served:
		if err != nil && !errors.Is(err, net.ErrClosed) {
			return fmt.Errorf("serving: %w", err)
		}
		return nil
	case <-ctx.Done():
		logger.Info("shutting down")
		if serr := app.ShutdownWithTimeout(shutdownBudget); serr != nil {
			return fmt.Errorf("shutting down: %w", serr)
		}
		<-served
		return nil
	}
}

// runPreviewWorker is the jailed decoder, and it is never run by hand.
//
// Confinement is required rather than preferred here. This is the one process
// whose whole job is parsing untrusted image data, so a kernel that cannot
// confine it is a refusal: decoding unconfined would be worse than not
// producing thumbnails at all.
func runPreviewWorker(args []string) int {
	policy := jail.Required
	if len(args) > 0 {
		policy = policyOf(args[0])
	}
	status, err := worker.Run(policy)
	if err != nil {
		if errors.Is(err, jail.ErrHardeningRefused) {
			// The step, the errno and the kernel, because "hardening failed"
			// is not something anybody can act on.
			return jail.Refuse(os.Stderr, status)
		}
		slog.Error("the preview worker stopped", "error", err)
		return 1
	}
	return 0
}

// policyOf reads a confinement name.
//
// Anything unrecognised is the strict policy. A misspelling must not quietly
// weaken the one process that exists to be confined.
func policyOf(name string) jail.Policy {
	switch name {
	case "preferred":
		return jail.Preferred
	case "off":
		return jail.Off
	default:
		return jail.Required
	}
}

// devCertificate reuses the engine's own material so a browser that has
// accepted this deployment's certificate once keeps trusting it.
func devCertificate(dataDir, addr string) (tls.Certificate, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	if host == "" {
		host = "127.0.0.1"
	}
	hosts := []string{host}
	if host != "localhost" {
		hosts = append(hosts, "localhost")
	}

	paths := server.TLSPaths{
		Cert: filepath.Join(dataDir, "tls", "cert.pem"),
		Key:  filepath.Join(dataDir, "tls", "key.pem"),
	}
	if mkErr := os.MkdirAll(filepath.Dir(paths.Cert), 0o700); mkErr != nil {
		return tls.Certificate{}, fmt.Errorf("creating the TLS directory: %w", mkErr)
	}

	_, statErr := os.Stat(paths.Cert)
	firstBoot := errors.Is(statErr, os.ErrNotExist)

	return server.EnsureTLS(paths, hosts, clock.System(), firstBoot,
		func(names []string, modes []uint32, write func(i int, f *os.File) error) error {
			units := make([]fsatomic.Unit, len(names))
			for i, n := range names {
				units[i] = fsatomic.Unit{Path: n, Mode: modes[i]}
			}
			return fsatomic.ReplaceFilesDurable(units, write)
		})
}
