//go:build linux

package lifecycle_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/secret"
	"github.com/heavycaffeiner/stowcloud/go/engine/lifecycle"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/auth"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
)

// contentShare serves an engine holding one share with a file of known bytes.
func contentShare(t *testing.T, perms acl.Perms, content []byte) (base, token, share string) {
	b, tok, sh, _ := contentShareAt(t, perms, content)
	return b, tok, sh
}

// contentShareAt is contentShare plus the host directory, for a test that has
// to act on the files themselves rather than through the API.
func contentShareAt(t *testing.T, perms acl.Perms, content []byte) (base, token, share, host string) {
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

	host = t.TempDir()
	if werr := os.WriteFile(filepath.Join(host, "doc.bin"), content, 0o600); werr != nil {
		t.Fatal(werr)
	}
	if merr := os.Mkdir(filepath.Join(host, "sub"), 0o700); merr != nil {
		t.Fatal(merr)
	}

	sh, err := e.Core.CreateShare(ctx, core.ShareSpec{Name: "files", Host: host})
	if err != nil {
		t.Fatal(err)
	}
	if _, gerr := e.Core.CreateGrant(ctx, core.GrantSpec{
		User: &id, Share: sh.ID, Allow: perms, Inherit: true, Label: sh.Name,
	}); gerr != nil {
		t.Fatal(gerr)
	}

	appPW, err := e.Auth.CreateAppPassword(ctx, id, "test",
		auth.Scope{Perms: auth.SyncScopePerms}, 0)
	if err != nil {
		t.Fatal(err)
	}
	return serve(t, e), appPW, sh.Name, host
}

// download performs a read, optionally with a Range header, and returns the
// whole response.
func download(t *testing.T, base, token, path, rangeHeader string) (int, http.Header, []byte) {
	t.Helper()

	req, err := http.NewRequest(http.MethodGet,
		base+"/api/v1/files/read?path="+urlEscape(path), nil)
	if err != nil {
		t.Fatalf("building: %v", err)
	}
	req.SetBasicAuth("ignored", token)
	if rangeHeader != "" {
		req.Header.Set("Range", rangeHeader)
	}

	resp, err := testClient().Do(req)
	if err != nil {
		t.Fatalf("requesting: %v", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	}()

	body := readAll(t, resp)
	return resp.StatusCode, resp.Header, body
}

// readAll drains a response body.
func readAll(t *testing.T, resp *http.Response) []byte {
	t.Helper()

	var out bytes.Buffer
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		if _, werr := out.Write(buf[:n]); werr != nil {
			t.Fatalf("buffering the body: %v", werr)
		}
		if err != nil {
			break
		}
	}
	return out.Bytes()
}

// payload is content long enough that a range covers a middle slice rather
// than the whole thing, and varied enough that an off-by-one is visible.
func payload() []byte {
	out := make([]byte, 4096)
	for i := range out {
		out[i] = byte(i % 251)
	}
	return out
}

// A read returns the file's bytes exactly.
func TestReadingAFile(t *testing.T) {
	want := payload()
	base, token, share := contentShare(t, everyPerm(), want)

	status, header, got := download(t, base, token, "/"+share+"/doc.bin", "")
	if status != http.StatusOK {
		t.Fatalf("answered %d: %s", status, got)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("read %d bytes, want %d, and they differ", len(got), len(want))
	}

	// The length has to match what was sent. A wrong Content-Length is how a
	// client ends up with a truncated file and no error.
	if header.Get("Content-Length") != strconv.Itoa(len(want)) {
		t.Errorf("Content-Length is %q for %d bytes", header.Get("Content-Length"), len(want))
	}
	if header.Get("Accept-Ranges") != "bytes" {
		t.Error("the response does not advertise range support")
	}
}

// A range returns exactly that slice, and says which slice it is.
func TestReadingARange(t *testing.T) {
	want := payload()
	base, token, share := contentShare(t, everyPerm(), want)

	const from, to = 100, 199
	status, header, got := download(t, base, token, "/"+share+"/doc.bin",
		fmt.Sprintf("bytes=%d-%d", from, to))
	if status != http.StatusPartialContent {
		t.Fatalf("a range answered %d, want 206: %s", status, got)
	}
	if !bytes.Equal(got, want[from:to+1]) {
		t.Errorf("the range returned %d bytes and they are not want[%d:%d]", len(got), from, to+1)
	}

	// The header names the slice and the whole size. A client assembling a
	// file from ranges uses both, and a wrong one corrupts the result with
	// nothing reporting a failure.
	wantRange := fmt.Sprintf("bytes %d-%d/%d", from, to, len(want))
	if header.Get("Content-Range") != wantRange {
		t.Errorf("Content-Range is %q, want %q", header.Get("Content-Range"), wantRange)
	}
	if header.Get("Content-Length") != strconv.Itoa(to-from+1) {
		t.Errorf("Content-Length is %q for a %d byte range",
			header.Get("Content-Length"), to-from+1)
	}
}

// An open-ended range runs to the end of the file.
func TestReadingAnOpenEndedRange(t *testing.T) {
	want := payload()
	base, token, share := contentShare(t, everyPerm(), want)

	const from = 4000
	status, _, got := download(t, base, token, "/"+share+"/doc.bin",
		fmt.Sprintf("bytes=%d-", from))
	if status != http.StatusPartialContent {
		t.Fatalf("answered %d", status)
	}
	if !bytes.Equal(got, want[from:]) {
		t.Errorf("got %d bytes, want the final %d", len(got), len(want)-from)
	}
}

// A suffix range returns the last N bytes.
func TestReadingASuffixRange(t *testing.T) {
	want := payload()
	base, token, share := contentShare(t, everyPerm(), want)

	const last = 64
	status, _, got := download(t, base, token, "/"+share+"/doc.bin",
		fmt.Sprintf("bytes=-%d", last))
	if status != http.StatusPartialContent {
		t.Fatalf("answered %d", status)
	}
	if !bytes.Equal(got, want[len(want)-last:]) {
		t.Errorf("got %d bytes, want the last %d", len(got), last)
	}
}

// A range past the end is refused with the real size, so a client can ask
// again correctly rather than guessing.
func TestARangePastTheEndIsRefused(t *testing.T) {
	want := payload()
	base, token, share := contentShare(t, everyPerm(), want)

	status, header, body := download(t, base, token, "/"+share+"/doc.bin", "bytes=99999-")
	if status != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("answered %d, want 416: %s", status, body)
	}
	if got := header.Get("Content-Range"); got != "bytes */"+strconv.Itoa(len(want)) {
		t.Errorf("Content-Range is %q, so the client is not told the real size", got)
	}
}

// A multi-range request is refused rather than served as its first range.
//
// A client that asked for three pieces and got one, with a 206 saying nothing
// went wrong, assembles a file out of what it received and finds the damage
// later.
func TestAMultiRangeRequestIsRefused(t *testing.T) {
	base, token, share := contentShare(t, everyPerm(), payload())

	status, _, body := download(t, base, token, "/"+share+"/doc.bin", "bytes=0-99,200-299")
	if status == http.StatusPartialContent || status == http.StatusOK {
		t.Fatalf("a multi-range request was served as %d with %d bytes", status, len(body))
	}
}

// Reading needs the Download bit, and a grant without it refuses.
func TestReadingNeedsTheDownloadPermission(t *testing.T) {
	base, token, share := contentShare(t, acl.Read, payload())

	status, _, body := download(t, base, token, "/"+share+"/doc.bin", "")
	if status == http.StatusOK {
		t.Fatalf("a grant without Download served %d bytes", len(body))
	}
}

// A write puts the bytes on disk and a read returns them.
func TestWritingAFile(t *testing.T) {
	base, token, share := contentShare(t, everyPerm(), []byte("original"))

	want := payload()
	status, body := upload(t, base, token, "/"+share+"/fresh.bin", want)
	if status != http.StatusOK {
		t.Fatalf("writing answered %d: %s", status, body)
	}

	code, _, got := download(t, base, token, "/"+share+"/fresh.bin", "")
	if code != http.StatusOK {
		t.Fatalf("reading back answered %d", code)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("read back %d bytes of %d written, and they differ", len(got), len(want))
	}
}

// upload sends a body to the write route.
func upload(t *testing.T, base, token, path string, content []byte) (int, []byte) {
	t.Helper()

	req, err := http.NewRequest(http.MethodPost,
		base+"/api/v1/files/write?path="+urlEscape(path), bytes.NewReader(content))
	if err != nil {
		t.Fatalf("building: %v", err)
	}
	req.SetBasicAuth("ignored", token)
	req.Header.Set("Content-Type", "application/octet-stream")

	resp, err := testClient().Do(req)
	if err != nil {
		t.Fatalf("requesting: %v", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	}()
	return resp.StatusCode, readAll(t, resp)
}

// A move relocates the entry: gone from one place, present at the other.
func TestMovingAFile(t *testing.T) {
	want := payload()
	base, token, share := contentShare(t, everyPerm(), want)

	status, body := post(t, base+"/api/v1/files/move", token, map[string]string{
		"from": "/" + share + "/doc.bin",
		"to":   "/" + share + "/sub/moved.bin",
	})
	if status != http.StatusOK {
		t.Fatalf("moving answered %d: %s", status, body)
	}

	// Both ends, because a move that copied without removing is a move that
	// silently doubled the data.
	if code, _, _ := download(t, base, token, "/"+share+"/doc.bin", ""); code == http.StatusOK {
		t.Error("the source still reads after a move")
	}
	code, _, got := download(t, base, token, "/"+share+"/sub/moved.bin", "")
	if code != http.StatusOK {
		t.Fatalf("the destination answered %d", code)
	}
	if !bytes.Equal(got, want) {
		t.Error("the moved file's contents changed")
	}
}

// A move onto a taken name is refused by default, and the destination keeps
// its own contents.
func TestAMoveOntoATakenNameIsRefused(t *testing.T) {
	base, token, share := contentShare(t, everyPerm(), []byte("source"))

	if status, body := upload(t, base, token, "/"+share+"/sub/taken.bin", []byte("destination")); status != http.StatusOK {
		t.Fatalf("preparing the destination answered %d: %s", status, body)
	}

	status, body := post(t, base+"/api/v1/files/move", token, map[string]string{
		"from": "/" + share + "/doc.bin",
		"to":   "/" + share + "/sub/taken.bin",
	})
	if status == http.StatusOK {
		t.Fatalf("a move onto a taken name succeeded: %s", body)
	}

	// The destination is untouched, which is the thing that matters: a
	// refusal that had already overwritten would have destroyed data.
	_, _, got := download(t, base, token, "/"+share+"/sub/taken.bin", "")
	if string(got) != "destination" {
		t.Errorf("the destination now holds %q", got)
	}
	// And the source is still there.
	if code, _, _ := download(t, base, token, "/"+share+"/doc.bin", ""); code != http.StatusOK {
		t.Error("the refused move removed the source")
	}
}

// An unknown conflict policy is refused rather than quietly treated as the
// default. The two differ by whether a file survives.
func TestAnUnknownConflictPolicyIsRefused(t *testing.T) {
	base, token, share := contentShare(t, everyPerm(), []byte("source"))

	status, body := post(t, base+"/api/v1/files/move", token, map[string]string{
		"from":        "/" + share + "/doc.bin",
		"to":          "/" + share + "/sub/moved.bin",
		"on_conflict": "obliterate",
	})
	if status == http.StatusOK {
		t.Fatalf("an unknown policy was accepted: %s", body)
	}

	if code, _, _ := download(t, base, token, "/"+share+"/doc.bin", ""); code != http.StatusOK {
		t.Error("the refused move relocated the file anyway")
	}
}

// A copy is accepted as a job and leaves both files in place.
func TestCopyingAFile(t *testing.T) {
	want := payload()
	base, token, share := contentShare(t, everyPerm(), want)

	status, body := post(t, base+"/api/v1/files/copy", token, map[string]string{
		"from": "/" + share + "/doc.bin",
		"to":   "/" + share + "/sub/copy.bin",
	})
	if status != http.StatusAccepted {
		t.Fatalf("copying answered %d: %s", status, body)
	}

	var view map[string]any
	if err := json.Unmarshal(body, &view); err != nil {
		t.Fatal(err)
	}
	if stringField(view, "id") == "" {
		t.Fatal("the copy reports no job id, so a client cannot poll it")
	}

	// The source is untouched. A copy that removed it would be a move.
	if code, _, _ := download(t, base, token, "/"+share+"/doc.bin", ""); code != http.StatusOK {
		t.Error("the source is gone after a copy")
	}
}

// The rollup reports what is beneath a directory.
func TestTheRecursiveSize(t *testing.T) {
	content := payload()
	base, token, share := contentShare(t, everyPerm(), content)

	status, body := authed(t, http.MethodGet,
		base+"/api/v1/files/size?path="+urlEscape("/"+share), token)
	if status != http.StatusOK {
		t.Fatalf("answered %d: %s", status, body)
	}

	var view map[string]any
	if err := json.Unmarshal(body, &view); err != nil {
		t.Fatal(err)
	}

	// Decimal strings, because a tree past 2^53 bytes loses exactness as a
	// JavaScript number and a quota decision would be made on the wrong
	// figure.
	size := stringField(view, "size")
	if size == "" {
		t.Fatalf("no size in %v", view)
	}
	n, err := strconv.ParseUint(size, 10, 64)
	if err != nil {
		t.Fatalf("the size %q is not a decimal string: %v", size, err)
	}
	if n < uint64(len(content)) {
		t.Errorf("the rollup reports %d bytes for a tree holding at least %d", n, len(content))
	}
	if stringField(view, "etag") == "" {
		t.Error("no etag, so a client has to walk the tree to detect a change")
	}
}

// The recent listing reports a write that just happened.
func TestTheRecentListing(t *testing.T) {
	base, token, share := contentShare(t, everyPerm(), []byte("x"))

	if status, body := upload(t, base, token, "/"+share+"/fresh.bin", payload()); status != http.StatusOK {
		t.Fatalf("writing answered %d: %s", status, body)
	}

	status, body := authed(t, http.MethodGet, base+"/api/v1/files/recent", token)
	if status != http.StatusOK {
		t.Fatalf("answered %d: %s", status, body)
	}

	var hits []map[string]any
	if err := json.Unmarshal(body, &hits); err != nil {
		t.Fatalf("decoding %s: %v", body, err)
	}
	if len(hits) == 0 {
		t.Fatal("the listing is empty right after a write")
	}

	var found bool
	for _, hit := range hits {
		if stringField(hit, "name") == "fresh.bin" {
			found = true
			if stringField(hit, "op") == "" {
				t.Error("the entry does not say what happened to it")
			}
		}
	}
	if !found {
		t.Errorf("the write is not in the listing: %v", hits)
	}
}

// An empty recent listing encodes as an array, never null. A client iterating
// a null gets a runtime error rather than zero rows.
func TestAnEmptyRecentListingIsAnArray(t *testing.T) {
	base, token := bootWithUser(t)

	status, body := authed(t, http.MethodGet, base+"/api/v1/files/recent", token)
	if status != http.StatusOK {
		t.Fatalf("answered %d: %s", status, body)
	}
	if string(bytes.TrimSpace(body)) != "[]" {
		t.Errorf("an empty listing encoded as %s", body)
	}
}

// A copy out of a share the account may not move from still works.
//
// Copying removes nothing from the source, so demanding Move there would
// refuse the ordinary case: taking a copy of something you may read but not
// modify. The destination is a separate share the account can write.
func TestCopyingFromAShareWithoutMoveRights(t *testing.T) {
	ctx := context.Background()

	e, err := lifecycle.Open(ctx, lifecycle.Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
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

	want := payload()
	readOnlyHost := t.TempDir()
	if werr := os.WriteFile(filepath.Join(readOnlyHost, "doc.bin"), want, 0o600); werr != nil {
		t.Fatal(werr)
	}
	writableHost := t.TempDir()

	source, err := e.Core.CreateShare(ctx, core.ShareSpec{Name: "reference", Host: readOnlyHost})
	if err != nil {
		t.Fatal(err)
	}
	dest, err := e.Core.CreateShare(ctx, core.ShareSpec{Name: "mine", Host: writableHost})
	if err != nil {
		t.Fatal(err)
	}

	// Read and Download only: everything a copy's source needs and nothing
	// that would let the account change it.
	if _, gerr := e.Core.CreateGrant(ctx, core.GrantSpec{
		User: &id, Share: source.ID, Allow: acl.Read | acl.Download,
		Inherit: true, Label: source.Name,
	}); gerr != nil {
		t.Fatal(gerr)
	}
	if _, gerr := e.Core.CreateGrant(ctx, core.GrantSpec{
		User: &id, Share: dest.ID, Allow: everyPerm(), Inherit: true, Label: dest.Name,
	}); gerr != nil {
		t.Fatal(gerr)
	}

	token, err := e.Auth.CreateAppPassword(ctx, id, "test",
		auth.Scope{Perms: auth.SyncScopePerms}, 0)
	if err != nil {
		t.Fatal(err)
	}
	base := serve(t, e)

	status, body := post(t, base+"/api/v1/files/copy", token, map[string]string{
		"from": "/reference/doc.bin",
		"to":   "/mine/copy.bin",
	})
	if status != http.StatusAccepted {
		t.Fatalf("copying from a read-only share answered %d: %s", status, body)
	}

	// A move from the same source has to be refused, which is what proves
	// the two requirements are actually different rather than both weak.
	moveStatus, moveBody := post(t, base+"/api/v1/files/move", token, map[string]string{
		"from": "/reference/doc.bin",
		"to":   "/mine/moved.bin",
	})
	if moveStatus == http.StatusOK {
		t.Errorf("a move out of a share without Move succeeded: %s", moveBody)
	}
}

// The recent listing is bounded whatever a client asks for.
//
// An unbounded limit is a journal scan whose cost grows with how long the
// account has been used, and it is a query a caller controls entirely.
func TestTheRecentLimitIsBounded(t *testing.T) {
	base, token, share := contentShare(t, everyPerm(), []byte("x"))

	// More writes than the ceiling, so an unbounded limit would return more
	// rows than the ceiling permits.
	const writes = 12
	for i := 0; i < writes; i++ {
		if status, body := upload(t, base, token,
			fmt.Sprintf("/%s/f%d.bin", share, i), []byte("y")); status != http.StatusOK {
			t.Fatalf("write %d answered %d: %s", i, status, body)
		}
	}

	for _, limit := range []string{"1", "5", "999999", "-1", "0", "abc"} {
		status, body := authed(t, http.MethodGet,
			base+"/api/v1/files/recent?limit="+limit, token)
		if status != http.StatusOK {
			t.Fatalf("limit=%s answered %d: %s", limit, status, body)
		}

		var hits []map[string]any
		if err := json.Unmarshal(body, &hits); err != nil {
			t.Fatalf("limit=%s: decoding %s: %v", limit, body, err)
		}

		// The ceiling is checked against recentLimit directly, since proving
		// it here would need more rows than the ceiling. What this covers is
		// that the parameter reaches the query rather than being ignored.
		if n, err := strconv.Atoi(limit); err == nil && n > 0 && len(hits) > n {
			t.Errorf("limit=%s returned %d rows", limit, len(hits))
		}
	}
}
