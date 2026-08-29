//go:build linux

package lifecycle_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/secret"
	"github.com/heavycaffeiner/stowcloud/go/engine/lifecycle"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/auth"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
)

// shareWith serves an engine whose account holds one share with exactly the
// named permissions, and returns the base URL, a token and the share name.
//
// The permissions are the point: each write route needs a different one, and a
// route that resolved with the wrong permission would be served by an account
// that was never granted it.
func shareWith(t *testing.T, perms acl.Perms) (base, token, share string) {
	t.Helper()
	ctx := context.Background()

	e, err := lifecycle.Open(ctx, lifecycle.Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	t.Cleanup(func() {
		if cerr := e.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	})

	id, err := e.Auth.CreateUser(ctx, "alice", "Alice", secret.New([]byte("a-long-enough-password")))
	if err != nil {
		t.Fatalf("creating the account: %v", err)
	}

	host := t.TempDir()
	if werr := os.WriteFile(filepath.Join(host, "existing.txt"), []byte("x"), 0o600); werr != nil {
		t.Fatalf("writing: %v", werr)
	}

	sh, err := e.Core.CreateShare(ctx, core.ShareSpec{Name: "work", Host: host})
	if err != nil {
		t.Fatalf("creating the share: %v", err)
	}
	if _, gerr := e.Core.CreateGrant(ctx, core.GrantSpec{
		User: &id, Share: sh.ID, Allow: perms, Inherit: true, Label: sh.Name,
	}); gerr != nil {
		t.Fatalf("granting: %v", gerr)
	}
	if rerr := e.Core.ReloadGrants(ctx); rerr != nil {
		t.Fatalf("reloading: %v", rerr)
	}

	// The credential carries every bit, so what refuses a request is the
	// grant rather than the token's own scope. Testing both at once would
	// not say which one refused.
	tok, err := e.Auth.CreateAppPassword(ctx, id, "test",
		auth.Scope{Perms: uint16(everyPerm())}, 0)
	if err != nil {
		t.Fatalf("minting: %v", err)
	}

	return serve(t, e), tok, sh.Name
}

// everyPerm is the full mask.
func everyPerm() acl.Perms {
	return acl.Read | acl.Write | acl.Create | acl.Delete |
		acl.Rename | acl.Move | acl.Share | acl.Download
}

// post sends a JSON body with a credential.
func post(t *testing.T, url, token string, body any) (int, []byte) {
	t.Helper()

	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(string(encoded)))
	if err != nil {
		t.Fatalf("building: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth("ignored", token)

	resp, err := testClient().Do(req)
	if err != nil {
		t.Fatalf("requesting: %v", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	}()

	out := make([]byte, 0, 512)
	buf := make([]byte, 512)
	for {
		n, rerr := resp.Body.Read(buf)
		out = append(out, buf[:n]...)
		if rerr != nil {
			break
		}
	}
	return resp.StatusCode, out
}

// Each write route needs its own permission. A route resolving with the wrong
// one is served by an account that was never granted it, which is the whole
// reason the bits are separate.
func TestEachWriteRouteNeedsItsOwnPermission(t *testing.T) {
	cases := []struct {
		name    string
		route   string
		body    any
		granted acl.Perms
		missing acl.Perms
	}{
		{
			name:  "mkdir needs Create",
			route: "/api/v1/files/mkdir",
			body:  map[string]string{"path": "/work/newdir"},
			// Everything except the one bit this route needs. A grant with
			// Read alone would refuse for the wrong reason.
			granted: everyPerm() &^ acl.Create,
			missing: acl.Create,
		},
		{
			name:    "delete needs Delete",
			route:   "/api/v1/files/delete",
			body:    map[string]string{"path": "/work/existing.txt"},
			granted: everyPerm() &^ acl.Delete,
			missing: acl.Delete,
		},
		{
			name:    "rename needs Rename",
			route:   "/api/v1/files/rename",
			body:    map[string]string{"path": "/work/existing.txt", "name": "renamed.txt"},
			granted: everyPerm() &^ acl.Rename,
			missing: acl.Rename,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// Without the bit: refused.
			base, token, _ := shareWith(t, c.granted)
			status, body := post(t, base+c.route, token, c.body)
			if status < 400 {
				t.Errorf("%s succeeded without %v: %d %s", c.route, c.missing, status, body)
			}

			// With it: served. Otherwise the refusal above could be anything.
			base2, token2, _ := shareWith(t, everyPerm())
			ok, okBody := post(t, base2+c.route, token2, c.body)
			if ok >= 400 {
				t.Errorf("%s was refused with every permission: %d %s", c.route, ok, okBody)
			}
		})
	}
}

// A write actually writes. Without this the permission tests above could pass
// against a route that refuses everything.
func TestMkdirCreatesARealDirectory(t *testing.T) {
	base, token, share := shareWith(t, everyPerm())

	status, body := post(t, base+"/api/v1/files/mkdir", token,
		map[string]string{"path": "/" + share + "/created"})
	if status != http.StatusCreated {
		t.Fatalf("mkdir answered %d: %s", status, body)
	}

	// And the listing shows it, which is the client's own view of the write.
	listStatus, listBody := authed(t, http.MethodGet,
		base+"/api/v1/files/list?path="+urlEscape("/"+share), token)
	if listStatus != http.StatusOK {
		t.Fatalf("the listing answered %d: %s", listStatus, listBody)
	}
	if !strings.Contains(string(listBody), "created") {
		t.Errorf("the new directory is not in the listing: %s", listBody)
	}
}

// A delete removes the entry from the listing.
func TestDeleteRemovesTheEntry(t *testing.T) {
	base, token, share := shareWith(t, everyPerm())

	status, body := post(t, base+"/api/v1/files/delete", token,
		map[string]string{"path": "/" + share + "/existing.txt"})
	if status != http.StatusNoContent {
		t.Fatalf("delete answered %d: %s", status, body)
	}

	_, listBody := authed(t, http.MethodGet,
		base+"/api/v1/files/list?path="+urlEscape("/"+share), token)
	if strings.Contains(string(listBody), "existing.txt") {
		t.Errorf("the deleted entry is still listed: %s", listBody)
	}
}

// A write to a path outside every held share is refused, and refused the same
// way a missing one is.
func TestAWriteOutsideTheSharesIsRefused(t *testing.T) {
	base, token, _ := shareWith(t, everyPerm())

	escapes := []string{"/../etc/newdir", "/work/../../tmp/newdir", "/nothing/newdir"}

	for _, path := range escapes {
		t.Run(path, func(t *testing.T) {
			status, body := post(t, base+"/api/v1/files/mkdir", token,
				map[string]string{"path": path})
			if status < 400 {
				t.Errorf("%q was created: %d %s", path, status, body)
			}
		})
	}
}

// A write route needs a credential.
func TestTheWriteRoutesNeedACredential(t *testing.T) {
	base, _, _ := shareWith(t, everyPerm())

	for _, route := range []string{
		"/api/v1/files/mkdir",
		"/api/v1/files/delete",
		"/api/v1/files/rename",
	} {
		t.Run(route, func(t *testing.T) {
			status, body := post(t, base+route, "", map[string]string{"path": "/work/x"})
			if status != http.StatusUnauthorized {
				t.Errorf("%s answered %d anonymously: %s", route, status, body)
			}
		})
	}
}

// A body that is not JSON is refused as malformed rather than acted on with
// zero values. A mkdir with an empty path is a request nobody made.
func TestAMalformedBodyIsRefused(t *testing.T) {
	base, token, _ := shareWith(t, everyPerm())

	req, err := http.NewRequest(http.MethodPost, base+"/api/v1/files/mkdir",
		strings.NewReader("this is not json"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth("ignored", token)

	resp, err := testClient().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	}()

	if resp.StatusCode < 400 {
		t.Errorf("a body that is not JSON answered %d", resp.StatusCode)
	}
}

// A delete goes to the trash where the share has one. The route does not offer
// a permanent delete: a caller that could ask for one could bypass a
// deployment's own retention, which is what the trash is for.
func TestADeleteGoesToTheTrashWhereTheShareHasOne(t *testing.T) {
	ctx := context.Background()

	e, err := lifecycle.Open(ctx, lifecycle.Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	t.Cleanup(func() {
		if cerr := e.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	})

	id, err := e.Auth.CreateUser(ctx, "alice", "Alice", secret.New([]byte("a-long-enough-password")))
	if err != nil {
		t.Fatal(err)
	}

	host := t.TempDir()
	if werr := os.WriteFile(filepath.Join(host, "doomed.txt"), []byte("x"), 0o600); werr != nil {
		t.Fatal(werr)
	}

	sh, err := e.Core.CreateShare(ctx, core.ShareSpec{Name: "kept", Host: host})
	if err != nil {
		t.Fatal(err)
	}
	// Trash on, which is what makes the two delete modes differ at all.
	on := true
	if _, uerr := e.Core.UpdateShare(ctx, sh.ID, core.SharePatch{TrashEnabled: &on}); uerr != nil {
		t.Fatalf("enabling trash: %v", uerr)
	}
	if _, gerr := e.Core.CreateGrant(ctx, core.GrantSpec{
		User: &id, Share: sh.ID, Allow: everyPerm(), Inherit: true, Label: sh.Name,
	}); gerr != nil {
		t.Fatal(gerr)
	}
	if rerr := e.Core.ReloadGrants(ctx); rerr != nil {
		t.Fatal(rerr)
	}

	token, err := e.Auth.CreateAppPassword(ctx, id, "test",
		auth.Scope{Perms: uint16(everyPerm())}, 0)
	if err != nil {
		t.Fatal(err)
	}

	base := serve(t, e)

	status, body := post(t, base+"/api/v1/files/delete", token,
		map[string]string{"path": "/" + sh.Name + "/doomed.txt"})
	if status != http.StatusNoContent {
		t.Fatalf("delete answered %d: %s", status, body)
	}

	// The file is recoverable: a permanent delete would leave nothing to
	// restore, and a person who deleted by mistake would have no way back.
	resolved, err := e.Core.Resolve(core.UserID(id), vpathOf(t, "/"+sh.Name), acl.Read)
	if err != nil {
		t.Fatalf("resolving the share: %v", err)
	}
	entries, err := e.Core.TrashList(ctx, resolved)
	if err != nil {
		t.Fatalf("listing the trash: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("the trash holds %d entries, want the deleted file", len(entries))
	}
}

// vpathOf parses a virtual path for a test.
func vpathOf(t *testing.T, s string) vfs.Vpath {
	t.Helper()

	p, err := vfs.ParseVpath(s)
	if err != nil {
		t.Fatalf("parsing %q: %v", s, err)
	}
	return p
}

// A body that is not JSON never reaches a service. Acting on the zero value
// would make a mkdir with an empty path into a request nobody sent, and the
// refusal is what keeps a malformed request from becoming a well-formed one.
func TestAMalformedBodyNeverReachesTheService(t *testing.T) {
	base, token, share := shareWith(t, everyPerm())

	bodies := []string{"this is not json", "{", "[]", `{"path":`, ""}

	for _, raw := range bodies {
		t.Run(raw, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodPost, base+"/api/v1/files/mkdir",
				strings.NewReader(raw))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/json")
			req.SetBasicAuth("ignored", token)

			resp, err := testClient().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			if cerr := resp.Body.Close(); cerr != nil {
				t.Errorf("closing: %v", cerr)
			}
			if resp.StatusCode < 400 {
				t.Errorf("%q answered %d", raw, resp.StatusCode)
			}
		})
	}

	// And nothing was created by any of them.
	_, listBody := authed(t, http.MethodGet,
		base+"/api/v1/files/list?path="+urlEscape("/"+share), token)

	var page struct {
		Entries []struct {
			Name string `json:"name"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(listBody, &page); err != nil {
		t.Fatalf("the listing does not parse: %v", err)
	}
	if len(page.Entries) != 1 || page.Entries[0].Name != "existing.txt" {
		t.Errorf("a malformed body changed the share: %+v", page.Entries)
	}
}
