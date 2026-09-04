//go:build linux

package lifecycle_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/secret"
	"github.com/heavycaffeiner/stowcloud/go/engine/lifecycle"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
)

// A path that escapes is refused, and refused the same way a missing one is.
//
// The parser is what decides this, and the point of the test is that the
// handler asks it rather than doing its own thing: every one of these reaches
// the route as a query parameter a caller controls entirely.
func TestNoPathEscapesTheVirtualRoot(t *testing.T) {
	base, _, sess := bootWithUser(t)

	escapes := []string{
		"/../etc/passwd",
		"/share/../../etc",
		"../etc",
		"/share/../..",
		"//etc",
		"/share/./../../root",
		"/\x00etc",
		"/share/sub/../../../..",
	}

	for _, path := range escapes {
		t.Run(path, func(t *testing.T) {
			status, body := authed(t, http.MethodGet,
				base+"/api/v1/files/list?path="+urlEscape(path), sess)

			// Not found rather than a parse error: telling a caller their path
			// was malformed distinguishes it from one that simply is not
			// there, and both are things this account cannot see.
			if status != http.StatusNotFound {
				t.Errorf("%q answered %d: %s", path, status, body)
			}
		})
	}
}

// An unparseable path and an absent one answer identically, byte for byte.
// A difference between them is a way to probe what exists.
func TestAMalformedPathIsIndistinguishableFromAnAbsentOne(t *testing.T) {
	base, _, sess := bootWithUser(t)

	malformed, malformedBody := authed(t, http.MethodGet,
		base+"/api/v1/files/list?path="+urlEscape("/../escape"), sess)
	absent, absentBody := authed(t, http.MethodGet,
		base+"/api/v1/files/list?path="+urlEscape("/nothing/here"), sess)

	if malformed != absent {
		t.Errorf("malformed answered %d and absent answered %d", malformed, absent)
	}
	if string(malformedBody) != string(absentBody) {
		t.Errorf("the two answers differ:\n%s\n%s", malformedBody, absentBody)
	}
}

// The virtual root lists what the account can reach, which for a new account
// with no grants is nothing. An empty listing is an empty array, not null: a
// client iterating a null gets a runtime error rather than zero rows.
func TestTheVirtualRootListsThisAccountsShares(t *testing.T) {
	base, _, sess := bootWithUser(t)

	for _, path := range []string{"", "/"} {
		t.Run("path="+path, func(t *testing.T) {
			status, body := authed(t, http.MethodGet,
				base+"/api/v1/files/list?path="+urlEscape(path), sess)
			if status != http.StatusOK {
				t.Fatalf("the root answered %d: %s", status, body)
			}

			var page struct {
				Entries []map[string]any `json:"entries"`
			}
			if err := json.Unmarshal(body, &page); err != nil {
				t.Fatalf("the page does not parse: %v\n%s", err, body)
			}
			if page.Entries == nil {
				t.Errorf("an empty root encoded its entries as null: %s", body)
			}
			if len(page.Entries) != 0 {
				t.Errorf("an account with no grants sees %d shares", len(page.Entries))
			}
		})
	}
}

// A share this account was never granted is not listed and not reachable. The
// listing and the resolve have to agree: one showing what the other refuses is
// a client that can see a name it cannot open.
func TestAnUngrantedShareIsNeitherListedNorReachable(t *testing.T) {
	base, _, sess := bootWithUser(t)

	status, body := authed(t, http.MethodGet,
		base+"/api/v1/files/list?path="+urlEscape("/somebody-elses-share"), sess)
	if status != http.StatusNotFound {
		t.Errorf("an ungranted share answered %d: %s", status, body)
	}

	stat, statBody := authed(t, http.MethodGet,
		base+"/api/v1/files/stat?path="+urlEscape("/somebody-elses-share"), sess)
	if stat != http.StatusNotFound {
		t.Errorf("stat on an ungranted share answered %d: %s", stat, statBody)
	}
}

// Both read routes need a credential. A file listing served anonymously is
// every file on the deployment served anonymously.
//
// The refusal is 404 rather than 401: middleware.scopeHandler answers every
// scope refusal as a path that is not there, so a stranger probing the
// surface cannot map which routes exist from the status code alone.
func TestTheFileRoutesNeedACredential(t *testing.T) {
	base, _, _ := bootWithUser(t)

	for _, path := range []string{"/api/v1/files/list", "/api/v1/files/stat"} {
		t.Run(path, func(t *testing.T) {
			status, body := get(t, path0(base, path))
			if status != http.StatusNotFound {
				t.Errorf("%s answered %d anonymously: %s", path, status, body)
			}
		})
	}
}

// path0 builds a request URL with a root path parameter.
func path0(base, route string) string { return base + route + "?path=%2F" }

// urlEscape percent-encodes a query value.
//
// Written out rather than using the standard escaper because a query value is
// being built, and the standard one leaves characters that mean something in a
// query string.
func urlEscape(s string) string {
	const hexDigits = "0123456789ABCDEF"

	out := make([]byte, 0, len(s)*3)
	for i := 0; i < len(s); i++ {
		c := s[i]
		unreserved := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' || c == '~'
		if unreserved {
			out = append(out, c)
			continue
		}
		out = append(out, '%', hexDigits[c>>4], hexDigits[c&0x0F])
	}
	return string(out)
}

// engineWithShare serves an engine holding one account with one granted share
// containing one file, and returns the base URL, a session and the share
// name.
func engineWithShare(t *testing.T) (base string, sess session, share string) {
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

	if _, cerr := e.Auth.CreateUser(ctx, "alice", "Alice", secret.New([]byte("a-long-enough-password"))); cerr != nil {
		t.Fatalf("creating the account: %v", cerr)
	}

	// A real directory with a real file, so the listing has something in it
	// and an empty answer means a defect rather than an empty deployment.
	host := t.TempDir()
	if werr := os.WriteFile(filepath.Join(host, "hello.txt"), []byte("contents"), 0o600); werr != nil {
		t.Fatalf("writing a file: %v", werr)
	}

	sh, err := e.Core.CreateShare(ctx, core.ShareSpec{Name: "docs", Host: host})
	if err != nil {
		t.Fatalf("creating the share: %v", err)
	}
	// The label is the name this share appears under in the virtual root. A
	// grant with no label falls back to a synthetic one, which is a usable
	// path but not the one a person would recognise.
	id, ierr := e.Auth.UserIDByName(ctx, "alice")
	if ierr != nil {
		t.Fatalf("reading the account id: %v", ierr)
	}
	if _, gerr := e.Core.CreateGrant(ctx, core.GrantSpec{
		User: &id, Share: sh.ID, Allow: acl.Read | acl.Download,
		Inherit: true, Label: sh.Name,
	}); gerr != nil {
		t.Fatalf("granting: %v", gerr)
	}
	if rerr := e.Core.ReloadGrants(ctx); rerr != nil {
		t.Fatalf("reloading grants: %v", rerr)
	}

	base = serve(t, e)
	return base, signIn(t, base, "alice", "a-long-enough-password"), sh.Name
}

// A granted share lists its real contents. Without this every refusal above
func TestAGrantedShareListsItsContents(t *testing.T) {
	base, sess, share := engineWithShare(t)

	status, body := authed(t, http.MethodGet,
		base+"/api/v1/files/list?path="+urlEscape("/"+share), sess)
	if status != http.StatusOK {
		t.Fatalf("a granted share answered %d: %s", status, body)
	}

	var page struct {
		Entries []struct {
			Name string `json:"name"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(body, &page); err != nil {
		t.Fatalf("the page does not parse: %v\n%s", err, body)
	}
	if len(page.Entries) != 1 || page.Entries[0].Name != "hello.txt" {
		t.Errorf("the listing is %+v, want the one file", page.Entries)
	}
}

// Every path a listing hands out has to work as the next request's argument.
//
// The projection had been sending the share-relative path, which names no
// share: a listing of Files/Docs answered `Docs/readme.txt` for a file only
// reachable as `Files/Docs/readme.txt`, so the row's own path was a 404. The
// web client addresses download, stat and preview by exactly this field, which
// is why a file in a browsed folder could not be downloaded while the same
// file behind a public link could: the link carries its own token and never
// looks at this path.
//
// Asserted by feeding the answer back in rather than by comparing to a string,
// because the property is that the path resolves, not that it is spelled a
// particular way.
func TestEveryListedPathResolvesOnTheNextRequest(t *testing.T) {
	base, sess, share := engineWithShare(t)

	status, body := authed(t, http.MethodGet,
		base+"/api/v1/files/list?path="+urlEscape("/"+share), sess)
	if status != http.StatusOK {
		t.Fatalf("listing answered %d: %s", status, body)
	}

	var page struct {
		Entries []struct {
			Name    string `json:"name"`
			Path    string `json:"path"`
			Content string `json:"content"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(body, &page); err != nil {
		t.Fatalf("the page does not parse: %v\n%s", err, body)
	}
	if len(page.Entries) == 0 {
		t.Fatal("the listing is empty, so nothing was checked")
	}

	for _, entry := range page.Entries {
		// The row's own reference, which is what a client uses: the listing
		// seals one into every file it names, and nothing composes a content
		// URL out of the path beside it.
		if entry.Content == "" {
			t.Errorf("the listing gave %q no content reference", entry.Name)
			continue
		}
		st, rb := authed(t, http.MethodGet,
			base+"/api/v1/files/read?claim="+urlEscape(entry.Content), sess)
		if st != http.StatusOK {
			t.Errorf("reading %q, the reference the listing gave for %q, answered %d: %s",
				entry.Path, entry.Name, st, rb)
		}
	}
}

// Stat's own reference round-trips too. It is a separate projection call site,
// and the one the details panel and the preview dialog address a file by.
func TestStatsReferenceResolvesOnTheNextRequest(t *testing.T) {
	base, sess, share := engineWithShare(t)

	status, body := authed(t, http.MethodGet,
		base+"/api/v1/files/stat?path="+urlEscape("/"+share+"/hello.txt"), sess)
	if status != http.StatusOK {
		t.Fatalf("stat answered %d: %s", status, body)
	}

	var entry struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(body, &entry); err != nil {
		t.Fatalf("stat does not parse: %v\n%s", err, body)
	}
	if entry.Content == "" {
		t.Fatalf("stat carries no content reference: %s", body)
	}

	st, rb := authed(t, http.MethodGet,
		base+"/api/v1/files/read?claim="+urlEscape(entry.Content), sess)
	if st != http.StatusOK {
		t.Errorf("reading the reference stat gave for %q answered %d: %s", entry.Path, st, rb)
	}
}

// The listing honours the order and the window the caller asks for.
//
// The grid fetches the rows it is about to draw and sorts by clicking a
// column header, so a route that ignored these would answer the first page in
// name order no matter what was asked, and scrolling would redraw the same
// rows. Core has supported all three since it was written; the route did not
// pass them.
func TestAListingSortsAndWindowsAsAsked(t *testing.T) {
	base, sess, share := engineWithManyFiles(t)

	names := func(query string) []string {
		t.Helper()
		status, body := authed(t, http.MethodGet,
			base+"/api/v1/files/list?path="+urlEscape("/"+share)+"&"+query, sess)
		if status != http.StatusOK {
			t.Fatalf("%s answered %d: %s", query, status, body)
		}
		var page struct {
			Entries []struct {
				Name string `json:"name"`
			} `json:"entries"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			t.Fatalf("parsing %s: %v", query, err)
		}
		out := make([]string, 0, len(page.Entries))
		for _, e := range page.Entries {
			out = append(out, e.Name)
		}
		return out
	}

	ascending := names("sort=name&limit=3")
	if len(ascending) != 3 {
		t.Fatalf("a limit of 3 returned %d rows: %v", len(ascending), ascending)
	}
	if ascending[0] != "file-00.txt" {
		t.Errorf("ascending starts at %q", ascending[0])
	}

	descending := names("sort=name&order=desc&limit=3")
	if len(descending) != 3 {
		t.Fatalf("descending returned %d rows: %v", len(descending), descending)
	}
	if descending[0] == ascending[0] {
		t.Errorf("descending starts at %q, the same row ascending starts at", descending[0])
	}
	if descending[0] != "file-09.txt" {
		t.Errorf("descending starts at %q, want the last name", descending[0])
	}
}

// A cursor walks the whole directory without repeating or skipping a row.
func TestACursorWalksEveryEntryExactlyOnce(t *testing.T) {
	base, sess, share := engineWithManyFiles(t)

	seen := map[string]int{}
	cursor := ""
	for page := 0; page < 20; page++ {
		query := "path=" + urlEscape("/"+share) + "&limit=3"
		if cursor != "" {
			query += "&cursor=" + urlEscape(cursor)
		}
		status, body := authed(t, http.MethodGet, base+"/api/v1/files/list?"+query, sess)
		if status != http.StatusOK {
			t.Fatalf("page %d answered %d: %s", page, status, body)
		}
		var got struct {
			Entries []struct {
				Name string `json:"name"`
			} `json:"entries"`
			Next string `json:"cursor"`
		}
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("parsing page %d: %v", page, err)
		}
		for _, e := range got.Entries {
			seen[e.Name]++
		}
		if got.Next == "" {
			break
		}
		cursor = got.Next
	}

	if len(seen) != 10 {
		t.Errorf("the walk saw %d distinct names, want 10", len(seen))
	}
	for name, times := range seen {
		if times != 1 {
			t.Errorf("%q appeared %d times", name, times)
		}
	}
}

// engineWithManyFiles serves a share holding ten files, enough that a window
// is a slice of the directory rather than all of it.
func engineWithManyFiles(t *testing.T) (base string, sess session, share string) {
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
	for i := range 10 {
		name := filepath.Join(host, "file-0"+strconv.Itoa(i)+".txt")
		if werr := os.WriteFile(name, []byte("x"), 0o600); werr != nil {
			t.Fatalf("writing: %v", werr)
		}
	}

	sh, err := e.Core.CreateShare(ctx, core.ShareSpec{Name: "docs", Host: host})
	if err != nil {
		t.Fatalf("creating the share: %v", err)
	}
	if _, gerr := e.Core.CreateGrant(ctx, core.GrantSpec{
		User: &id, Share: sh.ID, Allow: acl.Read | acl.Download,
		Inherit: true, Label: sh.Name,
	}); gerr != nil {
		t.Fatalf("granting: %v", gerr)
	}
	if rerr := e.Core.ReloadGrants(ctx); rerr != nil {
		t.Fatalf("reloading grants: %v", rerr)
	}

	base = serve(t, e)
	return base, signIn(t, base, "alice", "a-long-enough-password"), sh.Name
}

// The virtual root shows the granted share, so the listing and the resolve
// agree about what this account can reach.
func TestTheRootShowsAGrantedShare(t *testing.T) {
	base, sess, share := engineWithShare(t)

	status, body := authed(t, http.MethodGet, base+"/api/v1/files/list?path=%2F", sess)
	if status != http.StatusOK {
		t.Fatalf("the root answered %d: %s", status, body)
	}
	if !strings.Contains(string(body), share) {
		t.Errorf("the root does not show the granted share: %s", body)
	}
}

// Stat reads the real file, so the read path is exercised rather than only its
// refusals.
func TestStatReadsARealFile(t *testing.T) {
	base, sess, share := engineWithShare(t)

	status, body := authed(t, http.MethodGet,
		base+"/api/v1/files/stat?path="+urlEscape("/"+share+"/hello.txt"), sess)
	if status != http.StatusOK {
		t.Fatalf("stat answered %d: %s", status, body)
	}

	var entry struct {
		Name  string `json:"name"`
		IsDir bool   `json:"is_dir"`
		Size  string `json:"size"`
	}
	if err := json.Unmarshal(body, &entry); err != nil {
		t.Fatalf("the entry does not parse: %v\n%s", err, body)
	}
	if entry.Name != "hello.txt" || entry.IsDir {
		t.Errorf("stat returned %+v", entry)
	}
	if entry.Size != "8" {
		t.Errorf("the size is %q, want 8", entry.Size)
	}
}

// A traversal out of a share the account really holds is still refused. This
// is the case the earlier corpus could not reach, because that account had no
// share to escape from.
func TestATraversalOutOfAHeldShareIsRefused(t *testing.T) {
	base, sess, share := engineWithShare(t)

	escapes := []string{
		"/" + share + "/../..",
		"/" + share + "/../../etc",
		"/" + share + "/./../../root",
		"/" + share + "/hello.txt/../../..",
	}

	for _, path := range escapes {
		t.Run(path, func(t *testing.T) {
			status, body := authed(t, http.MethodGet,
				base+"/api/v1/files/list?path="+urlEscape(path), sess)
			if status != http.StatusNotFound {
				t.Errorf("%q answered %d: %s", path, status, body)
			}
		})
	}
}

// A share the account holds without Read is not readable. The permission the
// route needs is what makes this refuse: a resolve asking for nothing would
// hand back a location the grant never allowed reading.
func TestAShareGrantedWithoutReadIsNotReadable(t *testing.T) {
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

	if _, cerr := e.Auth.CreateUser(ctx, "alice", "Alice", secret.New([]byte("a-long-enough-password"))); cerr != nil {
		t.Fatal(cerr)
	}
	id, ierr := e.Auth.UserIDByName(ctx, "alice")
	if ierr != nil {
		t.Fatal(ierr)
	}

	host := t.TempDir()
	if werr := os.WriteFile(filepath.Join(host, "secret.txt"), []byte("x"), 0o600); werr != nil {
		t.Fatal(werr)
	}

	sh, err := e.Core.CreateShare(ctx, core.ShareSpec{Name: "dropbox", Host: host})
	if err != nil {
		t.Fatal(err)
	}

	// A drop box: the account may add files and may not read what is there.
	// The grant is real, so the share exists for this account; only the bit
	// the route needs is missing.
	if _, gerr := e.Core.CreateGrant(ctx, core.GrantSpec{
		User: &id, Share: sh.ID, Allow: acl.Read | acl.Create, Deny: acl.Read,
		Inherit: true, Label: sh.Name,
	}); gerr != nil {
		t.Fatal(gerr)
	}
	if rerr := e.Core.ReloadGrants(ctx); rerr != nil {
		t.Fatal(rerr)
	}

	base := serve(t, e)
	sess := signIn(t, base, "alice", "a-long-enough-password")

	status, body := authed(t, http.MethodGet,
		base+"/api/v1/files/list?path="+urlEscape("/"+sh.Name), sess)
	if status == http.StatusOK {
		t.Errorf("a share denied Read was listed: %s", body)
	}
}
