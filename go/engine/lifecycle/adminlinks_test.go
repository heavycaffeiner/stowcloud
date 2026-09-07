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

// The administrative overview of every published link.
//
// The owner's own listing answers "what have I published". This one answers
// "what is published from this server", which is the question an operator asked
// to take a leaked link down actually has: without it they cannot find the link
// without already knowing whose it is.

// ownedLink is one row of the overview.
type ownedLink struct {
	ID        string `json:"id"`
	Path      string `json:"path"`
	Owner     string `json:"owner"`
	OwnerName string `json:"owner_name"`
	Token     string `json:"token"`
}

// linksOverview boots an engine where an administrator and an ordinary account
// have each published a link over the same share.
func linksOverview(t *testing.T) string {
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

	admin, err := e.Auth.CreateAdmin(ctx, "alice", "Alice", secret.New([]byte(loginPassword)))
	if err != nil {
		t.Fatalf("creating the administrator: %v", err)
	}
	bob, err := e.Auth.CreateUser(ctx, "bob", "Bob", secret.New([]byte(loginPassword)))
	if err != nil {
		t.Fatalf("creating the ordinary account: %v", err)
	}

	dir := t.TempDir()
	sh, err := e.Core.CreateShare(ctx, core.ShareSpec{Name: "files", Host: dir})
	if err != nil {
		t.Fatalf("creating the share: %v", err)
	}
	for _, who := range []int64{admin, bob} {
		id := who
		if _, gerr := e.Core.CreateGrant(ctx, core.GrantSpec{
			User: &id, Share: sh.ID,
			Allow:   acl.Read | acl.Write | acl.Create | acl.Download | acl.Share,
			Inherit: true, Label: sh.Name,
		}); gerr != nil {
			t.Fatalf("granting: %v", gerr)
		}
	}
	if rerr := e.Core.ReloadGrants(ctx); rerr != nil {
		t.Fatalf("reloading grants: %v", rerr)
	}

	for who, name := range map[int64]string{admin: "alices.txt", bob: "bobs.txt"} {
		if werr := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); werr != nil {
			t.Fatalf("writing %s: %v", name, werr)
		}
		r, rerr := e.Core.Resolve(core.UserID(who), vpathOf(t, sh.Name+"/"+name), acl.Share)
		if rerr != nil {
			t.Fatalf("resolving %s: %v", name, rerr)
		}
		if _, _, cerr := e.Core.CreateLink(ctx, r, core.LinkSpec{
			Perms: acl.Read | acl.Download, MaxDown: -1,
		}); cerr != nil {
			t.Fatalf("minting over %s: %v", name, cerr)
		}
	}
	return serve(t, e)
}

// signInAs returns a session cookie for one named account.
func signInAs(t *testing.T, base, login string) *http.Cookie {
	t.Helper()
	resp := postJSON(t, base+"/api/v1/auth/login",
		map[string]string{"login": login, "password": loginPassword})
	cookie := resp.sessionCookie()
	if cookie == nil {
		t.Fatalf("signing in as %s produced no cookie: %d %v", login, resp.status, resp.body)
	}
	return cookie
}

// An administrator sees every account's links, with the owner named.
func TestTheLinkOverviewCrossesAccounts(t *testing.T) {
	base := linksOverview(t)

	status, body := withCookie(t, http.MethodGet, base+"/api/v1/admin/links",
		signInAs(t, base, "alice"))
	if status != http.StatusOK {
		t.Fatalf("the overview answered %d: %s", status, body)
	}

	var rows []ownedLink
	if err := json.Unmarshal(body, &rows); err != nil {
		t.Fatalf("decoding the overview: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("the overview lists %d links, want both accounts': %s", len(rows), body)
	}

	owners := map[string]bool{}
	for _, row := range rows {
		owners[row.OwnerName] = true
		// A listing crossing every account is the last place a live credential
		// belongs: whoever reads this screen would otherwise hold every
		// visitor's access to every published file.
		if row.Token != "" {
			t.Errorf("the overview carries a live token for %s", row.Path)
		}
		if row.Owner == "" {
			t.Errorf("a row names no owner: %s", row.Path)
		}
	}
	for _, want := range []string{"Alice", "Bob"} {
		if !owners[want] {
			t.Errorf("the overview omits %s's link: %s", want, body)
		}
	}
}

// An ordinary account cannot read the overview.
//
// It lists what every other account has published, including paths inside
// shares the caller was never granted, so the administrative gate is the whole
// of what keeps one account's publishing private from another's.
func TestTheLinkOverviewRefusesANonAdministrator(t *testing.T) {
	base := linksOverview(t)

	status, body := withCookie(t, http.MethodGet, base+"/api/v1/admin/links",
		signInAs(t, base, "bob"))
	if status == http.StatusOK {
		t.Fatalf("an ordinary account read the overview: %s", body)
	}
	if status != http.StatusForbidden {
		t.Errorf("the refusal answered %d, want 403: %s", status, body)
	}
}
