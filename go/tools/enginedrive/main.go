//go:build linux

// A real boot of the rebuilt engine, for driving its HTTP surface by hand.
//
// The shipped command still serves the old tree, so this is how the engine's
// routes are exercised as a running server rather than only from tests.
package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/secret"
	"github.com/heavycaffeiner/stowcloud/go/engine/lifecycle"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/auth"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
)

func main() {
	if len(os.Args) != 3 {
		log.Fatalf("usage: %s <data-dir> <share-dir>", os.Args[0])
	}
	ctx := context.Background()
	dataDir, shareDir := os.Args[1], os.Args[2]

	e, err := lifecycle.Open(ctx, lifecycle.Options{DataDir: dataDir})
	if err != nil {
		log.Fatalf("opening: %v", err)
	}

	if _, cerr := e.Auth.CreateAdmin(ctx, "root", "Root",
		secret.New([]byte("a-long-enough-password"))); cerr != nil {
		log.Fatalf("creating the administrator: %v", cerr)
	}
	user, err := e.Auth.CreateUser(ctx, "alice", "Alice",
		secret.New([]byte("a-long-enough-password")))
	if err != nil {
		log.Fatalf("creating the account: %v", err)
	}

	sh, err := e.Core.CreateShare(ctx, core.ShareSpec{Name: "docs", Host: shareDir})
	if err != nil {
		log.Fatalf("creating the share: %v", err)
	}
	on := true
	if _, uerr := e.Core.UpdateShare(ctx, sh.ID, core.SharePatch{TrashEnabled: &on}); uerr != nil {
		log.Fatalf("enabling the trash: %v", uerr)
	}
	every := acl.Read | acl.Write | acl.Create | acl.Delete |
		acl.Rename | acl.Move | acl.Share | acl.Download
	if _, gerr := e.Core.CreateGrant(ctx, core.GrantSpec{
		User: &user, Share: sh.ID, Allow: every, Inherit: true, Label: sh.Name,
	}); gerr != nil {
		log.Fatalf("granting: %v", gerr)
	}
	token, err := e.Auth.CreateAppPassword(ctx, user, "drive",
		auth.Scope{Perms: auth.SyncScopePerms}, 0)
	if err != nil {
		log.Fatalf("minting an app password: %v", err)
	}

	app, err := e.Mount()
	if err != nil {
		log.Fatalf("mounting: %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatalf("listening: %v", err)
	}

	// The caller reads these to reach the server, so a write that failed
	// leaves them driving nothing.
	if _, perr := fmt.Printf("BASE http://%s\nAPPPW %s\n", ln.Addr(), token); perr != nil {
		log.Fatalf("reporting the address: %v", perr)
	}
	if serr := app.Listener(ln); serr != nil {
		log.Fatalf("serving: %v", serr)
	}
}
