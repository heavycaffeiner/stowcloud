// The server is Linux only by design, and so is its repair door.
//go:build linux

package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/internal/auth"
	"github.com/heavycaffeiner/stowcloud/go/internal/clock"
	"github.com/heavycaffeiner/stowcloud/go/internal/server"
	"github.com/heavycaffeiner/stowcloud/go/internal/store"
)

// runEmergency brings up the settings editor and nothing else.
//
// It is deliberately the shortest path in this file. No sandbox, no core, no
// watcher, no upload engine, no preview pool, no search: the whole reason to
// run this is that one of those did not come up, and every one of them left
// out is one thing that cannot stop the repair door opening.
//
// The sandbox is the pointed omission. Its policy is a stored setting, and a
// policy this kernel cannot satisfy is exactly the failure that leaves an
// operator with nowhere to go: applying it here would take the door down over
// the setting the door exists to change.
func runEmergency(ctx context.Context, dataDir string, log *slog.Logger, stderr io.Writer) int {
	// The data directory is the one thing shared with a server that may still
	// be running, so the lock is taken rather than assumed. Two processes
	// writing these databases is how a repair ends up applied to a document
	// the running server then overwrites.
	lock, lerr := store.LockInstance(dataDir)
	if lerr != nil {
		say(stderr, "stowcloud %s: serve --emergency: %v\n", version, lerr)
		say(stderr, "  The server is still running. Stop it first: its own /emergency\n")
		say(stderr, "  route is the same editor and is already reachable.\n")
		return exitConfig
	}
	defer func() {
		if rerr := lock.Release(); rerr != nil {
			log.Warn("releasing the data directory lock", "error", rerr)
		}
	}()

	st, serr := store.Open(dataDir, store.Options{Clock: clock.System()})
	if serr != nil {
		// Nothing below this can be done without the settings, and they are in
		// the store. This is the one failure the emergency mode cannot repair.
		say(stderr, "stowcloud %s: serve --emergency: opening the store: %v\n", version, serr)
		return exitConfig
	}
	defer func() {
		if cerr := st.Close(); cerr != nil {
			say(stderr, "stowcloud %s: serve --emergency: closing the store: %v\n", version, cerr)
		}
	}()

	// No passdb path: this mode publishes nothing to SMB, and a credential
	// change made here is not what it is for.
	authSvc := auth.New(auth.Config{
		Store: st.State(), StoreDir: dataDir, Clock: clock.System(), Logger: log,
	})
	if _, kerr := authSvc.OpenMasterKey(ctx); kerr != nil {
		say(stderr, "stowcloud %s: serve --emergency: the master key: %v\n", version, kerr)
		return exitConfig
	}

	// Exiting is the whole of what a restart is here: this process holds no
	// engine to rebuild, so what has to happen is that the supervisor starts
	// the ordinary server again, without the flag.
	stop := make(chan struct{}, 1)
	serve, nerr := server.ServeEmergency(ctx, server.EmergencyOptions{
		Store: st, Auth: authSvc, Log: log, DataDir: dataDir,
		Reason: "started with --emergency",
		Restart: func() {
			select {
			case stop <- struct{}{}:
			default:
			}
		},
	})
	if nerr != nil {
		say(stderr, "stowcloud %s: serve --emergency: %v\n", version, nerr)
		return exitNoAnswer
	}
	log.Warn("emergency mode: only the settings editor is served",
		"addr", serve.Addr(), "path", "/emergency")

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	select {
	case s := <-sig:
		log.Info("shutting down", "signal", s.String())
	case <-stop:
		log.Info("exiting so the server can be started again without --emergency")
	}
	serve.Stop(ctx)
	return exitOK
}

// emergencyHint is what a startup failure prints instead of exiting silently.
//
// A stored setting that stops the server coming up used to leave an operator
// with a log line and no next step, because the screen that edits the setting
// is served by the thing that would not start. This names the step.
func emergencyHint(stderr io.Writer, dataDir string) {
	say(stderr, "  The settings that stopped this are in the database. To repair them,\n")
	say(stderr, "  start only the settings editor and browse to /emergency:\n\n")
	say(stderr, "      stowcloud serve --emergency --data-dir %s\n\n", filepath.Clean(dataDir))
}

// degrade brings up the emergency layer in a process whose engine could not be
// built, and serves it until the process is asked to stop.
//
// The process stays up rather than exiting, which is the point: browsing to
// the deployment's own address then lands on the repair screen with a banner
// naming what failed, instead of on a refused connection that says only that
// something is wrong.
//
// The sandbox is already applied by the time anything can reach this, so the
// degraded process is confined exactly as the healthy one would have been.
func degrade(
	ctx context.Context, st *store.Store, authSvc *auth.Service,
	dataDir, reason string, log *slog.Logger, stderr io.Writer,
) int {
	attempts, looping := recordEngineFailure(dataDir, time.Now())
	log.Error("the server could not be built; serving only the settings editor",
		"reason", reason, "recent_failures", attempts)
	if looping {
		// Three of these inside a minute is a stored value that is not going to
		// start working, not a disk that was slow to mount. Saying so is the
		// whole of what this does: the process still stays up serving the
		// repair door, because exiting would hand the spin back to the
		// supervisor, and that is the loop rather than the fix.
		log.Error("the server has failed to start repeatedly; holding in emergency mode",
			"failures", attempts, "within", engineLoopWindow.String())
		reason = "repeated startup failure: " + reason
	}
	emergencyHint(stderr, dataDir)

	stop := make(chan struct{}, 1)
	serve, nerr := server.ServeEmergency(ctx, server.EmergencyOptions{
		Store: st, Auth: authSvc, Log: log, DataDir: dataDir,
		Reason: reason,
		Restart: func() {
			select {
			case stop <- struct{}{}:
			default:
			}
		},
	})
	if nerr != nil {
		// Nothing left to serve from. The failure that brought us here is
		// already reported; this one says the door could not be opened either.
		say(stderr, "stowcloud %s: serve: the settings editor could not be started: %v\n", version, nerr)
		return exitConfig
	}
	log.Warn("emergency mode: only the settings editor is served",
		"addr", serve.Addr(), "path", "/emergency")

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	select {
	case s := <-sig:
		log.Info("shutting down", "signal", s.String())
	case <-stop:
		log.Info("restarting: the settings were repaired")
	}
	serve.Stop(ctx)
	// Not exitOK: the server did not serve what it was started to serve, and a
	// supervisor reading the code should see that it failed.
	return exitConfig
}
