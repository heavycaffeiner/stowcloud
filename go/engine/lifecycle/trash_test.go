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

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/secret"
	"github.com/heavycaffeiner/stowcloud/go/engine/lifecycle"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/auth"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
)

// trashShare serves an engine whose share has trash enabled and holds one file
// in a subdirectory, so a restore has somewhere specific to go back to.
func trashShare(t *testing.T, perms acl.Perms) (base, token, share string) {
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
		t.Fatal(err)
	}

	host := t.TempDir()
	if merr := os.Mkdir(filepath.Join(host, "sub"), 0o700); merr != nil {
		t.Fatal(merr)
	}
	if werr := os.WriteFile(filepath.Join(host, "sub", "doc.txt"), []byte("x"), 0o600); werr != nil {
		t.Fatal(werr)
	}

	sh, err := e.Core.CreateShare(ctx, core.ShareSpec{Name: "bin", Host: host})
	if err != nil {
		t.Fatal(err)
	}
	on := true
	if _, uerr := e.Core.UpdateShare(ctx, sh.ID, core.SharePatch{TrashEnabled: &on}); uerr != nil {
		t.Fatal(uerr)
	}
	if _, gerr := e.Core.CreateGrant(ctx, core.GrantSpec{
		User: &id, Share: sh.ID, Allow: perms, Inherit: true, Label: sh.Name,
	}); gerr != nil {
		t.Fatal(gerr)
	}
	if rerr := e.Core.ReloadGrants(ctx); rerr != nil {
		t.Fatal(rerr)
	}

	tok, err := e.Auth.CreateAppPassword(ctx, id, "test",
		auth.Scope{Perms: uint16(everyPerm())}, 0)
	if err != nil {
		t.Fatal(err)
	}

	return serve(t, e), tok, sh.Name
}

// trashOne deletes the fixture's file and returns the trash entry's id.
func trashOne(t *testing.T, base, token, share string) string {
	t.Helper()

	status, body := post(t, base+"/api/v1/files/delete", token,
		map[string]string{"path": "/" + share + "/sub/doc.txt"})
	if status != http.StatusNoContent {
		t.Fatalf("delete answered %d: %s", status, body)
	}

	listStatus, listBody := authed(t, http.MethodGet,
		base+"/api/v1/trash?path="+urlEscape("/"+share), token)
	if listStatus != http.StatusOK {
		t.Fatalf("the trash listing answered %d: %s", listStatus, listBody)
	}

	var entries []struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		OrigPath string `json:"orig_path"`
	}
	if err := json.Unmarshal(listBody, &entries); err != nil {
		t.Fatalf("the listing does not parse: %v\n%s", err, listBody)
	}
	if len(entries) != 1 {
		t.Fatalf("the trash holds %d entries, want the deleted file", len(entries))
	}
	if entries[0].Name != "doc.txt" {
		t.Errorf("the entry names %q", entries[0].Name)
	}
	return entries[0].ID
}

// A restore puts the entry back where it was deleted from, which the entry
// itself records. The request never says where: a caller choosing the
// destination could move a file anywhere it can write, using a delete as the
// first half of the move.
func TestARestoreReturnsTheEntryToItsOrigin(t *testing.T) {
	base, token, share := trashShare(t, everyPerm())

	id := trashOne(t, base, token, share)

	status, body := post(t, base+"/api/v1/trash/restore", token,
		map[string]string{"path": "/" + share, "id": id})
	if status != http.StatusNoContent {
		t.Fatalf("restore answered %d: %s", status, body)
	}

	// Back in the subdirectory it came from, not at the share root.
	_, subBody := authed(t, http.MethodGet,
		base+"/api/v1/files/list?path="+urlEscape("/"+share+"/sub"), token)
	if !strings.Contains(string(subBody), "doc.txt") {
		t.Errorf("the file did not return to its origin: %s", subBody)
	}

	_, rootBody := authed(t, http.MethodGet,
		base+"/api/v1/files/list?path="+urlEscape("/"+share), token)
	var page struct {
		Entries []struct {
			Name string `json:"name"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(rootBody, &page); err != nil {
		t.Fatal(err)
	}
	for _, entry := range page.Entries {
		if entry.Name == "doc.txt" {
			t.Errorf("the file was restored to the share root instead of its origin: %s", rootBody)
		}
	}
}

// The restore request carries no destination. A field a caller could set would
// be one they could point anywhere they can write.
func TestARestoreRequestNamesNoDestination(t *testing.T) {
	base, token, share := trashShare(t, everyPerm())

	id := trashOne(t, base, token, share)

	// A body carrying an extra destination is refused rather than honoured or
	// ignored: an unknown field means the client and the server disagree about
	// what this request is.
	req := map[string]string{
		"path": "/" + share,
		"id":   id,
		"to":   "/" + share + "/elsewhere.txt",
	}
	status, body := post(t, base+"/api/v1/trash/restore", token, req)
	if status < 400 {
		t.Errorf("a restore carrying a destination was accepted: %d %s", status, body)
	}
}

// Purging one entry removes it and leaves the rest.
func TestPurgingOneEntryLeavesTheRest(t *testing.T) {
	base, token, share := trashShare(t, everyPerm())

	// Two entries, so "removed one" is distinguishable from "emptied".
	first := trashOne(t, base, token, share)

	if status, body := post(t, base+"/api/v1/files/mkdir", token,
		map[string]string{"path": "/" + share + "/second"}); status != http.StatusCreated {
		t.Fatalf("mkdir answered %d: %s", status, body)
	}
	if status, body := post(t, base+"/api/v1/files/delete", token,
		map[string]string{"path": "/" + share + "/second"}); status != http.StatusNoContent {
		t.Fatalf("the second delete answered %d: %s", status, body)
	}

	status, body := post(t, base+"/api/v1/trash/purge", token,
		map[string]string{"path": "/" + share, "id": first})
	if status != http.StatusNoContent {
		t.Fatalf("purge answered %d: %s", status, body)
	}

	_, listBody := authed(t, http.MethodGet,
		base+"/api/v1/trash?path="+urlEscape("/"+share), token)

	var entries []map[string]any
	if err := json.Unmarshal(listBody, &entries); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("the trash holds %d entries after purging one of two", len(entries))
	}
}

// An empty trash lists as an empty array, not null.
func TestAnEmptyTrashListsAsAnArray(t *testing.T) {
	base, token, share := trashShare(t, everyPerm())

	status, body := authed(t, http.MethodGet,
		base+"/api/v1/trash?path="+urlEscape("/"+share), token)
	if status != http.StatusOK {
		t.Fatalf("answered %d: %s", status, body)
	}
	if strings.TrimSpace(string(body)) == "null" {
		t.Errorf("an empty trash encoded as null")
	}

	var entries []map[string]any
	if err := json.Unmarshal(body, &entries); err != nil {
		t.Fatalf("the listing does not parse: %v\n%s", err, body)
	}
	if entries == nil {
		t.Errorf("an empty trash decoded to nil: %s", body)
	}
}

// Restoring needs Create, because a restore adds a file to the tree. An
// account that may remove things is not thereby allowed to put them back.
func TestRestoringNeedsCreate(t *testing.T) {
	base, token, share := trashShare(t, everyPerm())
	id := trashOne(t, base, token, share)

	// The same fixture without Create.
	limited, limitedToken, limitedShare := trashShare(t, everyPerm()&^acl.Create)
	limitedID := trashOne(t, limited, limitedToken, limitedShare)

	status, body := post(t, limited+"/api/v1/trash/restore", limitedToken,
		map[string]string{"path": "/" + limitedShare, "id": limitedID})
	if status < 400 {
		t.Errorf("a restore succeeded without Create: %d %s", status, body)
	}

	// With Create: served, so the refusal above is about that bit.
	ok, okBody := post(t, base+"/api/v1/trash/restore", token,
		map[string]string{"path": "/" + share, "id": id})
	if ok != http.StatusNoContent {
		t.Errorf("a restore was refused with every permission: %d %s", ok, okBody)
	}
}

// The trash routes need a credential. Another account's deleted files are
// still their files.
func TestTheTrashRoutesNeedACredential(t *testing.T) {
	base, _, share := trashShare(t, everyPerm())

	status, body := get(t, base+"/api/v1/trash?path="+urlEscape("/"+share))
	if status != http.StatusUnauthorized {
		t.Errorf("the listing answered %d anonymously: %s", status, body)
	}

	for _, route := range []string{"/api/v1/trash/restore", "/api/v1/trash/purge"} {
		t.Run(route, func(t *testing.T) {
			s, b := post(t, base+route, "", map[string]string{"path": "/" + share, "id": "x"})
			if s != http.StatusUnauthorized {
				t.Errorf("%s answered %d anonymously: %s", route, s, b)
			}
		})
	}
}

// A trash operation on a share this account cannot reach is refused.
func TestTrashOutsideTheSharesIsRefused(t *testing.T) {
	base, token, _ := trashShare(t, everyPerm())

	for _, path := range []string{"/../etc", "/nothing", "/bin/../../tmp"} {
		t.Run(path, func(t *testing.T) {
			status, body := authed(t, http.MethodGet,
				base+"/api/v1/trash?path="+urlEscape(path), token)
			if status != http.StatusNotFound {
				t.Errorf("%q answered %d: %s", path, status, body)
			}
		})
	}
}

// Purging an id that matches nothing is refused, not reported as done. A
// client told the entry is gone stops showing it, and the next listing brings
// it back.
func TestPurgingAnAbsentEntryIsRefused(t *testing.T) {
	base, token, share := trashShare(t, everyPerm())
	trashOne(t, base, token, share)

	status, body := post(t, base+"/api/v1/trash/purge", token,
		map[string]string{"path": "/" + share, "id": "not-a-real-id"})
	if status == http.StatusNoContent || status == http.StatusOK {
		t.Fatalf("purging an absent entry reported success: %d %s", status, body)
	}
	if status != http.StatusNotFound {
		t.Errorf("answered %d, want a not-found: %s", status, body)
	}

	// And the real entry is untouched, so the refusal did not also empty it.
	_, listBody := authed(t, http.MethodGet,
		base+"/api/v1/trash?path="+urlEscape("/"+share), token)
	var entries []map[string]any
	if err := json.Unmarshal(listBody, &entries); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("the trash holds %d entries after a refused purge", len(entries))
	}
}

// Emptying an already-empty trash is success. The state asked for is the state
// that holds, which is not the same as a named entry that was never there.
func TestEmptyingAnEmptyTrashIsSuccess(t *testing.T) {
	base, token, share := trashShare(t, everyPerm())

	status, body := post(t, base+"/api/v1/trash/purge", token,
		map[string]string{"path": "/" + share})
	if status != http.StatusNoContent {
		t.Errorf("emptying an empty trash answered %d: %s", status, body)
	}
}
