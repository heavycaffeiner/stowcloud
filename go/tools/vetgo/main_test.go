package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReportsEveryGoStatementInTheFixture(t *testing.T) {
	out, n := run(t, filepath.Join("testdata", "bad"))
	if n != 2 {
		t.Fatalf("found %d go statements, want 2:\n%s", n, out)
	}
	if !strings.Contains(out, "bad.go:8") || !strings.Contains(out, "bad.go:14") {
		t.Fatalf("did not report both positions:\n%s", out)
	}
}

func TestAcceptsTheOneSpawningPackage(t *testing.T) {
	out, n := run(t, filepath.Join("testdata", "good"))
	if n != 0 {
		t.Fatalf("reported %d go statements in internal/task:\n%s", n, out)
	}
}

// run drives check against a real file tree, because the rule is about where a
// file sits and a synthetic parse would not exercise that.
func run(t *testing.T, root string) (string, int) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "vetgo")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close() //nolint:errcheck // closing a temporary file the test wrote is not a data path.
	n, err := check(root, f)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	return string(b), n
}
