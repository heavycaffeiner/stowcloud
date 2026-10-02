package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestCheck(t *testing.T) {
	const mod = `"github.com/heavycaffeiner/stowcloud/backend/internal/`
	for _, tc := range []struct {
		name, file, src string
		want            int
	}{
		{"platform to platform", "platform/clock/clock.go", "package clock\nimport " + mod + `platform/number"`, 0},
		{"platform to a feature", "platform/clock/clock.go", "package clock\nimport " + mod + `files"`, 1},
		{"feature to platform", "files/files.go", "package files\nimport " + mod + `platform/clock"`, 0},
		{"feature to server", "files/files.go", "package files\nimport " + mod + `server"`, 1},
		{"feature to a server subpackage", "files/files.go", "package files\nimport " + mod + `server/httpx"`, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			full := filepath.Join(root, filepath.FromSlash(tc.file))
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, []byte(tc.src), 0o644); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			n, err := check(root, &out)
			if err != nil {
				t.Fatal(err)
			}
			if n != tc.want {
				t.Fatalf("got %d refusals, want %d:\n%s", n, tc.want, out.String())
			}
		})
	}
}
