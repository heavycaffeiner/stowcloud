// Linux only, because what it tests is.
//go:build linux

package handler

import (
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func findingFor(findings []Finding, field string) (Finding, bool) {
	for _, f := range findings {
		if f.Field == field {
			return f, true
		}
	}
	return Finding{}, false
}

// The finding this endpoint exists for.
//
// A host list that does not name the host the request arrived on takes effect
// immediately and makes every later request, including the one that would undo
// it, answer "misdirected request".
func TestAHostListWithoutTheCallersHostBlocks(t *testing.T) {
	r := httptest.NewRequest("POST", "/api/admin/server-settings/network/check", nil)
	r.Host = "console.example.test:8443"

	got := checkSection(Deps{}, r, "network", map[string]any{
		"app_hosts": []any{"somewhere.else.test"},
	})
	f, ok := findingFor(got, "app_hosts")
	if !ok || f.Level != "block" {
		t.Fatalf("a host list excluding the caller did not block: %+v", got)
	}
	if f.ReasonKey != "settings.would_lock_you_out" {
		t.Errorf("reason = %q", f.ReasonKey)
	}
	// The host is named, because "this would lock you out" without saying
	// which host leaves an administrator guessing which entry to add.
	if f.ReasonParams["host"] != "console.example.test" {
		t.Errorf("host = %q, want the port stripped", f.ReasonParams["host"])
	}
}

// The same list including the caller passes, so the check is answering the
// real question rather than refusing every change to the host list.
func TestAHostListNamingTheCallerPasses(t *testing.T) {
	r := httptest.NewRequest("POST", "/", nil)
	r.Host = "console.example.test"

	got := checkSection(Deps{}, r, "network", map[string]any{
		"app_hosts": []any{"console.example.test", "other.test"},
	})
	if blocked(got) {
		t.Fatalf("a list naming the caller was blocked: %+v", got)
	}
}

// The guard compares exactly, so a wildcard is not a match and the check must
// not treat it as one. Accepting it here would pass a list the guard rejects,
// producing the lockout this check exists to prevent.
func TestAWildcardDoesNotCountAsTheCallersHost(t *testing.T) {
	r := httptest.NewRequest("POST", "/", nil)
	r.Host = "console.example.test"

	got := checkSection(Deps{}, r, "network", map[string]any{
		"app_hosts": []any{"*.example.test"},
	})
	if !blocked(got) {
		t.Fatalf("a wildcard was accepted as covering the caller: %+v", got)
	}
}

// Case is not significant, matching the guard's own comparison.
func TestTheHostMatchIsCaseInsensitive(t *testing.T) {
	r := httptest.NewRequest("POST", "/", nil)
	r.Host = "Console.Example.Test"

	got := checkSection(Deps{}, r, "network", map[string]any{
		"app_hosts": []any{"console.example.test"},
	})
	if blocked(got) {
		t.Fatalf("a case difference was treated as a different host: %+v", got)
	}
}

// An empty list blocks before the lockout check, since it locks everybody out
// rather than just this caller.
func TestAnEmptyHostListBlocks(t *testing.T) {
	r := httptest.NewRequest("POST", "/", nil)
	r.Host = "console.example.test"

	got := checkSection(Deps{}, r, "network", map[string]any{"app_hosts": []any{}})
	f, ok := findingFor(got, "app_hosts")
	if !ok || f.Level != "block" || f.ReasonKey != "settings.host_list_empty" {
		t.Fatalf("an empty host list did not block: %+v", got)
	}
}

// The homes root is probed by creating, because "the path exists" and "a
// directory can be made here" are different questions.

func TestAHomesRootUnderAMissingParentBlocks(t *testing.T) {
	// A root whose parent does not exist either. The server creates the root
	// and not a chain above it, so this is the case where turning homes on
	// yields a share whose directory can never appear.
	got := checkHomesLive(Deps{}, map[string]any{
		"enabled": true,
		"root":    filepath.Join(t.TempDir(), "missing", "deeper", "homes"),
	})
	if !blocked(got) {
		t.Fatalf("a homes root under a missing parent did not block: %+v", got)
	}
	if f, ok := findingFor(got, "root"); !ok || f.ReasonKey != "settings.dir_does_not_exist" {
		t.Errorf("reason = %+v, want the missing parent named", got)
	}
}

// A writable root passes and says so, so a clean run is distinguishable from
// a check that did not run.
func TestAWritableHomesRootPasses(t *testing.T) {
	got := checkHomesLive(Deps{}, map[string]any{"enabled": true, "root": t.TempDir()})
	if blocked(got) {
		t.Fatalf("a writable root blocked: %+v", got)
	}
	if len(got) == 0 {
		t.Fatal("a writable root produced no finding at all")
	}
}

// Zero is root's group on a sidecar that runs as root.
func TestGidZeroBlocks(t *testing.T) {
	r := httptest.NewRequest("POST", "/", nil)
	got := checkSection(Deps{}, r, "smb", map[string]any{"service_gid": float64(0)})
	if !blocked(got) {
		t.Fatalf("gid 0 did not block: %+v", got)
	}
}

// A workgroup the renderer refuses is refused here, by running the renderer
// rather than a second copy of its rules.
func TestAWorkgroupTheRendererRefusesBlocks(t *testing.T) {
	r := httptest.NewRequest("POST", "/", nil)
	got := checkSection(Deps{}, r, "smb", map[string]any{
		"workgroup": "bad\nname = injected",
	})
	if !blocked(got) {
		t.Fatalf("a workgroup carrying a newline did not block: %+v", got)
	}
}

// The save refuses exactly what the preview blocks. Two sets of rules that can
// disagree is the defect the shared probe exists to prevent.
func TestTheSaveRefusesWhatTheCheckBlocks(t *testing.T) {
	r := httptest.NewRequest("POST", "/", nil)
	r.Host = "console.example.test"
	body := map[string]any{"app_hosts": []any{"somewhere.else.test"}}

	findings := checkSection(Deps{}, r, "network", body)
	if !blocked(findings) {
		t.Fatal("the check did not block")
	}
	err := settingsRefused(findings)
	if err == nil {
		t.Fatal("a blocking finding produced no error for the save path")
	}
	if !strings.Contains(err.Error(), "refused") {
		t.Errorf("error = %v", err)
	}
}
