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
	"strings"
	"testing"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/clock"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/secret"
	"github.com/heavycaffeiner/stowcloud/go/engine/lifecycle"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
)

// contentShare serves an engine holding one share with a file of known bytes.
func contentShare(t *testing.T, perms acl.Perms, content []byte) (base string, sess session, share string) {
	b, sess, sh, _ := contentShareAt(t, perms, content)
	return b, sess, sh
}

// contentShareAt is contentShare plus the host directory, for a test that has
// to act on the files themselves rather than through the API.
func contentShareAt(t *testing.T, perms acl.Perms, content []byte) (base string, sess session, share, host string) {
	b, sess, sh, h, _, _ := contentShareGrant(t, perms, content)
	return b, sess, sh, h
}

// contentShareGrant is contentShareAt plus the engine and the grant's id, for
// a test that has to change what the account may reach while the server runs.
func contentShareGrant(t *testing.T, perms acl.Perms, content []byte) (
	base string, sess session, share, host string, e *lifecycle.Engine, grant int64,
) {
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
	g, gerr := e.Core.CreateGrant(ctx, core.GrantSpec{
		User: &id, Share: sh.ID, Allow: perms, Inherit: true, Label: sh.Name,
	})
	if gerr != nil {
		t.Fatal(gerr)
	}

	served := serve(t, e)
	return served, signIn(t, served, "alice", "a-long-enough-password"), sh.Name, host, e, g.ID
}

// download performs a read, optionally with a Range header, and returns the
// whole response.
func download(t *testing.T, base string, sess session, path, rangeHeader string) (int, http.Header, []byte) {
	t.Helper()

	req, err := http.NewRequest(http.MethodGet,
		base+"/api/v1/files/read?claim="+urlEscape(contentRef(t, base, sess, path)), nil)
	if err != nil {
		t.Fatalf("building: %v", err)
	}
	sess.attach(req)
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

// A download is a two-step ticket, and the fetch carries the attachment
// header naming the file.
//
// The path goes in the POST body and comes back as an opaque token, so no
// client composes a download URL out of a path. One that did composed it
// wrong: an account granted a folder inside a share saw its own label twice.
func TestADownloadTicketFetchesTheFileAsAnAttachment(t *testing.T) {
	base, sess, share := contentShare(t, acl.Read|acl.Download, []byte("payload"))

	status, body := post(t, base+"/api/v1/files/download", sess,
		map[string]string{"path": "/" + share + "/doc.bin"})
	if status != http.StatusOK {
		t.Fatalf("minting a download answered %d: %s", status, body)
	}
	var ticket struct {
		Token string `json:"token"`
		Name  string `json:"name"`
		URL   string `json:"url"`
	}
	if err := json.Unmarshal(body, &ticket); err != nil {
		t.Fatalf("the ticket does not parse: %v\n%s", err, body)
	}
	switch {
	case ticket.Token == "":
		t.Fatal("the ticket carries no token")
	case ticket.Name != "doc.bin":
		t.Errorf("the ticket names %q, want doc.bin", ticket.Name)
	case !strings.Contains(ticket.URL, ticket.Token):
		t.Errorf("the ticket's URL %q does not carry its token", ticket.URL)
	case strings.Contains(ticket.URL, "doc.bin") || strings.Contains(ticket.URL, "path="):
		t.Errorf("the ticket's URL %q carries the file's path", ticket.URL)
	}

	code, header, got := fetchTicket(t, base, sess, ticket.URL)
	if code != http.StatusOK {
		t.Fatalf("fetching the ticket answered %d", code)
	}
	disposition := header.Get("Content-Disposition")
	if disposition == "" {
		t.Fatal("the fetch carries no Content-Disposition, so the browser renders it instead of saving it")
	}
	if !strings.Contains(disposition, `filename="doc.bin"`) {
		t.Errorf("the disposition is %q, which does not name the file", disposition)
	}
	if string(got) != "payload" {
		t.Errorf("the fetch returned %q", got)
	}
}

// fetchTicket follows a ticket's own URL, which is what a browser navigating
// to it does.
func fetchTicket(t *testing.T, base string, sess session, ticketURL string) (int, http.Header, []byte) {
	t.Helper()

	req, err := http.NewRequest(http.MethodGet, base+ticketURL, nil)
	if err != nil {
		t.Fatalf("building the fetch: %v", err)
	}
	sess.attach(req)

	resp, err := testClient().Do(req)
	if err != nil {
		t.Fatalf("fetching: %v", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	}()
	return resp.StatusCode, resp.Header, readAll(t, resp)
}

// A ticket is a capability, so it belongs to the account that minted it and
// answers a stranger the same way a token that never existed does.
func TestADownloadTicketDoesNotCrossAccounts(t *testing.T) {
	base, sess, share, _, e, _ := contentShareGrant(t, acl.Read|acl.Download, []byte("private"))

	status, body := post(t, base+"/api/v1/files/download", sess,
		map[string]string{"path": "/" + share + "/doc.bin"})
	if status != http.StatusOK {
		t.Fatalf("minting answered %d: %s", status, body)
	}
	var ticket struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(body, &ticket); err != nil {
		t.Fatalf("the ticket does not parse: %v", err)
	}

	// A second account, signed in, with no grant over the share at all.
	ctx := context.Background()
	if _, cerr := e.Auth.CreateUser(ctx, "bob", "Bob", secret.New([]byte("another-long-password"))); cerr != nil {
		t.Fatalf("creating the second account: %v", cerr)
	}
	stranger := signIn(t, base, "bob", "another-long-password")

	if code, _, _ := fetchTicket(t, base, stranger, ticket.URL); code != http.StatusNotFound {
		t.Errorf("another account fetched the ticket: %d", code)
	}
	// And a token nobody minted answers identically, so a guess learns
	// nothing from the difference.
	absent := "/api/v1/files/download/fetch?token=" + urlEscape("never-minted")
	if code, _, _ := fetchTicket(t, base, sess, absent); code != http.StatusNotFound {
		t.Errorf("a token that was never minted answered %d", code)
	}
}

// The fetch resolves again rather than trusting the mint. A grant revoked
// between the two requests has to refuse the download, not serve it on a
// check that passed a moment ago.
func TestADownloadTicketIsRefusedAfterTheGrantGoes(t *testing.T) {
	base, sess, share, _, e, grant := contentShareGrant(t, acl.Read|acl.Download, []byte("private"))

	status, body := post(t, base+"/api/v1/files/download", sess,
		map[string]string{"path": "/" + share + "/doc.bin"})
	if status != http.StatusOK {
		t.Fatalf("minting answered %d: %s", status, body)
	}
	var ticket struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(body, &ticket); err != nil {
		t.Fatalf("the ticket does not parse: %v", err)
	}

	if derr := e.Core.DeleteGrant(context.Background(), grant); derr != nil {
		t.Fatalf("revoking the grant: %v", derr)
	}

	if code, _, _ := fetchTicket(t, base, sess, ticket.URL); code != http.StatusNotFound {
		t.Errorf("the ticket still served the file after the grant went: %d", code)
	}
}

// A folder is the archive pair's business. Minting a file ticket for one would
// hand back a token that streams nothing.
func TestADownloadTicketRefusesAFolder(t *testing.T) {
	base, sess, share := contentShare(t, acl.Read|acl.Download, []byte("payload"))

	status, body := post(t, base+"/api/v1/files/download", sess,
		map[string]string{"path": "/" + share})
	if status != http.StatusUnprocessableEntity {
		t.Errorf("minting a ticket for a folder answered %d: %s", status, body)
	}
}

// A path the account cannot reach answers as a missing one, which is the same
// answer every other surface gives for a path outside every grant.
func TestADownloadTicketRefusesAnUnreachablePath(t *testing.T) {
	base, sess, _ := contentShare(t, acl.Read|acl.Download, []byte("payload"))

	status, _ := post(t, base+"/api/v1/files/download", sess,
		map[string]string{"path": "/nowhere/doc.bin"})
	if status != http.StatusNotFound {
		t.Errorf("minting a ticket for an unreachable path answered %d", status)
	}
}

// A content reference narrows a session; it never says who the session is.
//
// The compatibility layer's direct URL is opened with no session at all, so
// the claim there is the whole credential. These are not that: the route
// refuses a reference whose account is not the account already signed in.
// Without that comparison any signed-in account could replay a reference it
// found in a history or a screenshot and read another account's file.
func TestAContentReferenceDoesNotCrossAccounts(t *testing.T) {
	base, sess, share, _, e, _ := contentShareGrant(t, acl.Read|acl.Download, []byte("private"))
	ref := contentRef(t, base, sess, "/"+share+"/doc.bin")

	ctx := context.Background()
	if _, cerr := e.Auth.CreateUser(ctx, "bob", "Bob", secret.New([]byte("another-long-password"))); cerr != nil {
		t.Fatalf("creating the second account: %v", cerr)
	}
	stranger := signIn(t, base, "bob", "another-long-password")

	status, body := authed(t, http.MethodGet,
		base+"/api/v1/files/read?claim="+urlEscape(ref), stranger)
	if status != http.StatusNotFound {
		t.Errorf("another account read the reference: %d %s", status, body)
	}

	// And the owner's own reference still works, so the refusal above is the
	// account comparison rather than a broken reference.
	if code, _, _ := download(t, base, sess, "/"+share+"/doc.bin", ""); code != http.StatusOK {
		t.Errorf("the owner's own reference answered %d", code)
	}
}

// A value the server did not seal is refused, and so is one whose bytes were
// changed in transit. Every refusal answers as a missing file, because telling
// them apart tells a caller which part of a forged reference to fix.
func TestAForgedContentReferenceIsRefused(t *testing.T) {
	base, sess, share := contentShare(t, acl.Read|acl.Download, []byte("private"))
	ref := contentRef(t, base, sess, "/"+share+"/doc.bin")

	tampered := []rune(ref)
	last := len(tampered) - 1
	if tampered[last] == 'A' {
		tampered[last] = 'B'
	} else {
		tampered[last] = 'A'
	}

	for name, value := range map[string]string{
		"empty":      "",
		"nonsense":   "not-a-claim",
		"no version": "YWJjZGVmZ2hpamtsbW5vcHFyc3R1dnd4eXo",
		"tampered":   string(tampered),
	} {
		t.Run(name, func(t *testing.T) {
			status, _ := authed(t, http.MethodGet,
				base+"/api/v1/files/read?claim="+urlEscape(value), sess)
			if status != http.StatusNotFound {
				t.Errorf("the reference answered %d, want the missing-file answer", status)
			}
		})
	}
}

// Without the parameter there is no attachment header: the same endpoint feeds
// the text editor and the preview, which render rather than save.
func TestAPlainReadIsNotAnAttachment(t *testing.T) {
	base, sess, share := contentShare(t, acl.Read|acl.Download, []byte("hello"))

	status, header, body := readWithQuery(t, base, sess, "/"+share+"/doc.bin", "")
	if status != http.StatusOK {
		t.Fatalf("the read answered %d", status)
	}
	if got := header.Get("Content-Disposition"); got != "" {
		t.Errorf("a plain read carries %q, so the editor would download instead of opening", got)
	}
	if string(body) != "hello" {
		t.Errorf("the body is %q", body)
	}
}

// readWithQuery reads a path with extra query parameters appended.
func readWithQuery(t *testing.T, base string, sess session, path, extra string) (int, http.Header, []byte) {
	t.Helper()

	url := base + "/api/v1/files/read?claim=" + urlEscape(contentRef(t, base, sess, path))
	if extra != "" {
		url += "&" + extra
	}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("building: %v", err)
	}
	sess.attach(req)

	resp, err := testClient().Do(req)
	if err != nil {
		t.Fatalf("requesting: %v", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	}()

	return resp.StatusCode, resp.Header, readAll(t, resp)
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
	base, sess, share := contentShare(t, everyPerm(), want)

	status, header, got := download(t, base, sess, "/"+share+"/doc.bin", "")
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
	base, sess, share := contentShare(t, everyPerm(), want)

	const from, to = 100, 199
	status, header, got := download(t, base, sess, "/"+share+"/doc.bin",
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
	base, sess, share := contentShare(t, everyPerm(), want)

	const from = 4000
	status, _, got := download(t, base, sess, "/"+share+"/doc.bin",
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
	base, sess, share := contentShare(t, everyPerm(), want)

	const last = 64
	status, _, got := download(t, base, sess, "/"+share+"/doc.bin",
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
	base, sess, share := contentShare(t, everyPerm(), want)

	status, header, body := download(t, base, sess, "/"+share+"/doc.bin", "bytes=99999-")
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
	base, sess, share := contentShare(t, everyPerm(), payload())

	status, _, body := download(t, base, sess, "/"+share+"/doc.bin", "bytes=0-99,200-299")
	if status == http.StatusPartialContent || status == http.StatusOK {
		t.Fatalf("a multi-range request was served as %d with %d bytes", status, len(body))
	}
}

// Reading needs the Download bit, and a grant without it refuses.
func TestReadingNeedsTheDownloadPermission(t *testing.T) {
	base, sess, share := contentShare(t, acl.Read, payload())

	status, _, body := download(t, base, sess, "/"+share+"/doc.bin", "")
	if status == http.StatusOK {
		t.Fatalf("a grant without Download served %d bytes", len(body))
	}
}

// A write puts the bytes on disk and a read returns them.
func TestWritingAFile(t *testing.T) {
	base, sess, share := contentShare(t, everyPerm(), []byte("original"))

	want := payload()
	status, body := upload(t, base, sess, "/"+share+"/fresh.bin", want)
	if status != http.StatusOK {
		t.Fatalf("writing answered %d: %s", status, body)
	}

	code, _, got := download(t, base, sess, "/"+share+"/fresh.bin", "")
	if code != http.StatusOK {
		t.Fatalf("reading back answered %d", code)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("read back %d bytes of %d written, and they differ", len(got), len(want))
	}
}

// upload sends a body to the write route.
func upload(t *testing.T, base string, sess session, path string, content []byte) (int, []byte) {
	t.Helper()

	req, err := http.NewRequest(http.MethodPost,
		base+"/api/v1/files/write?path="+urlEscape(path), bytes.NewReader(content))
	if err != nil {
		t.Fatalf("building: %v", err)
	}
	sess.attach(req)
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

// A conditional write is refused once the file has moved underneath it.
//
// This is the whole defence against two people editing one file: the second
// save carries the token the first reader saw, and the server refuses rather
// than writing over an edit nobody has seen. Without it the last save silently
// wins and the earlier one is gone with no trace that it existed.
func TestAConditionalWriteIsRefusedAfterTheFileChanges(t *testing.T) {
	base, sess, share, host := contentShareAt(t, everyPerm(), []byte("original"))
	path := "/" + share + "/doc.bin"

	// The token this editor is holding.
	status, body := authed(t, http.MethodGet,
		base+"/api/v1/files/stat?path="+urlEscape(path), sess)
	if status != http.StatusOK {
		t.Fatalf("stat answered %d: %s", status, body)
	}
	var before struct {
		ETag string `json:"etag"`
	}
	if err := json.Unmarshal(body, &before); err != nil {
		t.Fatalf("stat does not parse: %v", err)
	}
	if before.ETag == "" {
		t.Fatal("stat carries no token, so nothing can be conditioned on it")
	}

	// Somebody else saves first.
	if code, rb := upload(t, base, sess, path, []byte("somebody else's edit")); code != http.StatusOK {
		t.Fatalf("the intervening write answered %d: %s", code, rb)
	}

	// The first editor's save, still carrying the old token.
	code, rb := uploadIfMatch(t, base, sess, path, []byte("my edit"), before.ETag)
	if code == http.StatusOK {
		t.Fatal("a stale conditional write succeeded, erasing the edit it never saw")
	}

	// And the file still holds what the other editor wrote, so the refusal
	// protected the bytes rather than only answering a status.
	got, gerr := os.ReadFile(filepath.Join(host, "doc.bin"))
	if gerr != nil {
		t.Fatalf("reading the file back: %v", gerr)
	}
	if string(got) != "somebody else's edit" {
		t.Errorf("the file holds %q after a refused write: %s", got, rb)
	}
}

// A conditional write is refused even when the token is the current one, and
// the refusal names what the file is now.
//
// This looks wrong and is deliberate. Every file token here is weak, because
// the filesystem exposes no change version to derive a strong one from, and a
// weak token cannot satisfy If-Match. Rather than pretend a comparison it
// cannot make, the server refuses and hands back the current token so a
// conflict screen can show it. Dropping the condition is the only way past,
// which puts the decision with the person doing the overwrite.
func TestEveryConditionalWriteIsRefusedAndReportsTheCurrentToken(t *testing.T) {
	base, sess, share := contentShare(t, everyPerm(), []byte("original"))
	path := "/" + share + "/doc.bin"

	status, body := authed(t, http.MethodGet,
		base+"/api/v1/files/stat?path="+urlEscape(path), sess)
	if status != http.StatusOK {
		t.Fatalf("stat answered %d", status)
	}
	var cur struct {
		ETag string `json:"etag"`
	}
	if err := json.Unmarshal(body, &cur); err != nil {
		t.Fatalf("stat does not parse: %v", err)
	}

	code, rb := uploadIfMatch(t, base, sess, path, []byte("my edit"), cur.ETag)
	if code != http.StatusPreconditionFailed {
		t.Fatalf("a conditional write answered %d, want 412: %s", code, rb)
	}

	// Dropping the condition is what gets through, which is the deliberate
	// escape: an unconditional write is somebody choosing to overwrite.
	if plain, pb := upload(t, base, sess, path, []byte("my edit")); plain != http.StatusOK {
		t.Fatalf("the unconditional retry answered %d: %s", plain, pb)
	}
}

// uploadIfMatch is upload with a change token attached.
func uploadIfMatch(t *testing.T, base string, sess session, path string, content []byte, etag string) (int, []byte) {
	t.Helper()

	req, err := http.NewRequest(http.MethodPost,
		base+"/api/v1/files/write?path="+urlEscape(path), bytes.NewReader(content))
	if err != nil {
		t.Fatalf("building: %v", err)
	}
	sess.attach(req)
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("If-Match", `"`+etag+`"`)

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
	base, sess, share := contentShare(t, everyPerm(), want)

	status, body := post(t, base+"/api/v1/files/move", sess, map[string]string{
		"from": "/" + share + "/doc.bin",
		"to":   "/" + share + "/sub/moved.bin",
	})
	if status != http.StatusOK {
		t.Fatalf("moving answered %d: %s", status, body)
	}

	// Both ends, because a move that copied without removing is a move that
	// silently doubled the data. Absence is asked of stat: it is the one
	// place a path becomes a content reference, so a path stat refuses is a
	// path nothing can read.
	if code, _ := statPath(t, base, sess, "/"+share+"/doc.bin"); code != http.StatusNotFound {
		t.Errorf("the source still stats after a move: %d", code)
	}
	code, _, got := download(t, base, sess, "/"+share+"/sub/moved.bin", "")
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
	base, sess, share := contentShare(t, everyPerm(), []byte("source"))

	if status, body := upload(t, base, sess, "/"+share+"/sub/taken.bin", []byte("destination")); status != http.StatusOK {
		t.Fatalf("preparing the destination answered %d: %s", status, body)
	}

	status, body := post(t, base+"/api/v1/files/move", sess, map[string]string{
		"from": "/" + share + "/doc.bin",
		"to":   "/" + share + "/sub/taken.bin",
	})
	if status == http.StatusOK {
		t.Fatalf("a move onto a taken name succeeded: %s", body)
	}

	// The destination is untouched, which is the thing that matters: a
	// refusal that had already overwritten would have destroyed data.
	_, _, got := download(t, base, sess, "/"+share+"/sub/taken.bin", "")
	if string(got) != "destination" {
		t.Errorf("the destination now holds %q", got)
	}
	// And the source is still there.
	if code, _, _ := download(t, base, sess, "/"+share+"/doc.bin", ""); code != http.StatusOK {
		t.Error("the refused move removed the source")
	}
}

// An unknown conflict policy is refused rather than quietly treated as the
// default. The two differ by whether a file survives.
func TestAnUnknownConflictPolicyIsRefused(t *testing.T) {
	base, sess, share := contentShare(t, everyPerm(), []byte("source"))

	status, body := post(t, base+"/api/v1/files/move", sess, map[string]string{
		"from":        "/" + share + "/doc.bin",
		"to":          "/" + share + "/sub/moved.bin",
		"on_conflict": "obliterate",
	})
	if status == http.StatusOK {
		t.Fatalf("an unknown policy was accepted: %s", body)
	}

	if code, _, _ := download(t, base, sess, "/"+share+"/doc.bin", ""); code != http.StatusOK {
		t.Error("the refused move relocated the file anyway")
	}
}

// Duplicating an entry copies it beside itself.
//
// A duplicate names the source as its own destination and lets the rename
// policy pick the free name. The self-containment guard ran against the
// requested path rather than the one the rename settled on, so it refused
// every duplicate before the rename happened: the button answered 404 and
// nothing was written.
func TestDuplicatingAFile(t *testing.T) {
	want := payload()
	base, sess, share := contentShare(t, everyPerm(), want)

	status, body := post(t, base+"/api/v1/files/copy", sess, map[string]string{
		"from":        "/" + share + "/doc.bin",
		"to":          "/" + share + "/doc.bin",
		"on_conflict": "rename",
	})
	if status != http.StatusAccepted && status != http.StatusOK {
		t.Fatalf("duplicating answered %d: %v", status, body)
	}

	// The copy is a job, so the listing is read once it has settled.
	var names []string
	// Waits for the duplicate itself. Waiting for "more than one entry" broke
	// the moment the fixture grew a second one: the loop left on the first
	// poll and read the listing before the copy had written anything.
	var duplicate string
	for range 200 {
		names = listNames(t, base, sess, "/"+share)
		duplicate = ""
		for _, n := range names {
			if n != "doc.bin" && strings.HasPrefix(n, "doc") {
				duplicate = n
			}
		}
		if duplicate != "" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if duplicate == "" {
		t.Fatalf("the duplicate is not in the listing: %v", names)
	}
	// Named after the original rather than left as a bare suffix, which is
	// what a person looks for in the folder afterwards.
	if duplicate != "doc (2).bin" {
		t.Errorf("the duplicate is called %q, want %q", duplicate, "doc (2).bin")
	}

	// And it holds the original's bytes, which is the whole point.
	code, _, got := download(t, base, sess, "/"+share+"/"+duplicate, "")
	if code != http.StatusOK {
		t.Fatalf("reading the duplicate answered %d", code)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("the duplicate holds %d bytes, want %d", len(got), len(want))
	}

	// The original is still there: a duplicate adds, never moves.
	if code, _, orig := download(t, base, sess, "/"+share+"/doc.bin", ""); code != http.StatusOK || !bytes.Equal(orig, want) {
		t.Error("the original did not survive its own duplicate")
	}
}

// listNames reads one directory's entry names.
func listNames(t *testing.T, base string, sess session, path string) []string {
	t.Helper()

	status, raw := authed(t, http.MethodGet, base+"/api/v1/files/list?path="+urlEscape(path), sess)
	if status != http.StatusOK {
		t.Fatalf("listing %s answered %d: %s", path, status, raw)
	}
	var page struct {
		Entries []struct {
			Name string `json:"name"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(raw, &page); err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(page.Entries))
	for _, e := range page.Entries {
		out = append(out, e.Name)
	}
	return out
}

// A copy is accepted as a job and leaves both files in place.
func TestCopyingAFile(t *testing.T) {
	want := payload()
	base, sess, share := contentShare(t, everyPerm(), want)

	status, body := post(t, base+"/api/v1/files/copy", sess, map[string]string{
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

	// The job writes after the request returns, so waiting is what keeps the
	// temporary directory from being removed underneath it: an unwaited copy
	// leaves the cleanup racing a live writer, and the databases close while
	// the journal is still being written.
	awaitJobToken(t, base, sess, stringField(view, "id"))

	// The source is untouched. A copy that removed it would be a move.
	if code, _, _ := download(t, base, sess, "/"+share+"/doc.bin", ""); code != http.StatusOK {
		t.Error("the source is gone after a copy")
	}
}

// awaitJobToken polls one job until it leaves the running state, using an app
// password rather than a session.
//
// A test that starts a job and returns leaves the runner writing into a
// t.TempDir the framework is about to remove, which fails the test with a
// cleanup error naming a directory that is merely still in use.
func awaitJobToken(t *testing.T, base string, sess session, id string) {
	t.Helper()
	if id == "" {
		return
	}
	clk := clock.System()
	deadline := clk.Now().Add(20 * time.Second)
	for clk.Now().Before(deadline) {
		status, raw := authed(t, http.MethodGet, base+"/api/v1/jobs/"+id, sess)
		if status != http.StatusOK {
			// The job row is gone, which means it finished and was swept.
			return
		}
		var job map[string]any
		if err := json.Unmarshal(raw, &job); err != nil {
			t.Fatalf("decoding the job: %v", err)
		}
		if state, isString := job["state"].(string); !isString || state != "running" {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the job never left the running state")
}

// The rollup reports what is beneath a directory.
func TestTheRecursiveSize(t *testing.T) {
	content := payload()
	base, sess, share := contentShare(t, everyPerm(), content)

	status, body := authed(t, http.MethodGet,
		base+"/api/v1/files/size?path="+urlEscape("/"+share), sess)
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
	base, sess, share := contentShare(t, everyPerm(), []byte("x"))

	if status, body := upload(t, base, sess, "/"+share+"/fresh.bin", payload()); status != http.StatusOK {
		t.Fatalf("writing answered %d: %s", status, body)
	}

	status, body := authed(t, http.MethodGet, base+"/api/v1/files/recent", sess)
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
	base, _, sess := bootWithUser(t)

	status, body := authed(t, http.MethodGet, base+"/api/v1/files/recent", sess)
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

	base := serve(t, e)
	sess := signIn(t, base, "alice", "a-long-enough-password")

	status, body := post(t, base+"/api/v1/files/copy", sess, map[string]string{
		"from": "/reference/doc.bin",
		"to":   "/mine/copy.bin",
	})
	if status != http.StatusAccepted {
		t.Fatalf("copying from a read-only share answered %d: %s", status, body)
	}

	// A move from the same source has to be refused, which is what proves
	// the two requirements are actually different rather than both weak.
	moveStatus, moveBody := post(t, base+"/api/v1/files/move", sess, map[string]string{
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
	base, sess, share := contentShare(t, everyPerm(), []byte("x"))

	// More writes than the ceiling, so an unbounded limit would return more
	// rows than the ceiling permits.
	const writes = 12
	for i := 0; i < writes; i++ {
		if status, body := upload(t, base, sess,
			fmt.Sprintf("/%s/f%d.bin", share, i), []byte("y")); status != http.StatusOK {
			t.Fatalf("write %d answered %d: %s", i, status, body)
		}
	}

	for _, limit := range []string{"1", "5", "999999", "-1", "0", "abc"} {
		status, body := authed(t, http.MethodGet,
			base+"/api/v1/files/recent?limit="+limit, sess)
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

// contentRef reads one row's own content reference, which is where a client
// gets it: the listing and stat seal it into the row, and nothing composes a
// content URL out of a path any more.
func contentRef(t *testing.T, base string, sess session, path string) string {
	t.Helper()

	status, raw := statPath(t, base, sess, path)
	if status != http.StatusOK {
		t.Fatalf("stat %q answered %d: %s", path, status, raw)
	}
	var entry struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(raw, &entry); err != nil {
		t.Fatalf("decoding the stat of %q: %v\n%s", path, err, raw)
	}
	if entry.Content == "" {
		t.Fatalf("stat %q carries no content reference: %s", path, raw)
	}
	return entry.Content
}

// statPath asks the server about one path without requiring it to be there.
func statPath(t *testing.T, base string, sess session, path string) (int, []byte) {
	t.Helper()
	return authed(t, http.MethodGet, base+"/api/v1/files/stat?path="+urlEscape(path), sess)
}
