package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReportsEveryKoreanLine(t *testing.T) {
	out, n := run(t, filepath.Join("testdata", "bad"))
	if n != 3 {
		t.Fatalf("found %d lines, want 3:\n%s", n, out)
	}
}

func TestStaysQuietOnEnglish(t *testing.T) {
	out, n := run(t, filepath.Join("testdata", "good"))
	if n != 0 {
		t.Fatalf("false positives:\n%s", out)
	}
}

// The gate this replaces reported PASS on the development host whatever the
// tree contained, because grep -P there does not interpret \x{...} as a code
// point. A table lookup is the same on every host, and this is the assertion
// that says so.
func TestTheScanIsNotHostDependent(t *testing.T) {
	_, n := run(t, filepath.Join("testdata", "bad"))
	if n == 0 {
		t.Fatal("the scan found nothing in a fixture that is entirely Korean")
	}
}

func run(t *testing.T, root string) (string, int) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "koscan")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close() //nolint:errcheck // closing a temporary file the test wrote is not a data path.
	n, err := scan(root, f)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	return string(b), n
}
