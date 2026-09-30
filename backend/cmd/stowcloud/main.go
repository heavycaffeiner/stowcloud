// Linux only, because the engine it serves is Linux only.
//go:build linux

// The engine command: load the deployment settings, seal the process, then
// hand the settled configuration to server.Run.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"runtime/debug"

	securitylinux "github.com/heavycaffeiner/hanami/security/linux"
	"github.com/heavycaffeiner/stowcloud/backend/internal/bootstrap/preflight"
	"github.com/heavycaffeiner/stowcloud/backend/internal/bootstrap/sandbox"
	"github.com/heavycaffeiner/stowcloud/backend/internal/config"
	"github.com/heavycaffeiner/stowcloud/backend/internal/server"
)

func main() {
	// The subcommands other than serve run without a listener. The decoder
	// re-exec is one of them. A command line naming no subcommand serves.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "settings":
			os.Exit(runSettings(os.Args[2:]))
		case "serve":
			os.Exit(runServe("stowcloud serve", os.Args[2:]))
		case "healthcheck":
			os.Exit(runHealthcheck(os.Args[2:]))
		case "preview-worker":
			os.Exit(sandbox.RunPreviewWorker())
		case "version", "--version", "-version", "-v":
			os.Exit(runVersion())
		}
	}

	os.Exit(runServe("stowcloud", os.Args[1:]))
}

// revision is stamped into the binary at build time with -ldflags="-X main.revision=...".
var revision string

func buildRevision() string {
	if revision != "" {
		return revision
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, s := range info.Settings {
			if s.Key == "vcs.revision" && s.Value != "" {
				return s.Value
			}
		}
	}
	return "dev"
}

func runVersion() int {
	if _, err := fmt.Println(buildRevision()); err != nil {
		return 1
	}
	return 0
}

// runServe parses the serve flags and serves until the process stops.
//
//	[serve] [--data-dir DIR] [--addr HOST:PORT] [--plain]
func runServe(name string, argv []string) int {
	parsed, err := config.ParseServeArgs(name, argv, os.Stderr)
	switch {
	case errors.Is(err, flag.ErrHelp):
		return 0
	case err != nil:
		return 2
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if rerr := run(logger, parsed); rerr != nil {
		logger.Error("the server failed", "error", rerr)
		return 1
	}
	return 0
}

func run(logger *slog.Logger, args config.ServeArgs) error {
	handoff := os.Getenv(securitylinux.HandoffEnvironment) != ""
	loaded, err := preflight.Load(context.Background(), preflight.Options{
		Addr: args.Addr, DataDir: args.DataDir, Plain: args.Plain, Logger: logger,
		SkipRootDiscovery: false,
	})
	if err != nil {
		return err
	}
	policy := sandbox.BuildPolicy(loaded.Values, loaded.DataDir, loaded.Roots, loaded.ShareHosts, loaded.ExactPaths)
	if !handoff {
		if securityErr := securitylinux.MaybeReexec(policy); securityErr != nil {
			return fmt.Errorf("applying process security: %w", securityErr)
		}
	}
	// Past the re-exec this verifies the handoff and installs the syscall
	// filter, before any service opens a file.
	if _, err := securitylinux.Apply(policy); err != nil {
		return fmt.Errorf("applying process security: %w", err)
	}
	return server.Run(context.Background(), server.Config{
		DataDir:      loaded.DataDir,
		Address:      loaded.Address,
		Pinned:       loaded.Pinned,
		Plain:        loaded.Plain,
		Hardening:    loaded.Values.Hardening,
		Revision:     buildRevision(),
		Logger:       logger,
		InstanceLock: loaded.Lock,
	})
}
