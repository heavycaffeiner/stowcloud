//go:build linux

package lifecycle_test

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// Turning home folders on gives every account a folder, named after it.
//
// The setting used to do nothing at all: the value was stored, validated and
// shown on the settings screen, and no code path ever called EnableHomes. An
// operator following the screen's own description ("each account gets a
// personal folder under this path") got nothing, and no restart helped, because
// nothing anywhere consumed the value.
func TestTurningHomeFoldersOnCreatesThem(t *testing.T) {
	root := filepath.Join(t.TempDir(), "homes")

	base, cookie, csrf, plainCookie, _ := adminEngine(t)

	if status, body := mutate(t, http.MethodPatch, base+"/api/v1/admin/settings/homes",
		cookie, csrf, map[string]any{"enabled": true, "root": root}); status != http.StatusOK {
		t.Fatalf("turning home folders on answered %d: %v", status, body)
	}

	// The ordinary account's own listing is what runs the per-account
	// creation: the projected root is built from grants, and the home hook
	// fills one in on the way past.
	if status, body := withCookie(t, http.MethodGet,
		base+"/api/v1/files/list?path=/", plainCookie); status != http.StatusOK {
		t.Fatalf("listing the root answered %d: %s", status, body)
	}

	// Named after the account, which is the whole point of the naming: an
	// operator in a shell or on an SMB mount can tell whose folder is whose.
	if _, err := os.Stat(filepath.Join(root, loginName)); err != nil {
		t.Errorf("no home folder was created for %q: %v", loginName, err)
	}
}
