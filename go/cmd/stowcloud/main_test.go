package main

import (
	"slices"
	"strings"
	"testing"
)

// The subcommand set is a contract with the image: Docker's exec-form
// HEALTHCHECK runs an argv, and the operator-triggered offline operations have
// no route to reach them by. A name dropped here is a surface that silently
// stops existing.
func TestTheSubcommandSet(t *testing.T) {
	want := []string{
		"serve", "healthcheck", "preview-worker", "caps", "setup", "gc",
		"routes", "smb-sync", "index", "masterkey", "migrate",
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
		{"masterkey", "rotate"},
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
