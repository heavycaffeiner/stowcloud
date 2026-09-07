//go:build linux && compat_nc

package lifecycle_test

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/lifecycle"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
)

// A trash row can be acted on at the address the listing gave it.
//
// The app keeps the href it was shown, minus the DAV prefix, and builds its
// delete and restore back onto that. An href spelled any other way than the
// address the client used therefore names a row it cannot reach: the listing
// hung its members off a fixed path with the account segment missing, so
// emptying one item and restoring one both answered 404 while the trash screen
// itself looked fine.
func TestATrashRowIsReachableAtTheHrefItWasListedAt(t *testing.T) {
	t.Parallel()

	e, openErr := lifecycle.Open(context.Background(), lifecycle.Options{DataDir: t.TempDir()})
	if openErr != nil {
		t.Fatalf("opening the engine: %v", openErr)
	}
	t.Cleanup(func() {
		if cerr := e.Close(); cerr != nil {
			t.Errorf("closing the engine: %v", cerr)
		}
	})

	ctx := context.Background()
	if rerr := e.Core.RegisterShare(ctx, core.ShareDef{
		ID: 1, Name: "files", Host: t.TempDir(), Policy: vfs.DefaultSharePolicy(), TrashEnabled: true,
	}); rerr != nil {
		t.Fatalf("registering the share: %v", rerr)
	}
	uid, aerr := e.Auth.CreateAdmin(ctx, "alice", "Alice", pwOf(loginPassword))
	if aerr != nil {
		t.Fatalf("creating the account: %v", aerr)
	}
	if gerr := e.Core.GrantEveryShare(ctx, uid); gerr != nil {
		t.Fatalf("granting: %v", gerr)
	}
	base := serveCompatEngine(t, e)
	credential := davAuth(t, e, base, uid)
	client := compatClient()
	t.Cleanup(client.CloseIdleConnections)

	// The address the app lists its trash at, account segment and all.
	trash := base + "/remote.php/dav/trashbin/alice/trash"
	rowHref := func(name string) string {
		t.Helper()
		if code, body := davSend(t, client, credential, http.MethodPut,
			base+"/remote.php/dav/files/alice/files/"+name,
			strings.NewReader("bytes"), nil); code != http.StatusCreated {
			t.Fatalf("writing %s answered %d: %s", name, code, body)
		}
		if code, body := davSend(t, client, credential, http.MethodDelete,
			base+"/remote.php/dav/files/alice/files/"+name, nil, nil); code != http.StatusNoContent {
			t.Fatalf("deleting %s answered %d: %s", name, code, body)
		}
		ask := `<?xml version="1.0"?><d:propfind xmlns:d="DAV:" xmlns:nc="http://nextcloud.org/ns"><d:prop><nc:trashbin-filename/></d:prop></d:propfind>`
		code, body := davSend(t, client, credential, "PROPFIND", trash,
			strings.NewReader(ask), map[string]string{"Depth": "1", "Content-Type": "text/xml"})
		if code != http.StatusMultiStatus {
			t.Fatalf("listing the trash answered %d: %s", code, body)
		}
		for _, m := range regexp.MustCompile(`<d:href>([^<]*)</d:href>`).FindAllStringSubmatch(body, -1) {
			if strings.Contains(m[1], ":") {
				return m[1]
			}
		}
		t.Fatalf("the trash listing carried no member: %s", body)
		return ""
	}

	// What the client holds: the href with the DAV prefix removed, which it
	// rebuilds onto the same prefix to act.
	actOn := func(href string) string {
		t.Helper()
		parts := strings.SplitN(href, "/remote.php/dav", 2)
		if len(parts) != 2 {
			t.Fatalf("the href is not under the DAV mount: %q", href)
		}
		return base + "/remote.php/dav" + parts[1]
	}

	// Emptying one row.
	purge := actOn(rowHref("purge-me.txt"))
	if code, body := davSend(t, client, credential, http.MethodDelete, purge, nil, nil); code != http.StatusNoContent {
		t.Errorf("deleting the row the listing named answered %d: %s", code, body)
	}

	// Restoring another.
	restore := actOn(rowHref("restore-me.txt"))
	if code, body := davSend(t, client, credential, "MOVE", restore, nil, map[string]string{
		"Destination": base + "/remote.php/dav/trashbin/alice/restore/restore-me.txt",
	}); code != http.StatusCreated && code != http.StatusNoContent {
		t.Errorf("restoring the row the listing named answered %d: %s", code, body)
	}
	if code, got := davSend(t, client, credential, http.MethodGet,
		base+"/remote.php/dav/files/alice/files/restore-me.txt", nil, nil); code != http.StatusOK || got != "bytes" {
		t.Errorf("the restored file answered %d holding %q", code, got)
	}
}
