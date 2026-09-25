// Linux only, because the engine it serves is Linux only.
//go:build linux

// The product command selects Hanami bootstrap integrations and composes the
// Stowcloud application graph. Hanami owns process lifecycle and managed HTTP
// generations; the product owns routes, policy, and feature services.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/debug"

	"github.com/heavycaffeiner/hanami"
	"github.com/heavycaffeiner/hanami/ownership"
	securitylinux "github.com/heavycaffeiner/hanami/security/linux"
	app "github.com/heavycaffeiner/stowcloud/backend/internal/app"
	"github.com/heavycaffeiner/stowcloud/backend/internal/bootstrap/preflight"
	"github.com/heavycaffeiner/stowcloud/backend/internal/bootstrap/sandbox"
	"go.uber.org/fx"
)

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
			os.Exit(sandbox.RunPreviewWorker())
		case "version", "--version", "-version", "-v":
			os.Exit(runVersion())
		}
	}

	var (
		addr    = flag.String("addr", "", "listen address; overrides the stored one")
		dataDir = flag.String("data", ".dev/data", "data directory")
		plain   = flag.Bool("plain", false, "serve HTTP instead of HTTPS")
	)
	flag.Parse()

	if err := run(*addr, *dataDir, *plain); err != nil {
		slog.Error("sc-engine failed", "error", err)
		os.Exit(1)
	}
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

func run(addr, dataDir string, plain bool) error {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	handoff := os.Getenv(securitylinux.HandoffEnvironment) != ""
	config, err := preflight.Load(context.Background(), preflight.Options{
		Addr: addr, DataDir: dataDir, Plain: plain, Logger: logger,
		SkipRootDiscovery: false,
	})
	if err != nil {
		return err
	}
	policy := sandbox.BuildPolicy(config.Values, config.DataDir, config.Roots, config.ShareHosts, config.ExactPaths)
	if !handoff {
		if securityErr := securitylinux.MaybeReexec(policy); securityErr != nil {
			return fmt.Errorf("applying process security: %w", securityErr)
		}
	}
	spec := hanami.Spec[preflight.Config]{
		Name: "sc-engine",
		Load: func(context.Context) (preflight.Config, error) {
			return config, nil
		},
		Modules: func(config preflight.Config) fx.Option {
			return app.Module(app.ModuleConfig{
				DataDir:   config.DataDir,
				Address:   config.Address,
				Pinned:    config.Pinned,
				Plain:     config.Plain,
				Hardening: config.Values.Hardening,
				Revision:  buildRevision(),
				Logger:    logger,
			})
		},
	}
	options := []hanami.Option[preflight.Config]{
		securitylinux.WithPolicy(func(config preflight.Config) (securitylinux.Policy, error) {
			return sandbox.BuildPolicy(config.Values, config.DataDir, config.Roots, config.ShareHosts, config.ExactPaths), nil
		}),
		ownership.WithRequirement(func(config preflight.Config) (ownership.Requirement, error) {
			return ownership.Requirement{LockPath: filepath.Join(config.DataDir, ".stowcloud-instance.lock")}, nil
		}),
		hanami.WithLogger[preflight.Config](logger),
	}
	_, err = hanami.Run(context.Background(), spec, options...)
	return err
}
