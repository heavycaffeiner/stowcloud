// Linux only, like the command it tests.
//go:build linux

package main

import (
	"slices"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/internal/server"
)

// The subcommand set is a contract with the image: Docker's exec-form
// HEALTHCHECK runs an argv, and the operator-triggered offline operations have
// no route to reach them by. A name dropped here is a surface that silently
// stops existing.
func TestTheSubcommandSet(t *testing.T) {
	want := []string{
		"serve", "healthcheck", "preview-worker", "caps", "setup", "gc",
		"routes", "smb-sync", "index", "masterkey",
	}
	var got []string
	for _, c := range table() {
		got = append(got, c.name)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("subcommands = %v\nwant        = %v", got, want)
	}
}

func TestNoArgvIsServe(t *testing.T) {
	var out strings.Builder
	if code := run(nil, &out); code == exitUsage {
		t.Fatalf("empty argv was a usage error: %s", out.String())
	}
	if !strings.Contains(out.String(), "serve") {
		t.Fatalf("empty argv did not dispatch to serve: %s", out.String())
	}
}

func TestUnknownSubcommandIsAUsageError(t *testing.T) {
	var out strings.Builder
	if code := run([]string{"nope"}, &out); code != exitUsage {
		t.Fatalf("exit = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(out.String(), "usage:") {
		t.Fatalf("no usage printed: %s", out.String())
	}
}

func TestAVerbedCommandRefusesAMissingVerb(t *testing.T) {
	for _, args := range [][]string{{"index"}, {"index", "rebuild"}, {"masterkey"}} {
		var out strings.Builder
		if code := run(args, &out); code != exitUsage {
			t.Errorf("run(%v) = %d, want %d", args, code, exitUsage)
		}
	}
}

func TestAVerbedCommandAcceptsItsVerbs(t *testing.T) {
	for _, args := range [][]string{
		{"index", "build"}, {"index", "merge"}, {"index", "status"},
		{"masterkey", "rotate", t.TempDir()},
	} {
		var out strings.Builder
		if code := run(args, &out); code == exitUsage {
			t.Errorf("run(%v) was a usage error: %s", args, out.String())
		}
	}
}

// The exit-code vocabulary is fixed by the image and the health probe, so it is
// asserted rather than left to whoever writes the first command.
func TestExitCodes(t *testing.T) {
	for name, code := range map[string]int{
		"ok": exitOK, "no answer": exitNoAnswer,
		"usage": exitUsage, "config refused": exitConfig,
	} {
		if code < 0 || code > 78 {
			t.Errorf("%s = %d, outside the documented set", name, code)
		}
	}
	if exitOK != 0 || exitNoAnswer != 1 || exitUsage != 64 || exitConfig != 78 {
		t.Fatal("the exit codes moved")
	}
}

// Every configured share is inside the sandbox.
//
// The domain is built before the shares are registered, so on a first run the
// database holds none and a domain built from it alone grants no share path at
// all. The kernel then denies every listing while the share table and the
// grants both look correct, which reads as a permission bug anywhere but where
// it is. This is what a fresh container deployment does.
func TestTheJailGrantsEveryConfiguredShare(t *testing.T) {
	cfg := &server.Config{
		DataDir: "/var/lib/stowcloud",
		Shares: []server.ShareConfig{
			{Name: "files", Host: "/shares/files"},
			{Name: "media", Host: "/srv/media"},
		},
	}

	spec := jailSpec(cfg, "/etc/stowcloud/sc.toml", nil)
	granted := map[string]bool{}
	for _, g := range spec.GrantBeneath {
		granted[g.Path] = true
	}
	for _, sh := range cfg.Shares {
		if !granted[sh.Host] {
			t.Errorf("share %q at %s is outside the sandbox", sh.Name, sh.Host)
		}
	}
	if !granted[cfg.DataDir] {
		t.Error("the data directory is outside the sandbox")
	}
}
