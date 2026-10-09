//go:build linux

package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/backend/internal/platform/clock"
)

func TestFailedCredentialSynchronizationStopsExistingConnections(t *testing.T) {
	for _, failure := range []string{"import", "revoke"} {
		t.Run(failure, func(t *testing.T) {
			dir := t.TempDir()
			write := func(name, body string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			write("smbd", "#!/bin/sh\nsleep 30\n")
			write("testparm", "#!/bin/sh\nexit 0\n")
			if failure == "import" {
				write("pdbedit", "#!/bin/sh\nexit 1\n")
			} else {
				write("pdbedit", "#!/bin/sh\ncase \"$1\" in\n-L) echo 'alice:200001:'; exit 0;;\n-x) exit 1;;\nesac\nexit 0\n")
			}
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			config := filepath.Join(dir, "config")
			if err := os.Mkdir(config, 0o750); err != nil {
				t.Fatal(err)
			}
			write("config/smb.conf", "[global]\n  interfaces = lo\n")
			write("config/smbpasswd", "")
			write("passwd", "root:x:0:0::/root:/bin/sh\n")
			write("group", "root:x:0:\n")
			paths := Paths{ConfigDir: config, StateDir: dir, SmbConf: filepath.Join(dir, "active.conf"), Passwd: filepath.Join(dir, "passwd"), Group: filepath.Join(dir, "group"), Passdb: filepath.Join(dir, "passdb")}
			a := NewAgent(paths, quiet(), clock.System())
			if err := a.smbd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := a.Shutdown(); err != nil {
					t.Error(err)
				}
			})
			report := a.Apply(context.Background())
			if report.OK || report.Smbd != ActionFailed || !strings.Contains(report.Error, "SMB stopped") {
				t.Fatalf("credential failure was reported as applied: %+v", report)
			}
			if a.SmbdRunning() {
				t.Fatal("old credentials remain usable after failed synchronization")
			}
		})
	}
}
