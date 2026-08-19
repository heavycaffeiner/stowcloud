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
	if len(args) != 1 {
		say(w, "usage: stowcloud setup <sc.toml>\n\n")
		say(w, "  Prints and persists a one-time setup token, valid for fifteen\n")
		say(w, "  minutes and spent once by POST /api/setup. Refused when an\n")
		say(w, "  administrator already exists.\n")
		return exitUsage
	}
	cfg, err := server.Load(args[0])
	if err != nil {
		say(w, "stowcloud %s: setup: %v\n", version, err)
		return exitConfig
	}
	ctx := context.Background()
	st, serr := store.Open(cfg.DataDir, store.Options{Clock: clock.System()})
	if serr != nil {
		say(w, "stowcloud %s: setup: opening the store: %v\n", version, serr)
		return exitConfig
	}
	defer func() {
		if cerr := st.Close(); cerr != nil {
			say(w, "stowcloud %s: setup: closing the store: %v\n", version, cerr)
		}
	}()
	svc := auth.New(auth.Config{Store: st.State(), StoreDir: cfg.DataDir, Clock: clock.System()})
	if _, kerr := svc.OpenMasterKey(ctx); kerr != nil {
		say(w, "stowcloud %s: setup: the master key: %v\n", version, kerr)
		return exitConfig
	}
	gate, gerr := server.NewSetupGate(ctx, svc, clock.System(), cfg.DataDir)
	if gerr != nil {
		say(w, "stowcloud %s: setup: %v\n", version, gerr)
		return exitConfig
	}
	if rerr := gate.Reissue(ctx, os.Stdout); rerr != nil {
		say(w, "stowcloud %s: setup: %v\n", version, rerr)
		return exitConfig
	}
	say(w, "The token is also in %s\n", filepath.Join(cfg.DataDir, "setup-token"))
	return exitOK
}
