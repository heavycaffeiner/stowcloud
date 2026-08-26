// The server is Linux only by design: a share root is an openat2 handle and
// the sandbox is seccomp and Landlock.
//go:build linux

package main

import (
	"context"
	"io"
	"os"
	"path/filepath"

	"github.com/heavycaffeiner/stowcloud/go/internal/auth"
	"github.com/heavycaffeiner/stowcloud/go/internal/clock"
	"github.com/heavycaffeiner/stowcloud/go/internal/server"
	"github.com/heavycaffeiner/stowcloud/go/internal/store"
)

// runSetup re-prints and re-persists a one-time setup token from the command
// line, for the case where the first one scrolled out of a log before anyone
// read it. It shares the gate's refusal: an administrator already exists, so
// the token is not coming back.
func runSetup(args []string, w io.Writer) int {
	dataDir, uerr := dataDirArg(args)
	if uerr != nil {
		say(w, "stowcloud %s: setup: %v\n\n", version, uerr)
		say(w, "usage: stowcloud setup [--data-dir DIR]\n\n")
		say(w, "  Prints and persists a one-time setup token, valid for fifteen\n")
		say(w, "  minutes and spent once by POST /api/setup. Refused when an\n")
		say(w, "  administrator already exists. DIR defaults to %s.\n", defaultDataDir)
		return exitUsage
	}
	ctx := context.Background()
	st, serr := store.Open(dataDir, store.Options{Clock: clock.System()})
	if serr != nil {
		say(w, "stowcloud %s: setup: opening the store: %v\n", version, serr)
		return exitConfig
	}
	defer func() {
		if cerr := st.Close(); cerr != nil {
			say(w, "stowcloud %s: setup: closing the store: %v\n", version, cerr)
		}
	}()
	svc := auth.New(auth.Config{Store: st.State(), StoreDir: dataDir, Clock: clock.System()})
	if _, kerr := svc.OpenMasterKey(ctx); kerr != nil {
		say(w, "stowcloud %s: setup: the master key: %v\n", version, kerr)
		return exitConfig
	}
	gate, gerr := server.NewSetupGate(ctx, svc, clock.System(), dataDir)
	if gerr != nil {
		say(w, "stowcloud %s: setup: %v\n", version, gerr)
		return exitConfig
	}
	if rerr := gate.Reissue(ctx, os.Stdout); rerr != nil {
		say(w, "stowcloud %s: setup: %v\n", version, rerr)
		return exitConfig
	}
	say(w, "The token is also in %s\n", filepath.Join(dataDir, "setup-token"))
	return exitOK
}
