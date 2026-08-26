// The server is Linux only by design: a share root is an openat2 handle and
// the sandbox is seccomp and Landlock.
//go:build linux

package main

import (
	"context"
	"encoding/json"
	"io"
	"os"

	"github.com/heavycaffeiner/stowcloud/go/internal/clock"
	"github.com/heavycaffeiner/stowcloud/go/internal/store"
)

// The settings escape hatch.
//
// Configuration lives in the database and the web interface is where it is
// edited. That leaves one case with no way out: a stored value that stops the
// server answering at all. A bind address nothing can bind takes the emergency
// interface down with the ordinary one, because both need a socket.
//
// So there is a command. It writes one section the same way the API does, on a
// data directory nothing is serving, and it is the only way to change a
// setting without a running server.

// runSettings dispatches the settings verbs.
func runSettings(args []string, w io.Writer) int {
	if len(args) == 0 {
		return settingsUsage(w)
	}
	switch args[0] {
	case "set":
		return runSettingsSet(args[1:], w)
	case "get":
		return runSettingsGet(args[1:], w)
	}
	return settingsUsage(w)
}

func settingsUsage(w io.Writer) int {
	say(w, "usage: stowcloud settings get [--data-dir DIR]\n")
	say(w, "       stowcloud settings set <section> [--data-dir DIR] < document.json\n\n")
	say(w, "  Reads or writes the stored settings directly, for a deployment whose\n")
	say(w, "  stored configuration stops the server answering. The document is one\n")
	say(w, "  section's JSON object on standard input, and it replaces that\n")
	say(w, "  section whole. Every other section is left alone.\n\n")
	say(w, "  Nothing here validates the document. The server clamps or drops what\n")
	say(w, "  it cannot use and logs why, which is what makes this a way back in\n")
	say(w, "  rather than a second place to get it wrong.\n")
	return exitUsage
}

// runSettingsSet replaces one section from a JSON document on stdin.
func runSettingsSet(args []string, w io.Writer) int {
	if len(args) == 0 {
		return settingsUsage(w)
	}
	section := args[0]
	dataDir, uerr := dataDirArg(args[1:])
	if uerr != nil {
		say(w, "stowcloud %s: settings: %v\n\n", version, uerr)
		return settingsUsage(w)
	}

	// The document is an operator's own file on standard input, and it is
	// bounded anyway: a settings section is a handful of keys, and reading an
	// unbounded stream from a pipe is a way to run a machine out of memory.
	raw, rerr := io.ReadAll(io.LimitReader(os.Stdin, settingsDocLimit))
	if rerr != nil {
		say(w, "stowcloud %s: settings: reading the document: %v\n", version, rerr)
		return exitConfig
	}
	var body map[string]any
	if jerr := json.Unmarshal(raw, &body); jerr != nil {
		say(w, "stowcloud %s: settings: the document is not a JSON object: %v\n", version, jerr)
		return exitConfig
	}

	st, serr := store.Open(dataDir, store.Options{Clock: clock.System()})
	if serr != nil {
		say(w, "stowcloud %s: settings: opening the store: %v\n", version, serr)
		return exitConfig
	}
	defer func() {
		if cerr := st.Close(); cerr != nil {
			say(w, "stowcloud %s: settings: closing the store: %v\n", version, cerr)
		}
	}()
	if merr := st.State().MergeSettings(context.Background(), section, body); merr != nil {
		say(w, "stowcloud %s: settings: writing %s: %v\n", version, section, merr)
		return exitConfig
	}
	say(w, "wrote the %s section\n", section)
	return exitOK
}

// runSettingsGet prints the whole stored document, which is what an operator
// needs before they can decide what to change.
func runSettingsGet(args []string, w io.Writer) int {
	dataDir, uerr := dataDirArg(args)
	if uerr != nil {
		say(w, "stowcloud %s: settings: %v\n\n", version, uerr)
		return settingsUsage(w)
	}
	st, serr := store.Open(dataDir, store.Options{Clock: clock.System()})
	if serr != nil {
		say(w, "stowcloud %s: settings: opening the store: %v\n", version, serr)
		return exitConfig
	}
	defer func() {
		if cerr := st.Close(); cerr != nil {
			say(w, "stowcloud %s: settings: closing the store: %v\n", version, cerr)
		}
	}()
	all, aerr := st.State().Settings(context.Background())
	if aerr != nil {
		say(w, "stowcloud %s: settings: reading them: %v\n", version, aerr)
		return exitConfig
	}
	body, jerr := json.MarshalIndent(all, "", "  ")
	if jerr != nil {
		say(w, "stowcloud %s: settings: rendering them: %v\n", version, jerr)
		return exitConfig
	}
	say(os.Stdout, "%s\n", body)
	return exitOK
}

// settingsDocLimit bounds the document read from standard input. A section is
// a handful of keys.
const settingsDocLimit = 1 << 20
