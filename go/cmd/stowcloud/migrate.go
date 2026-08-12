package main

import (
	"context"
	"io"
	"path/filepath"

	"github.com/heavycaffeiner/stowcloud/go/internal/store"
	"github.com/heavycaffeiner/stowcloud/go/internal/store/fromrust"
)

// runMigrate is the one-shot import of a Rust-era data directory.
//
// It takes the directory as an argument rather than reading the configuration,
// because it is run by hand between stopping one build and starting the other,
// and the thing it must not do is guess which data directory that was.
func runMigrate(args []string, w io.Writer) int {
	if len(args) != 2 || args[0] != "--from-rust" {
		say(w, "usage: stowcloud migrate --from-rust <data-dir>\n\n")
		say(w, "  Reads the databases the Rust build wrote, without touching them,\n")
		say(w, "  and writes %s beside them. It refuses if that file exists.\n", store.StateFile)
		return exitUsage
	}

	dir := args[1]
	rep, err := fromrust.Import(context.Background(), dir)
	if err != nil {
		say(w, "stowcloud %s: migrate: %v\n", version, err)
		return exitConfig
	}

	say(w, "Wrote %s:\n", filepath.Join(dir, store.StateFile))
	if err := rep.Write(w); err != nil {
		return exitNoAnswer
	}
	return exitOK
}
