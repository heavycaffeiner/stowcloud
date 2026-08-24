// The server is Linux only by design: a share root is an openat2 handle and
// the sandbox is seccomp and Landlock.
//go:build linux

package main

import (
	"context"
	"io"
	"path/filepath"

	"github.com/heavycaffeiner/stowcloud/go/internal/auth"
	"github.com/heavycaffeiner/stowcloud/go/internal/clock"
	"github.com/heavycaffeiner/stowcloud/go/internal/store"
	"github.com/heavycaffeiner/stowcloud/go/internal/store/dbfile"
	"github.com/heavycaffeiner/stowcloud/go/internal/store/state"
)

// runMasterkeyRotate re-seals every encrypted row under a new master key and
// swaps the key file. It is a CLI command and not an HTTP route because a
// master key belongs to an operator's terminal, never a browser tab: rotation
// sits at the trust level of shell access to the data directory, which is the
// level the key file already sits at.
func runMasterkeyRotate(args []string, w io.Writer) int {
	if len(args) != 1 {
		say(w, "usage: stowcloud masterkey rotate <data-dir>\n\n")
		say(w, "  Generates a new master key, re-seals every SMB %s, TOTP secret\n",
			"credential")
		say(w, "  and recoverable share-link ciphertext in one state transaction, and\n")
		say(w, "  swaps the key ring file. A crash at any point leaves the key the\n")
		say(w, "  committed database names, which the next start picks up.\n")
		return exitUsage
	}

	dir := args[0]
	ctx := context.Background()
	f, err := dbfile.Open(ctx, state.Spec(filepath.Join(dir, store.StateFile)))
	if err != nil {
		say(w, "stowcloud %s: masterkey rotate: %v\n", version, err)
		return exitConfig
	}
	defer func() {
		if cerr := f.Close(); cerr != nil {
			say(w, "stowcloud %s: masterkey rotate: closing state.db: %v\n", version, cerr)
		}
	}()

	s := auth.New(auth.Config{Store: state.New(f), StoreDir: dir, Clock: clock.System()})
	if _, kerr := s.OpenMasterKey(ctx); kerr != nil {
		say(w, "stowcloud %s: masterkey rotate: %v\n", version, kerr)
		return exitConfig
	}
	rep, err := s.RotateMasterKey(ctx)
	if err != nil {
		say(w, "stowcloud %s: masterkey rotate: %v\n", version, err)
		return exitConfig
	}
	say(w, "rotated the master key: version %d -> %d, and re-sealed %d SMB, %d TOTP and %d share-link ciphertexts\n",
		rep.OldVersion, rep.NewVersion, rep.SMBBrought, rep.TOTPBrought, rep.LinksBrought)
	return exitOK
}
