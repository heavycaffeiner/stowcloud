package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/internal/acl"
	"github.com/heavycaffeiner/stowcloud/go/internal/auth"
	"github.com/heavycaffeiner/stowcloud/go/internal/clock"
	"github.com/heavycaffeiner/stowcloud/go/internal/core"
	"github.com/heavycaffeiner/stowcloud/go/internal/server"
	"github.com/heavycaffeiner/stowcloud/go/internal/store"
	"github.com/heavycaffeiner/stowcloud/go/internal/task"
)

// runServe starts the server: config, store, master key, core domain, the
// setup gate, the listener, and a graceful shutdown on SIGINT or SIGTERM.
func runServe(args []string, stderr io.Writer) int {
	if len(args) > 1 {
		say(stderr, "usage: stowcloud serve [sc.toml]\n\n")
		say(stderr, "  Starts the server. The one config file names the data directory,\n")
		say(stderr, "  the hosts this server answers for, the trusted-proxy ranges, and\n")
		say(stderr, "  the rate bounds. With no argument, sc.toml in the working\n")
		say(stderr, "  directory is read; a missing file is a refused startup.\n")
		return exitUsage
	}
	configPath := "sc.toml"
	if len(args) == 1 {
		configPath = args[0]
	}

	log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	clk := clock.System()
	ctx := context.Background()

	cfg, err := server.Load(configPath)
	if err != nil {
		say(stderr, "stowcloud %s: serve: %v\n", version, err)
		return exitConfig
	}

	st, serr := store.Open(cfg.DataDir, store.Options{Clock: clk})
	if serr != nil {
		say(stderr, "stowcloud %s: serve: opening the store: %v\n", version, serr)
		return exitConfig
	}
	defer func() {
		if cerr := st.Close(); cerr != nil {
			say(stderr, "stowcloud %s: serve: closing the store: %v\n", version, cerr)
		}
	}()

	authSvc := auth.New(auth.Config{Store: st.State(), StoreDir: cfg.DataDir, Clock: clk})
	if _, kerr := authSvc.OpenMasterKey(ctx); kerr != nil {
		say(stderr, "stowcloud %s: serve: the master key: %v\n", version, kerr)
		return exitConfig
	}

	coreSvc, cerr := core.New(st, core.Options{ACL: acl.NewEvaluator(), Clock: clk})
	if cerr != nil {
		say(stderr, "stowcloud %s: serve: the core domain: %v\n", version, cerr)
		return exitConfig
	}

	setupGate, gerr := server.NewSetupGate(ctx, authSvc, clk, cfg.DataDir)
	if gerr != nil {
		say(stderr, "stowcloud %s: serve: the setup gate: %v\n", version, gerr)
		return exitConfig
	}
	if setupGate.IsRequired(ctx) {
		if ierr := setupGate.Issue(os.Stdout); ierr != nil {
			say(stderr, "stowcloud %s: serve: issuing the setup token: %v\n", version, ierr)
			return exitConfig
		}
	}

	watcher, hub, werr := server.StartWatch(ctx, coreSvc, clk, log)
	if werr != nil {
		say(stderr, "stowcloud %s: serve: the watcher: %v\n", version, werr)
		return exitConfig
	}
	defer func() {
		_ = watcher.Close() //nolint:errcheck // shutdown is closing everything anyway.
	}()

	srv, nerr := server.New(cfg, server.Options{Store: st, Auth: authSvc, Core: coreSvc, Log: log, Clk: clk, Watch: watcher, WS: hub}, setupGate)
	if nerr != nil {
		say(stderr, "stowcloud %s: serve: %v\n", version, nerr)
		return exitConfig
	}

	ln, lerr := net.Listen("tcp", cfg.Listen)
	if lerr != nil {
		say(stderr, "stowcloud %s: serve: binding %s: %v\n", version, cfg.Listen, lerr)
		return exitNoAnswer
	}

	// The shutdown path: a signal starts the drain, and the drain has a
	// deadline of its own so a stuck upload cannot hold the process forever.
	// The watcher is a task, which is the one spawn this tree allows and the
	// one that installs a recover.
	done := make(chan struct{})
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	task.Go(ctx, "shutdown watcher", func() {
		s := <-sig
		log.Info("shutting down", "signal", s.String())
		shCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		if serr := srv.Shutdown(shCtx); serr != nil {
			log.Error("shutdown did not drain cleanly", "error", serr)
			_ = srv.Close() //nolint:errcheck // the drain already failed; closing is the fallback.
		}
		close(done)
	})

	log.Info("listening", "addr", cfg.Listen, "app_host", cfg.AppHost)
	if err := srv.ServeTLS(ln, "", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
		say(stderr, "stowcloud %s: serve: %v\n", version, err)
		return exitNoAnswer
	}
	<-done
	return exitOK
}
