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
	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/clock"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/task"
	"github.com/heavycaffeiner/stowcloud/go/engine/lifecycle"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/preview/worker"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/settings/runtimecfg"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/dbfile"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/fsatomic"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/state"
)

// The wind-down budget. Long enough for an in-flight upload to finish its
// current write, short enough that a stuck connection does not hold the
// process forever.
const shutdownBudget = 20 * time.Second

func main() {
	// The subcommands run without a listener. The decoder re-exec is one of
	// them: it arrives with no argv to parse, which is why the dispatch
	// precedes the flags rather than following them.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "settings":
			os.Exit(runSettings(os.Args[2:]))
		case "serve":
			os.Exit(runServeCmd(os.Args[2:]))
		case "healthcheck":
			os.Exit(runHealthcheck(os.Args[2:]))
		case "preview-worker":
			os.Exit(runPreviewWorker())
		}
	}

	var (
		addr    = flag.String("addr", "127.0.0.1:8081", "listen address")
		dataDir = flag.String("data", ".dev/data", "data directory")
		plain   = flag.Bool("plain", false, "serve HTTP instead of HTTPS")
	)
	flag.Parse()

	if err := run(*addr, *dataDir, *plain); err != nil {
		slog.Error("sc-engine failed", "error", err)
		os.Exit(1)
	}
}

func run(addr, dataDir string, plain bool) error {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	abs, err := filepath.Abs(dataDir)
	if err != nil {
		return fmt.Errorf("resolving the data directory: %w", err)
	}
	if mkErr := os.MkdirAll(abs, 0o700); mkErr != nil {
		return fmt.Errorf("creating the data directory: %w", mkErr)
	}

	// The atomic path resolver is checked before anything is opened: under a
	// hardening policy that refuses a racy resolver, saying so now beats
	// failing later at every write.
	if rerr := vfs.RequireResolver(vfs.Probe()); rerr != nil {
		return rerr
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// The settings and the share hosts are read before anything else opens,
	// because the sandbox is built from them: its domain has to name the data
	// directory and every share's parent before the first file is opened, and
	// a Landlock domain cannot be widened once it is installed.
	values, shareHosts := bootSettings(abs, logger)
	domain := jailSpec(values, abs, shareHosts)
	jailStatus, jerr := jail.Apply(values.Hardening, domain)
	if jerr != nil {
		code := jail.Refuse(os.Stderr, jailStatus)
		slog.Error("the sandbox could not be applied; a required policy this kernel cannot satisfy is a stored setting, and it is the one most likely to put a deployment in a restart loop",
			"status", jailStatus.String())
		os.Exit(code)
	}
	logger.Info("hardening", "status", jailStatus.String())

	// No worker is named, which leaves the pool re-executing this binary with
	// the subcommand handled at the top of main.
	eng, err := lifecycle.Open(ctx, lifecycle.Options{
		DataDir: abs,
		Logger:  logger,
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

// bootSettings reads the stored settings and the registered share hosts from
// the state database, in one brief open that closes before the engine is
// constructed. The sandbox is built from what it returns, which is why this
// runs first: a Landlock domain cannot be widened after it is installed, so
// the share hosts have to be known before the databases below are opened for
// the engine proper.
func bootSettings(dataDir string, log *slog.Logger) (runtimecfg.Values, []string) {
	ctx := context.Background()

	stateFile, err := dbfile.Open(ctx, state.Spec(filepath.Join(dataDir, "state.db")))
	if err != nil {
		return runtimecfg.Defaults(), nil
	}
	defer func() {
		if cerr := stateFile.Close(); cerr != nil {
			log.Warn("closing the settings probe", "error", cerr)
		}
	}()
	st := state.New(stateFile)

	values := runtimecfg.Load(ctx, st, runtimecfg.Defaults(), log)

	var hosts []string
	if rows, lerr := st.ListShares(ctx); lerr == nil {
		for _, row := range rows {
			hosts = append(hosts, row.Host)
		}
	}
	return values, hosts
}

// jailSpec builds the sandbox domain from what this process touches: the data
// directory, the SMB sidecar's config directory and control socket when one is
// configured, and the parent directory of every registered share.
//
// Each share's parent is granted rather than the share itself, which is what
// lets a folder added from the admin screen work without a restart: a Landlock
// domain cannot be widened once it is installed, so a share added later could
// never be reached. The parent boundary is deliberate, and "/" is never
// granted, which is the one thing this sandbox exists to prevent.
func jailSpec(values runtimecfg.Values, dataDir string, shareHosts []string) jail.Spec {
	spec := jail.Spec{ExceptExec: true}
	spec.GrantBeneath = append(spec.GrantBeneath, jail.Grant{Path: dataDir})

	if values.SMB.Enabled && values.SMBConfigDir != "" {
		spec.GrantBeneath = append(spec.GrantBeneath, jail.Grant{Path: values.SMBConfigDir})
	}
	if sock := values.SMBSocket; values.SMB.Enabled && sock != "" {
		if dir := filepath.Dir(sock); dir != "" && dir != "/" {
			spec.GrantBeneath = append(spec.GrantBeneath, jail.Grant{Path: dir})
		}
	}

	seen := map[string]bool{}
	for _, host := range shareHosts {
		if host == "" {
			continue
		}
		clean := filepath.Clean(host)
		parent := filepath.Dir(clean)
		if parent == "/" || parent == "." || parent == clean {
			parent = clean
		}
		if seen[parent] {
			continue
		}
		seen[parent] = true
		spec.GrantBeneath = append(spec.GrantBeneath, jail.Grant{Path: parent})
	}
	return spec
}

// runPreviewWorker is the jailed decoder, and it is never run by hand.
//
// Confinement is required rather than preferred here. This is the one process
// whose whole job is parsing untrusted image data, so a kernel that cannot
// confine it is a refusal: decoding unconfined would be worse than not
// producing thumbnails at all.
func runPreviewWorker() int {
	status, err := worker.Run(jail.Required)
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
