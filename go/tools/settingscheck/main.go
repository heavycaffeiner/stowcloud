// settingscheck compares the settings keys the client writes against the keys
// something in Go actually reads.
//
// Why it exists: the settings screen passes the client's JSON object through to
// the store unchanged. Nothing in that path decides what a section means, which
// is deliberate (a second definition is a second place to disagree), but it
// means a field name only the client knows is stored happily and read by
// nobody. The save reports success, the value sits in the database, and the
// control does nothing forever.
//
// contractcheck catches the same class in the other direction, a field the
// client reads and the server never sends, by comparing response structs. This
// compares request field names against loader key literals, which is where the
// settings surface can drift.
//
// It is structural, like contractcheck: it reads names out of both sides and
// reports what one writes and the other never looks for. It knows nothing about
// types or semantics.
package main

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
)

// say writes a diagnostic. It is the one place in this command that ignores an
// error, because a message that cannot be printed has nowhere else to go.
func say(w io.Writer, format string, a ...any) {
	fmt.Fprintf(w, format, a...) //nolint:errcheck // see the comment above.
}

// section maps a client request interface to the settings section its body is
// PATCHed to. The name is not derivable: SymlinkPolicyReq writes to
// "symlink-policy", and the route is what decides.
// The security section is absent on purpose: the sandbox policy is reported by
// the snapshot and is not editable from the screen, so the client has no
// request type for it. A mapping here would check that section against nothing.
var section = map[string]string{
	"SmbSettingsReq":     "smb",
	"SearchSettingsReq":  "search",
	"ArchiveSettingsReq": "archive",
	"RateSettingsReq":    "rate",
	"NetworkSettingsReq": "network",
	"DbSettingsReq":      "db",
	"SymlinkPolicyReq":   "symlink-policy",
	"HomesSettingsReq":   "homes",
	"WatchSettingsReq":   "watch",
	"OidcSettingsReq":    "oidc",
	"PathsSettingsReq":   "paths",
}

// allowed names keys the client writes that no loader reads, each with the
// reason it is not a defect. An entry here is a decision somebody made; an
// absence is a defect.
var allowed = map[string]string{
	// Read through a different path than the settings loader.
	"symlink-policy.policy": "per-share, read from the share row rather than the settings document",
	"watch.backend":         "reported read-only by the snapshot; the running backend is detected, not configured",
	"oidc.smb_policy":       "block is the only accepted value, enforced at the handler rather than loaded",

	// Not a setting, and the client type saying otherwise is the mistake this
	// entry records. The callback URI is built per request from the Host the
	// request arrived on, because a deployment reached under several names
	// needs the one that applies to this browser, and the same string has to
	// reach the token exchange byte for byte. A stored list could not do that.
	//
	// So the value an operator types here is stored, never read, never sent
	// back by the snapshot, and never validated. The screen lists the key as
	// editable, so the field renders empty on every load and forgets whatever
	// was entered. Removing it is a client change, which is phase 3's
	// api-consistency work; it is named here so the gate does not report it
	// every run while the reason goes unrecorded.
	"oidc.redirect_uris": "derived per request from the Host header, never stored; the client field is vestigial",

	// The whole "paths" section, which the server reports as builtin and
	// read-only while the client type offers all three as required fields.
	//
	// The server's own comments say why each must not be offered. data_dir "is
	// the one thing that cannot be a setting, because it is where the settings
	// are"; it arrives as the --data-dir process argument. smb.config_dir is
	// "the other side of a container boundary", so moving it here would move
	// only one end of a pair.
	//
	// A write to this section is accepted, stored, and read by nothing, and the
	// snapshot keeps reporting the process's real value, so the screen shows
	// the old path back. Note that the loader does read smb.config_dir, under
	// the smb section: the client writing it as paths.smb_config_dir means even
	// that one lands in a place nothing looks.
	//
	// Fixing it is a client change, which belongs to phase 3's api-consistency
	// work rather than to a gate. Recorded so the reason is written down.
	"paths.data_dir":        "a process argument, reported read-only by the snapshot; it is where the settings live",
	"paths.master_key_file": "a process argument, reported read-only by the snapshot",
	"paths.smb_config_dir":  "read-only by the snapshot, and the loader reads it as smb.config_dir rather than under paths",
}

// clientKeys reads the field names out of each request interface in types.ts.
func clientKeys(src string) map[string]bool {
	out := map[string]bool{}
	field := regexp.MustCompile(`(?m)^\s{2}([a-z][a-z_0-9]*)\??:`)
	for iface, sec := range section {
		open := strings.Index(src, "export interface "+iface+" {")
		if open < 0 {
			continue
		}
		body := src[open:]
		if end := strings.Index(body, "\n}"); end >= 0 {
			body = body[:end]
		}
		for _, m := range field.FindAllStringSubmatch(body, -1) {
			out[sec+"."+m[1]] = true
		}
	}
	return out
}

// loaderKeys reads the section and key literals the loader looks up. Every
// reader in that file names both, adjacent, which is what makes this a text
// scan rather than a type check.
func loaderKeys(src string) map[string]bool {
	out := map[string]bool{}
	pair := regexp.MustCompile(`"([a-z][a-z-]*)",\s*"([a-z][a-z_0-9]*)"`)
	for _, m := range pair.FindAllStringSubmatch(src, -1) {
		out[m[1]+"."+m[2]] = true
	}
	return out
}

func main() {
	if len(os.Args) != 3 {
		say(os.Stderr, "usage: settingscheck <types.ts> <loader.go>\n")
		os.Exit(64)
	}
	types, err := os.ReadFile(os.Args[1])
	if err != nil {
		say(os.Stderr, "settingscheck: %v\n", err)
		os.Exit(2)
	}
	loader, err := os.ReadFile(os.Args[2])
	if err != nil {
		say(os.Stderr, "settingscheck: %v\n", err)
		os.Exit(2)
	}

	writes := clientKeys(string(types))
	reads := loaderKeys(string(loader))

	// An empty side means the scan matched nothing, which would pass silently
	// while checking nothing at all.
	if len(writes) == 0 {
		say(os.Stderr, "settingscheck: no client settings fields found in %s; the scan is broken\n", os.Args[1])
		os.Exit(2)
	}
	if len(reads) == 0 {
		say(os.Stderr, "settingscheck: no loader keys found in %s; the scan is broken\n", os.Args[2])
		os.Exit(2)
	}

	var missing []string
	for k := range writes {
		if reads[k] || allowed[k] != "" {
			continue
		}
		missing = append(missing, k)
	}
	sort.Strings(missing)

	for _, k := range missing {
		say(os.Stdout, "%s: the client writes it and no loader reads it\n", k)
	}
	if len(missing) > 0 {
		say(os.Stderr, "\nsettingscheck: %d settings key(s) stored and never read.\n"+
			"A control the operator moves while nothing happens. Either read the key "+
			"where it is meant to take effect, or record why it is not a setting in the "+
			"allow list with its reason.\n", len(missing))
		os.Exit(1)
	}
}
