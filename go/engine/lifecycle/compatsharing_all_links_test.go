//go:build linux && compat_nc

package lifecycle_test

import (
	"context"
	"encoding/base64"
	"net/http"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/lifecycle"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/auth"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/state"
)

// The OCS share listing never carries a link share with an empty token.
//
// The reference client reads the token of every public-link entry without
// checking whether it is there, and the exception that produces is caught
// nowhere in its parser: it escapes as a failed operation, so the share screen
// reports that it could not fetch anything against an HTTP 200 and shows the
// user nothing at all. One unrenderable row therefore costs the whole listing,
// which is why a row that cannot carry its token is left out instead.
//
// This is also why the listing has no administrative variant. An overview
// crossing accounts must not hand an administrator every visitor's access, so
// it would have to blank the tokens, and blanked tokens are exactly the shape
// that takes the screen down. That overview lives on the web interface, where
// it needs no token to be useful.
func TestCompatShareListingOmitsALinkWithNoToken(t *testing.T) {
	t.Parallel()
	e, openErr := lifecycle.Open(context.Background(), lifecycle.Options{DataDir: t.TempDir()})
	if openErr != nil {
		t.Fatalf("opening engine: %v", openErr)
	}
	t.Cleanup(func() {
		if cerr := e.Close(); cerr != nil {
			t.Errorf("closing engine: %v", cerr)
		}
	})
	ctx := context.Background()

	dir := t.TempDir()
	if rerr := e.Core.RegisterShare(ctx, core.ShareDef{
		ID: 1, Name: "openshare", Host: dir, Policy: vfs.DefaultSharePolicy(),
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
	token, terr := e.Auth.CreateAppPassword(ctx, uid, "phone",
		auth.Scope{Perms: auth.SyncScopePerms}, 0)
	if terr != nil {
		t.Fatalf("minting the credential: %v", terr)
	}

	base := serveCompatEngine(t, e)
	client := compatClient()
	t.Cleanup(client.CloseIdleConnections)
	basic := "Basic " + base64.StdEncoding.EncodeToString([]byte("alice:"+token))

	for _, name := range []string{"good.txt", "legacy.txt"} {
		if code, body := davSend(t, client, basic, http.MethodPut,
			base+"/remote.php/dav/files/alice/openshare/"+name,
			strings.NewReader("x"), nil); code != http.StatusCreated {
			t.Fatalf("seeding %s: %d %s", name, code, body)
		}
	}

	// An ordinary link, whose token the cipher can recover.
	r, rerr := e.Core.Resolve(core.UserID(uid), vpathOf(t, "openshare/good.txt"), acl.Share)
	if rerr != nil {
		t.Fatalf("resolving: %v", rerr)
	}
	if _, _, cerr := e.Core.CreateLink(ctx, r, core.LinkSpec{
		Perms: acl.Read | acl.Download, MaxDown: -1,
	}); cerr != nil {
		t.Fatalf("minting the link: %v", cerr)
	}

	// A link with no recoverable token, written straight to the store the way
	// one minted before the token cipher was wired sits on disk: a hash that
	// still authenticates a visitor's request, and no ciphertext to open. The
	// projection reports Token nil for it, which is the row that renders as an
	// empty element and takes the client's parser down.
	if _, ierr := e.State.Insert(ctx, state.LinkRow{
		TokenHash: []byte("a-hash-that-authenticates-a-request"),
		Share:     1,
		Path:      "legacy.txt",
		Owner:     uid,
		Perms:     uint16(acl.Read | acl.Download),
		MaxDown:   nil,
		CreatedNs: 1,
	}); ierr != nil {
		t.Fatalf("writing the legacy link: %v", ierr)
	}

	// The request the app's share screen sends: no format parameter, so the
	// answer is the XML its own parser reads.
	code, body := davSend(t, client, basic, http.MethodGet,
		base+"/ocs/v2.php/apps/files_sharing/api/v1/shares?include_tags=true", nil,
		map[string]string{"OCS-APIRequest": "true"})
	if code != http.StatusOK {
		t.Fatalf("the listing answered %d: %s", code, body)
	}
	if strings.Contains(body, "<token/>") {
		t.Errorf("the listing carries an empty token element, which crashes the client: %s", body)
	}
	// Dropping the unrenderable row must not drop the renderable one with it:
	// the whole point is that one bad row no longer costs the listing.
	if !strings.Contains(body, "good.txt") {
		t.Errorf("the listing lost the link it can describe: %s", body)
	}
}
