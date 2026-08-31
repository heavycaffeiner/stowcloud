// Linux only, matching the file under test.
//go:build linux

package server

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func probePath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), ".probe.json")
}

// The snapshot round-trips, and the file is the owner's alone.
func TestTheProbeSnapshotRoundTrips(t *testing.T) {
	path := probePath(t)
	want := Probe{Addr: "127.0.0.1:8443", Host: "app.example.test"}

	if err := WriteProbe(path, want, durable); err != nil {
		t.Fatalf("WriteProbe: %v", err)
	}
	got := ReadProbe(path)
	if got != want {
		t.Errorf("the snapshot read back as %+v", got)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("the snapshot has mode %v", info.Mode().Perm())
	}
}

// The snapshot says where to knock and nothing about what is behind the door.
func TestTheSnapshotCarriesOnlyTheAddress(t *testing.T) {
	path := probePath(t)
	if err := WriteProbe(path, Probe{Addr: "127.0.0.1:8443", Host: "app.example.test"}, durable); err != nil {
		t.Fatalf("WriteProbe: %v", err)
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading: %v", err)
	}
	// Two keys, no more. A snapshot that grew a field would be a file an
	// unauthenticated reader learns something new from.
	var raw map[string]any
	if derr := json.Unmarshal(body, &raw); derr != nil {
		t.Fatalf("decoding: %v", derr)
	}
	for k := range raw {
		if k != "addr" && k != "host" {
			t.Errorf("the snapshot carries the field %q", k)
		}
	}
	if len(raw) > 2 {
		t.Errorf("the snapshot carries %d fields", len(raw))
	}
}

// An unreadable snapshot falls back rather than failing, since a healthcheck
// that cannot find the address is more useful trying the default.
func TestAnUnreadableSnapshotFallsBack(t *testing.T) {
	dir := t.TempDir()

	for _, c := range []struct{ what, body string }{
		{"absent", ""},
		{"not json", "not json at all"},
		{"an empty object", "{}"},
		{"a null address", `{"addr":null}`},
		{"a blank address", `{"addr":"   "}`},
		{"truncated", `{"addr":"127.0`},
	} {
		path := filepath.Join(dir, strings.ReplaceAll(c.what, " ", "-")+".json")
		if c.body != "" {
			if err := os.WriteFile(path, []byte(c.body), 0o600); err != nil {
				t.Fatalf("writing %s: %v", c.what, err)
			}
		}
		got := ReadProbe(path)
		if got.Addr != ProbeFallbackAddr {
			t.Errorf("%s read as %q, want the fallback", c.what, got.Addr)
		}
	}
}

// An oversized file is not read. What this server writes is under a hundred
// bytes, so anything larger was written by something else.
func TestAnOversizedSnapshotIsNotRead(t *testing.T) {
	path := probePath(t)
	huge := `{"addr":"127.0.0.1:9999","host":"` + strings.Repeat("a", 100<<10) + `"}`
	if err := os.WriteFile(path, []byte(huge), 0o600); err != nil {
		t.Fatalf("writing: %v", err)
	}

	got := ReadProbe(path)
	if got.Addr != ProbeFallbackAddr {
		t.Errorf("an oversized snapshot read as %q", got.Addr)
	}
}

// A deployment with no configured host writes none, rather than an empty
// string a reader would have to test for.
func TestNoHostIsAbsentFromTheSnapshot(t *testing.T) {
	path := probePath(t)
	if err := WriteProbe(path, Probe{Addr: "0.0.0.0:8443"}, durable); err != nil {
		t.Fatalf("WriteProbe: %v", err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading: %v", err)
	}
	if strings.Contains(string(body), "host") {
		t.Errorf("a hostless deployment wrote %s", body)
	}
}

// Only an answer that never arrived, or one this build cannot read, is
// unhealthy. A supervisor restarting on a reported problem restarts a server
// whose configuration is wrong, which does not fix a configuration.
func TestOnlyAnUnreadableAnswerIsUnhealthy(t *testing.T) {
	for _, status := range []string{"ok", "degraded", "failing"} {
		if got := HealthExitFor(status, nil); got != HealthExitOK {
			t.Errorf("the status %q exited %d", status, got)
		}
	}

	if got := HealthExitFor("ok", errors.New("connection refused")); got != HealthExitUnhealthy {
		t.Errorf("an answer that never arrived exited %d", got)
	}
	// A status this build does not know is not a valid answer: it is the shape
	// of something else answering on that port.
	for _, status := range []string{"", "healthy", "OK", "yes"} {
		if got := HealthExitFor(status, nil); got != HealthExitUnhealthy {
			t.Errorf("the unknown status %q exited %d", status, got)
		}
	}
}

// A concurrent reader sees one snapshot or the other, never a half-written
// one, which is what the durable write is for.
func TestAReaderNeverSeesAHalfWrittenSnapshot(t *testing.T) {
	path := probePath(t)
	if err := WriteProbe(path, Probe{Addr: "127.0.0.1:1111"}, durable); err != nil {
		t.Fatalf("the first write: %v", err)
	}

	// A write that is interrupted leaves the previous snapshot readable rather
	// than a truncated one.
	err := WriteProbe(path, Probe{Addr: "127.0.0.1:2222"}, func(
		paths []string, modes []uint32, write func(i int, f *os.File) error,
	) error {
		return errors.New("the write was interrupted")
	})
	if err == nil {
		t.Fatal("the interrupted write reported success")
	}

	got := ReadProbe(path)
	if got.Addr != "127.0.0.1:1111" {
		t.Errorf("after an interrupted write the snapshot reads %q", got.Addr)
	}
}
