package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// write puts one Go file in dir and returns the directory, so a test reads as
// the tree it is describing.
func write(t *testing.T, dir, name, body string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// The long comment is the one the rule is about: prose moved rather than
// rewritten.
func TestACarriedCommentIsReported(t *testing.T) {
	root := t.TempDir()
	carried := "// The share is not registered in this process, so the row stays put."
	oldDir := write(t, filepath.Join(root, "old"), "a.go", "package a\n"+carried+"\n")
	newDir := write(t, filepath.Join(root, "new"), "b.go", "package b\n"+carried+"\n")

	old, err := collect(oldDir)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	n, err := scan(newDir, old, &out)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("found %d carried lines, want 1:\n%s", n, out.String())
	}
	// The report has to name the file and the line, or a finding is a fact
	// nobody can act on.
	if !strings.Contains(out.String(), "b.go:2") {
		t.Errorf("the report does not locate the line:\n%s", out.String())
	}
}

// Restating the same idea in different words is the whole point of the rule, so
// it must not be flagged.
func TestARestatedCommentPasses(t *testing.T) {
	root := t.TempDir()
	oldDir := write(t, filepath.Join(root, "old"), "a.go",
		"package a\n// The share is not registered in this process, so the row stays put.\n")
	newDir := write(t, filepath.Join(root, "new"), "b.go",
		"package b\n// This process has not registered the share, so the row is left alone.\n")

	old, err := collect(oldDir)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	n, err := scan(newDir, old, &out)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("a restated comment was flagged:\n%s", out.String())
	}
}

// Indentation and trailing space are formatting rather than content, so a line
// moved into a deeper block is still the same carried line.
func TestWhitespaceDoesNotHideACarriedLine(t *testing.T) {
	root := t.TempDir()
	body := "The kernel dropped events faster than they were read, so the set is short."
	oldDir := write(t, filepath.Join(root, "old"), "a.go", "package a\n// "+body+"\n")
	newDir := write(t, filepath.Join(root, "new"), "b.go", "package b\nfunc f() {\n\t\t//  "+body+"  \n}\n")

	old, err := collect(oldDir)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	n, err := scan(newDir, old, &out)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("found %d, want 1: indentation hid a carried line:\n%s", n, out.String())
	}
}

// Short lines recur between any two files by habit. Flagging them would bury
// the findings that matter.
func TestShortLinesAreNotCompared(t *testing.T) {
	root := t.TempDir()
	short := "// Never nil."
	if len(strings.TrimPrefix(short, "// ")) >= minLen {
		t.Fatal("the fixture is not shorter than the threshold")
	}
	oldDir := write(t, filepath.Join(root, "old"), "a.go", "package a\n"+short+"\n")
	newDir := write(t, filepath.Join(root, "new"), "b.go", "package b\n"+short+"\n")

	old, err := collect(oldDir)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	n, err := scan(newDir, old, &out)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("a short line was compared:\n%s", out.String())
	}
}

// Code that happens to match is not a comment, and a gate that read it would
// report a finding nobody can fix.
func TestOnlyCommentsAreCompared(t *testing.T) {
	root := t.TempDir()
	text := "the quick brown fox jumps over the lazy dog again"
	oldDir := write(t, filepath.Join(root, "old"), "a.go", "package a\n// "+text+"\n")
	newDir := write(t, filepath.Join(root, "new"), "b.go", "package b\nvar s = \""+text+"\"\n")

	old, err := collect(oldDir)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	n, err := scan(newDir, old, &out)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("a string literal was read as a comment:\n%s", out.String())
	}
}

func TestNonGoFilesAreIgnored(t *testing.T) {
	root := t.TempDir()
	carried := "// The share is not registered in this process, so the row stays put."
	oldDir := write(t, filepath.Join(root, "old"), "a.go", "package a\n"+carried+"\n")

	newDir := filepath.Join(root, "new")
	write(t, newDir, "b.go", "package b\n")
	if err := os.WriteFile(filepath.Join(newDir, "notes.md"), []byte(carried+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	old, err := collect(oldDir)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	n, err := scan(newDir, old, &out)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("a non-Go file was scanned:\n%s", out.String())
	}
}

// The live tree is the assertion that matters: this is the state the sweep
// left, and the gate exists to keep it.
func TestTheEngineCarriesNothing(t *testing.T) {
	oldDir := filepath.Join("..", "..", "internal")
	newDir := filepath.Join("..", "..", "engine")
	for _, d := range []string{oldDir, newDir} {
		if _, err := os.Stat(d); err != nil {
			t.Skipf("%s is not present in this checkout", d)
		}
	}

	old, err := collect(oldDir)
	if err != nil {
		t.Fatal(err)
	}
	// A reference set that came back empty would pass this test without
	// checking anything.
	if len(old) == 0 {
		t.Fatal("no comments collected from the old tree")
	}

	var out bytes.Buffer
	n, err := scan(newDir, old, &out)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%d carried comment line(s) in the engine:\n%s", n, out.String())
	}
}
