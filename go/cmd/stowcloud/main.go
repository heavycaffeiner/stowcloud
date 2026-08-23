// Command stowcloud is the whole product. Dispatch is on argv[1] before any
// flag parsing, so a subcommand costs nothing in the flag set and works in a
// shell-less image where Docker's exec-form HEALTHCHECK runs an argv directly.
//
// One binary rather than two: a second one roughly doubles the image for no
// gain when the process can exec its own path with a different argument.
package main

import (
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
)

// version is stamped by the linker with -X main.version=...
//
//nolint:gochecknoglobals // a linker-set value has to be a package-level var.
var version = "dev"

// Exit codes. A degraded server is a configuration state, so healthcheck
// reports 0 for both ok and degraded and 1 only when nothing answered at all;
// mapping degraded to unhealthy makes Docker restart-loop a problem forever.
const (
	exitOK       = 0
	exitNoAnswer = 1
	exitUsage    = 64
	exitConfig   = 78
)

// command is one entry in the dispatch table. verbs is empty for a command that
// takes none; a flag such as routes --json is not a verb, because dispatch
// stops at argv[1].
type command struct {
	name    string
	verbs   []string
	summary string
}

// table is a function rather than a package-level var so that the dispatch set
// is a compile-time table by construction and not by convention.
func table() []command {
	return []command{
		{name: "serve", summary: "the server, and the default with no argv"},
		{name: "healthcheck", summary: "loopback TLS probe, exit 0 on ok or degraded"},
		{name: "preview-worker", summary: "the jailed decoder; never run by hand"},
		{name: "caps", summary: "print the kernel capability probe and exit"},
		{name: "setup", summary: "print and persist a one-time setup token"},
		{name: "gc", summary: "incremental vacuum, trash and upload sweeps"},
		{name: "routes", summary: "dump the route table; --json for machine use"},
		{name: "smb-sync", summary: "render smb.conf, smbpasswd and passwd"},
		{name: "index", verbs: []string{"build", "merge", "status"},
			summary: "name-index maintenance"},
		{name: "masterkey", verbs: []string{"rotate"},
			summary: "new key, re-encrypt every secret, swap"},
	}
}

// say writes a diagnostic. It is the one place in this command that ignores an
// error, because a message that cannot be printed has nowhere else to go.
func say(w io.Writer, format string, a ...any) {
	fmt.Fprintf(w, format, a...) //nolint:errcheck // see the comment above.
}

func main() { os.Exit(run(os.Args[1:], os.Stderr)) }

func run(args []string, stderr io.Writer) int {
	if len(args) == 0 {
		args = []string{"serve"}
	}

	commands := table()
	i := slices.IndexFunc(commands, func(c command) bool { return c.name == args[0] })
	if i < 0 {
		say(stderr, "stowcloud %s: unknown subcommand %q\n\n", version, args[0])
		usage(stderr)
		return exitUsage
	}

	cmd := commands[i]
	if cmd.name == "caps" {
		// The one command Phase 1 implements, because the phase's claim is that
		// the kernel confines a share and this is what executes that claim
		// against the container an operator actually deployed.
		return printCaps(stderr)
	}
	if cmd.name == "serve" {
		return runServe(args[1:], stderr)
	}
	if cmd.name == "healthcheck" {
		return runHealthcheck(args[1:], stderr)
	}
	if cmd.name == "setup" {
		return runSetup(args[1:], stderr)
	}
	if cmd.name == "preview-worker" {
		return runPreviewWorker(args[1:], stderr)
	}
	if len(args) > 1 && cmd.name == "masterkey" && args[1] == "rotate" {
		// Phase 3's command: re-seal every ciphertext under a new key and swap
		// the ring file. It is a CLI and never an HTTP route, because a master
		// key must not reach a browser tab.
		return runMasterkeyRotate(args[2:], stderr)
	}

	spoken := cmd.name
	if len(cmd.verbs) > 0 {
		if len(args) < 2 || !slices.Contains(cmd.verbs, args[1]) {
			say(stderr, "stowcloud %s: %s takes one of: %s\n",
				version, cmd.name, strings.Join(cmd.verbs, ", "))
			return exitUsage
		}
		spoken += " " + args[1]
	}

	// Phase 0 ships the gate, not the product. Every command below is written
	// by the phase that owns it, and until then saying so on stderr and
	// failing is the only honest answer: exit 0 here would tell Docker's
	// health probe that a server answered.
	say(stderr, "stowcloud %s: %s is not implemented yet\n", version, spoken)
	return exitNoAnswer
}

func usage(w io.Writer) {
	say(w, "usage: stowcloud <subcommand> [arguments]\n\n")
	for _, c := range table() {
		name := c.name
		if len(c.verbs) > 0 {
			name += " " + strings.Join(c.verbs, "|")
		}
		say(w, "  %-28s %s\n", name, c.summary)
	}
}
