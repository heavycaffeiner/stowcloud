//go:build linux

package lifecycle_test

import (
	"archive/zip"
	"bytes"
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

// A link's holder is not an account. Everything below is requested with no
// credential at all, which is the whole point of the surface: the token in the
// path is the authority, and a route that quietly wanted a session would work
// in these tests and fail for every real visitor.

// The file a link points at downloads, as an attachment, to a caller holding
// nothing but the address.
func TestAPublicLinkDownloadsWithoutACredential(t *testing.T) {
	base, token, _ := linkEngine(t, "note.txt", []byte("shared bytes"), acl.Read|acl.Download)

	status, header, body := anonymous(t, http.MethodGet, base+"/s/"+token+"/download", nil)
	if status != http.StatusOK {
		t.Fatalf("the download answered %d: %s", status, body)
	}
	if string(body) != "shared bytes" {
		t.Errorf("the body is %q", body)
	}
	if got := header.Get("Content-Disposition"); !strings.Contains(got, `filename="note.txt"`) {
		t.Errorf("the disposition is %q, which does not name the file", got)
	}
}

// The landing endpoint describes the link so the page can draw itself.
func TestAPublicLinkDescribesItself(t *testing.T) {
	base, token, _ := linkEngine(t, "note.txt", []byte("shared"), acl.Read|acl.Download)

	status, _, body := anonymous(t, http.MethodGet, base+"/s/"+token, nil)
	if status != http.StatusOK {
		t.Fatalf("the landing answered %d: %s", status, body)
	}

	var out struct {
		Protected   bool   `json:"protected"`
		Name        string `json:"name"`
		CanDownload bool   `json:"can_download"`
		Drop        bool   `json:"drop"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("the landing does not parse: %v\n%s", err, body)
	}
	if out.Protected {
		t.Error("a link with no password reports itself as protected")
	}
	if out.Name != "note.txt" || !out.CanDownload || out.Drop {
		t.Errorf("the landing describes the link as %+v", out)
	}
}

// A locked link reveals nothing at all until the password is answered.
//
// Not even the name: a link whose contents are readable without the password
// is one where the password only guards the bytes, and the name of a file is
// frequently the sensitive part.
func TestALockedLinkRevealsNothingUntilUnlocked(t *testing.T) {
	base, token, _ := linkEngineWithPassword(t, "salary-2026.pdf", []byte("%PDF"), "the-password")

	status, _, body := anonymous(t, http.MethodGet, base+"/s/"+token, nil)
	if status != http.StatusOK {
		t.Fatalf("the locked landing answered %d: %s", status, body)
	}
	if strings.Contains(string(body), "salary") {
		t.Errorf("a locked link leaked its name: %s", body)
	}

	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("the landing does not parse: %v", err)
	}
	if out["protected"] != true {
		t.Errorf("the locked landing is %v", out)
	}
	if len(out) != 1 {
		t.Errorf("a locked link answered %d fields, want only the locked flag: %v", len(out), out)
	}

	// And the bytes are behind it too, not just the description.
	dstatus, _, _ := anonymous(t, http.MethodGet, base+"/s/"+token+"/download", nil)
	if dstatus == http.StatusOK {
		t.Error("a locked link served its bytes without the password")
	}
}

// The password opens the link, and the wrong one does not.
func TestThePasswordUnlocksALinkAndAWrongOneDoesNot(t *testing.T) {
	base, token, _ := linkEngineWithPassword(t, "note.txt", []byte("guarded"), "the-password")

	wrong, _, _ := anonymous(t, http.MethodPost, base+"/s/"+token+"/auth",
		[]byte(`{"password":"not-it"}`))
	if wrong == http.StatusNoContent {
		t.Fatal("a wrong password unlocked the link")
	}

	status, header, body := anonymous(t, http.MethodPost, base+"/s/"+token+"/auth",
		[]byte(`{"password":"the-password"}`))
	if status != http.StatusNoContent {
		t.Fatalf("the right password answered %d: %s", status, body)
	}

	cookie := header.Get("Set-Cookie")
	if cookie == "" {
		t.Fatal("unlocking set no cookie, so the next request is locked again")
	}
	// Scoped to this link, so unlocking one does not unlock another and the
	// proof is not sent anywhere it is not needed.
	if !strings.Contains(cookie, "path=/s/"+token) && !strings.Contains(cookie, "Path=/s/"+token) {
		t.Errorf("the unlock cookie is not scoped to the link: %q", cookie)
	}
	if !strings.Contains(strings.ToLower(cookie), "httponly") {
		t.Errorf("the unlock cookie is readable to script: %q", cookie)
	}

	// Carrying it back reads the link.
	status, _, body = anonymousWithCookie(t, http.MethodGet, base+"/s/"+token, cookie)
	if status != http.StatusOK {
		t.Fatalf("the unlocked landing answered %d", status)
	}
	if !strings.Contains(string(body), "note.txt") {
		t.Errorf("the unlocked landing does not name the file: %s", body)
	}
}

// A folder link packs a zip a visitor can actually open.
func TestAPublicFolderLinkPacksAZip(t *testing.T) {
	base, token := linkEngineOverFolder(t, acl.Read|acl.Download)

	status, header, body := anonymous(t, http.MethodGet, base+"/s/"+token+"/zip", nil)
	if status != http.StatusOK {
		t.Fatalf("the zip answered %d: %s", status, body)
	}
	if got := header.Get("Content-Type"); got != "application/zip" {
		t.Errorf("the zip is typed %q", got)
	}

	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("the zip does not open: %v", err)
	}
	if len(zr.File) == 0 {
		t.Fatal("the zip is empty")
	}
	found := false
	for _, f := range zr.File {
		if strings.HasSuffix(f.Name, "inside.txt") {
			found = true
		}
	}
	if !found {
		t.Errorf("the zip does not hold the folder's file: %v", zr.File)
	}
}

// A drop link takes a file and shows nothing.
//
// Create without Read is the whole shape of one: whoever holds it can put
// something in and cannot see what is already there. A landing that listed the
// folder would reveal exactly what the link exists not to.
func TestADropLinkAcceptsAFileAndListsNothing(t *testing.T) {
	base, token, host := linkEngineOverFolderAt(t, acl.Create)

	status, _, body := anonymous(t, http.MethodGet, base+"/s/"+token, nil)
	if status != http.StatusOK {
		t.Fatalf("the drop landing answered %d: %s", status, body)
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("the landing does not parse: %v", err)
	}
	if _, listed := out["entries"]; listed {
		t.Errorf("a drop link listed the folder it drops into: %s", body)
	}
	if out["drop"] != true {
		t.Errorf("the landing does not report itself as a drop: %v", out)
	}

	up, _, ubody := anonymous(t, http.MethodPost,
		base+"/s/"+token+"/drop?name=dropped.txt", []byte("from a stranger"))
	if up != http.StatusCreated {
		t.Fatalf("the drop answered %d: %s", up, ubody)
	}

	// The file is really on disk, so the acceptance is not a status alone.
	got, rerr := os.ReadFile(filepath.Join(host, "dropped.txt"))
	if rerr != nil {
		t.Fatalf("the dropped file is not on disk: %v", rerr)
	}
	if string(got) != "from a stranger" {
		t.Errorf("the dropped file holds %q", got)
	}

	// And it cannot be read back through the same link.
	down, _, _ := anonymous(t, http.MethodGet, base+"/s/"+token+"/download", nil)
	if down == http.StatusOK {
		t.Error("a drop link served a download")
	}
}

// A token that names no link is not found, and says nothing else.
func TestAnUnknownTokenIsNotFound(t *testing.T) {
	base, _, _ := linkEngine(t, "note.txt", []byte("x"), acl.Read|acl.Download)

	status, _, _ := anonymous(t, http.MethodGet, base+"/s/nosuchtokenatall", nil)
	if status != http.StatusNotFound {
		t.Errorf("an unknown token answered %d, want 404", status)
	}
}

// anonymous performs a request carrying no credential whatsoever.
func anonymous(t *testing.T, method, url string, body []byte) (int, http.Header, []byte) {
	t.Helper()
	return anonymousWithCookie(t, method, url, "", body...)
}

// anonymousWithCookie is the same, carrying one cookie back.
func anonymousWithCookie(
	t *testing.T, method, url, cookie string, body ...byte,
) (int, http.Header, []byte) {
	t.Helper()

	var reader *bytes.Reader
	if len(body) > 0 {
		reader = bytes.NewReader(body)
	} else {
		reader = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatalf("building: %v", err)
	}
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	if cookie != "" {
		req.Header.Set("Cookie", strings.Split(cookie, ";")[0])
	}

	resp, err := testClient().Do(req)
	if err != nil {
		t.Fatalf("requesting %s: %v", url, err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	}()
	return resp.StatusCode, resp.Header, readAll(t, resp)
}

// linkEngine serves an engine holding one shared file, and mints a link over
// it with the given permissions.
func linkEngine(t *testing.T, name string, content []byte, perms acl.Perms) (base, token, host string) {
	t.Helper()
	return linkEngineAt(t, name, content, perms, nil)
}

// linkEngineWithPassword is linkEngine plus a password on the link.
func linkEngineWithPassword(t *testing.T, name string, content []byte, password string) (base, token, host string) {
	t.Helper()
	return linkEngineAt(t, name, content, acl.Read|acl.Download, &password)
}

func linkEngineAt(
	t *testing.T, name string, content []byte, perms acl.Perms, password *string,
) (base, token, host string) {
	t.Helper()
	ctx := context.Background()

	e, id, sh, dir := linkFixture(t)
	if werr := os.WriteFile(filepath.Join(dir, name), content, 0o600); werr != nil {
		t.Fatalf("writing: %v", werr)
	}

	r, err := e.Core.Resolve(core.UserID(id), vpathOf(t, sh.Name+"/"+name), acl.Share)
	if err != nil {
		t.Fatalf("resolving: %v", err)
	}
	_, tok, err := e.Core.CreateLink(ctx, r, core.LinkSpec{
		Perms: perms, Password: password, MaxDown: -1,
	})
	if err != nil {
		t.Fatalf("minting the link: %v", err)
	}
	if tok.Len() == 0 {
		t.Fatal("the link came back without a token")
	}
	// Reveal, not String: String masks the value, and a masked token in a URL
	// is a 404 that looks like the route is missing.
	return serve(t, e), string(tok.Reveal()), dir
}

// linkEngineOverFolder mints a link over the share's own folder.
func linkEngineOverFolder(t *testing.T, perms acl.Perms) (base, token string) {
	t.Helper()
	b, tok, _ := linkEngineOverFolderAt(t, perms)
	return b, tok
}

func linkEngineOverFolderAt(t *testing.T, perms acl.Perms) (base, token, host string) {
	t.Helper()
	ctx := context.Background()

	e, id, sh, dir := linkFixture(t)
	sub := filepath.Join(dir, "folder")
	if merr := os.Mkdir(sub, 0o700); merr != nil {
		t.Fatalf("making a folder: %v", merr)
	}
	if werr := os.WriteFile(filepath.Join(sub, "inside.txt"), []byte("packed"), 0o600); werr != nil {
		t.Fatalf("writing: %v", werr)
	}

	r, err := e.Core.Resolve(core.UserID(id), vpathOf(t, sh.Name+"/folder"), acl.Share)
	if err != nil {
		t.Fatalf("resolving: %v", err)
	}
	_, tok, err := e.Core.CreateLink(ctx, r, core.LinkSpec{Perms: perms, MaxDown: -1})
	if err != nil {
		t.Fatalf("minting the link: %v", err)
	}
	return serve(t, e), string(tok.Reveal()), sub
}

// linkFixture opens an engine with one account holding one share.
func linkFixture(t *testing.T) (*lifecycle.Engine, int64, core.Share, string) {
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
	sh, err := e.Core.CreateShare(ctx, core.ShareSpec{Name: "files", Host: host})
	if err != nil {
		t.Fatalf("creating the share: %v", err)
	}
	if _, gerr := e.Core.CreateGrant(ctx, core.GrantSpec{
		User: &id, Share: sh.ID,
		Allow:   acl.Read | acl.Write | acl.Create | acl.Download | acl.Share,
		Inherit: true, Label: sh.Name,
	}); gerr != nil {
		t.Fatalf("granting: %v", gerr)
	}
	if rerr := e.Core.ReloadGrants(ctx); rerr != nil {
		t.Fatalf("reloading grants: %v", rerr)
	}

	// Not used by these tests, but the engine mints one for every account and
	// an unused variable is worse than a named discard.
	if _, aerr := e.Auth.CreateAppPassword(ctx, id, "test",
		auth.Scope{Perms: uint16(acl.Read)}, 0); aerr != nil {
		t.Fatalf("minting: %v", aerr)
	}
	return e, id, sh, host
}
