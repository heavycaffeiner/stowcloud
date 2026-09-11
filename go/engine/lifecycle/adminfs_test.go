//go:build linux

package lifecycle_test

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// hostListing mirrors handler.HostListingView, decoded locally so a test
// checks the bytes on the wire rather than a type shared with the handler.
type hostListing struct {
	Path    string `json:"path"`
	Parent  string `json:"parent"`
	Entries []struct {
		Name  string `json:"name"`
		Path  string `json:"path"`
		IsDir bool   `json:"is_dir"`
	} `json:"entries"`
	Truncated bool `json:"truncated"`
}

// decodeListing reads a browse response body.
func decodeListing(t *testing.T, body []byte) hostListing {
	t.Helper()
	var out hostListing
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decoding the listing: %v\n%s", err, body)
	}
	return out
}

// Browsing the host filesystem is an administrative action: an ordinary
// account has no path field that opens on the strength of a listing, so it
// gets the same refusal as every other admin route.
func TestFsBrowseNeedsAnAdministrator(t *testing.T) {
	t.Parallel()
	base, _, _, plainCookie, _ := adminEngine(t)

	code, _ := withCookie(t, http.MethodGet, base+"/api/v1/admin/fs", plainCookie)
	if code != http.StatusForbidden {
		t.Errorf("an ordinary account browsed the host filesystem: %d", code)
	}
}

// A listing draws directories before files, each group folded to lower
// case, which is what makes it read as a file manager's view rather than a
// raw directory dump.
func TestFsBrowseListsADirectoryWithDirectoriesFirst(t *testing.T) {
	t.Parallel()
	base, cookie, _, _, _ := adminEngine(t)

	dir := t.TempDir()
	for _, name := range []string{"zebra.txt", "apple.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatalf("seeding %s: %v", name, err)
		}
	}
	for _, name := range []string{"beta", "alpha"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0o700); err != nil {
			t.Fatalf("seeding %s: %v", name, err)
		}
	}

	code, body := withCookie(t, http.MethodGet,
		base+"/api/v1/admin/fs?path="+urlEscape(dir), cookie)
	if code != http.StatusOK {
		t.Fatalf("browsing answered %d: %s", code, body)
	}

	listing := decodeListing(t, body)
	if len(listing.Entries) != 4 {
		t.Fatalf("got %d entries, want 4: %+v", len(listing.Entries), listing.Entries)
	}
	want := []string{"alpha", "beta", "apple.txt", "zebra.txt"}
	for i, name := range want {
		if listing.Entries[i].Name != name {
			t.Errorf("entry %d is %q, want %q", i, listing.Entries[i].Name, name)
		}
	}
	if !listing.Entries[0].IsDir || !listing.Entries[1].IsDir {
		t.Error("the first two entries, the subdirectories, are not reported as directories")
	}
	if listing.Entries[2].IsDir || listing.Entries[3].IsDir {
		t.Error("the last two entries, the files, are reported as directories")
	}
}

// A relative path names nothing on the host filesystem in particular, so it
// is refused before any syscall would have to pick one interpretation of it.
func TestFsBrowseRefusesARelativePath(t *testing.T) {
	t.Parallel()
	base, cookie, _, _, _ := adminEngine(t)

	code, body := withCookie(t, http.MethodGet, base+"/api/v1/admin/fs?path=relative/dir", cookie)
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("a relative path answered %d: %s", code, body)
	}
}

// A path that does not exist is not found, distinct from one this process is
// merely forbidden to see.
func TestFsBrowseAMissingPathIsNotFound(t *testing.T) {
	t.Parallel()
	base, cookie, _, _, _ := adminEngine(t)

	missing := filepath.Join(t.TempDir(), "does-not-exist")
	code, body := withCookie(t, http.MethodGet,
		base+"/api/v1/admin/fs?path="+urlEscape(missing), cookie)
	if code != http.StatusNotFound {
		t.Fatalf("a missing path answered %d: %s", code, body)
	}
}

// The root listing is the fixed starting points, every one an absolute
// directory: a client offers each straight away, with nothing to resolve
// before descending into it.
func TestFsBrowseRootListingAnswersAbsoluteDirectories(t *testing.T) {
	t.Parallel()
	base, cookie, _, _, _ := adminEngine(t)

	code, body := withCookie(t, http.MethodGet, base+"/api/v1/admin/fs", cookie)
	if code != http.StatusOK {
		t.Fatalf("the root listing answered %d: %s", code, body)
	}

	listing := decodeListing(t, body)
	if len(listing.Entries) == 0 {
		t.Fatal("the root listing named no starting points")
	}
	for _, e := range listing.Entries {
		if !filepath.IsAbs(e.Path) {
			t.Errorf("entry %q is not absolute", e.Path)
		}
		if e.Name != e.Path {
			t.Errorf("entry name %q does not match its path %q", e.Name, e.Path)
		}
		if !e.IsDir {
			t.Errorf("entry %q is not reported as a directory", e.Path)
		}
	}
	if listing.Parent != "" {
		t.Errorf("the root listing names a parent: %q", listing.Parent)
	}
}
