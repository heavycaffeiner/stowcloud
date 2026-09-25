//go:build linux

// The settings escape hatch.
//
// Configuration lives in the database and the web interface is where it is
// edited. That leaves one case with no way out: a stored value that stops the
// server answering at all. A bind address nothing can bind takes every
// interface down with the ordinary one, because all of them need a socket.
//
// So there is a command. It writes one section the same way the API does, on
// a data directory nothing is serving, and it is the only way to change a
// setting without a running server.
package main

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"

	"github.com/heavycaffeiner/stowcloud/backend/internal/bootstrap/args"
	"github.com/heavycaffeiner/stowcloud/backend/internal/store/dbfile"
	"github.com/heavycaffeiner/stowcloud/backend/internal/store/state"
)

// runSettings dispatches the settings verbs. `set` replaces one section from
// a JSON document on standard input; `get` prints the whole stored document.
func runSettings(argv []string) int {
	if len(argv) == 0 {
		return settingsUsage()
	}
	switch argv[0] {
	case "set":
		return runSettingsSet(argv[1:])
	case "get":
		return runSettingsGet(argv[1:])
	}
	return settingsUsage()
}

func settingsUsage() int {
	out := log.New(os.Stderr, "", 0)
	out.Println("usage: sc-engine settings get [-data DIR]")
	out.Println("       sc-engine settings set <section> [-data DIR] < document.json")
	out.Println()
	out.Println("  Reads or writes the stored settings directly, for a deployment whose")
	out.Println("  stored configuration stops the server answering. The document is one")
	out.Println("  section's JSON object on standard input, and it replaces that section")
	out.Println("  whole. Every other section is left alone.")
	out.Println()
	out.Println("  Nothing here validates the document. The server clamps or drops what")
	out.Println("  it cannot use and logs why, which is what makes this a way back in")
	out.Println("  rather than a second place to get it wrong.")
	return 2
}

// runSettingsSet replaces one section from a JSON document on standard input.
//
// The arguments are the section name and the data directory, in either
func runSettingsSet(argv []string) int {
	out := log.New(os.Stderr, "", 0)
	section, dataDir := args.ParseSettingsArgs(argv)
	if section == "" {
		return settingsUsage()
	}

	raw, rerr := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
	if rerr != nil {
		out.Printf("sc-engine settings: reading the document: %v\n", rerr)
		return 1
	}
	var sectionBody map[string]any
	if jerr := json.Unmarshal(raw, &sectionBody); jerr != nil {
		out.Printf("sc-engine settings: the document is not a JSON object: %v\n", jerr)
		return 1
	}

	stateFile, err := dbfile.Open(context.Background(), state.Spec(filepath.Join(dataDir, "state.db")))
	if err != nil {
		out.Printf("sc-engine settings: opening the store: %v\n", err)
		return 1
	}
	defer func() {
		if cerr := stateFile.Close(); cerr != nil {
			out.Printf("sc-engine settings: closing the store: %v\n", cerr)
		}
	}()
	st := state.New(stateFile)
	if merr := st.MergeSettings(context.Background(), section, sectionBody); merr != nil {
		out.Printf("sc-engine settings: writing %s: %v\n", section, merr)
		return 1
	}
	out.Printf("wrote the %s section\n", section)
	return 0
}

func runSettingsGet(argv []string) int {
	out := log.New(os.Stderr, "", 0)
	dataDir := args.DataDir(argv)

	stateFile, err := dbfile.Open(context.Background(), state.Spec(filepath.Join(dataDir, "state.db")))
	if err != nil {
		out.Printf("sc-engine settings: opening the store: %v\n", err)
		return 1
	}
	defer func() {
		if cerr := stateFile.Close(); cerr != nil {
			out.Printf("sc-engine settings: closing the store: %v\n", cerr)
		}
	}()
	st := state.New(stateFile)
	all, aerr := st.Settings(context.Background())
	if aerr != nil {
		out.Printf("sc-engine settings: reading them: %v\n", aerr)
		return 1
	}
	body, jerr := json.MarshalIndent(all, "", "  ")
	if jerr != nil {
		out.Printf("sc-engine settings: rendering them: %v\n", jerr)
		return 1
	}
	out.Println(string(body))
	return 0
}
