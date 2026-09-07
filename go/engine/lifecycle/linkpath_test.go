//go:build linux

package lifecycle_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/secret"
	"github.com/heavycaffeiner/stowcloud/go/engine/lifecycle"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
)

// A link listing reports the path its owner's client can actually open.
//
// The stored path is share-relative and carries the grant's subpath. An
// account granted one folder inside a share sees that folder as its own root,
// so the stored path begins with a component the client never addresses:
// listing it verbatim gave "Game/file" for an account whose root already is
// Game, and every navigation from the screen landed on a path that does not
// exist. The projection the rest of the product uses is the one answer.
func TestALinkListingReportsANavigablePath(t *testing.T) {
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

	id, err := e.Auth.CreateUser(ctx, loginName, "Alice", secret.New([]byte(loginPassword)))
	if err != nil {
		t.Fatalf("creating the account: %v", err)
	}

	// A share the account reaches only through a subfolder, which is the shape
	// that produced the bug: the grant projects "Game" as the account's root
	// while the stored path still begins with it.
	host := t.TempDir()
	if merr := os.MkdirAll(filepath.Join(host, "Game"), 0o700); merr != nil {
		t.Fatalf("seeding the share: %v", merr)
	}
	if werr := os.WriteFile(filepath.Join(host, "Game", "save.dat"), []byte("x"), 0o600); werr != nil {
		t.Fatalf("writing: %v", werr)
	}
	sh, err := e.Core.CreateShare(ctx, core.ShareSpec{Name: "vault", Host: host})
	if err != nil {
		t.Fatalf("creating the share: %v", err)
	}
	if _, gerr := e.Core.CreateGrant(ctx, core.GrantSpec{
		User: &id, Share: sh.ID, Subpath: "Game",
		Allow:   acl.Read | acl.Write | acl.Create | acl.Download | acl.Share,
		Inherit: true, Label: "Saves",
	}); gerr != nil {
		t.Fatalf("granting: %v", gerr)
	}
	if rerr := e.Core.ReloadGrants(ctx); rerr != nil {
		t.Fatalf("reloading grants: %v", rerr)
	}
	r, rerr := e.Core.Resolve(core.UserID(id), vpathOf(t, "Saves/save.dat"), acl.Share)
	if rerr != nil {
		t.Fatalf("resolving: %v", rerr)
	}
	if _, _, cerr := e.Core.CreateLink(ctx, r, core.LinkSpec{
		Perms: acl.Read | acl.Download, MaxDown: -1,
	}); cerr != nil {
		t.Fatalf("minting: %v", cerr)
	}

	base := serve(t, e)
	cookie, _ := signedIn(t, base)
	status, body := withCookie(t, http.MethodGet, base+"/api/v1/links", cookie)
	if status != http.StatusOK {
		t.Fatalf("the listing answered %d: %s", status, body)
	}

	var rows []struct {
		Path string `json:"path"`
	}
	if uerr := json.Unmarshal(body, &rows); uerr != nil {
		t.Fatalf("decoding: %v", uerr)
	}
	if len(rows) != 1 {
		t.Fatalf("the listing holds %d rows: %s", len(rows), body)
	}

	// What the account's own client addresses, which is what a navigation from
	// this screen has to use. The stored form is "Game/save.dat": the grant
	// subpath on the front, a component this account never addresses, because
	// its grant projects that folder as the root called "Saves".
	const want = "Saves/save.dat"
	if rows[0].Path != want {
		t.Errorf("the listing reports %q, which the account cannot open; want %q",
			rows[0].Path, want)
	}
	if _, verr := e.Core.Resolve(core.UserID(id), vpathOf(t, rows[0].Path), acl.Read); verr != nil {
		t.Errorf("the reported path %q does not resolve: %v", rows[0].Path, verr)
	}
}
