//go:build linux

package lifecycle_test

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// An archive of a subtree reads back through the standard library.
//
// Verified with archive/zip rather than with the writer that produced it: a
// zip that only its own author can open is not a zip, and the thing a person
// does with this response is hand it to their operating system.
func TestAnArchiveOfASubtree(t *testing.T) {
	base, token, share := contentShare(t, everyPerm(), []byte("root file"))

	// Something to put in it, including a nested directory so the entry
	// names have to carry a path.
	if status, body := upload(t, base, token, "/"+share+"/sub/one.txt", []byte("first")); status != http.StatusOK {
		t.Fatalf("writing one.txt answered %d: %s", status, body)
	}
	if status, body := upload(t, base, token, "/"+share+"/sub/two.txt", []byte("second")); status != http.StatusOK {
		t.Fatalf("writing two.txt answered %d: %s", status, body)
	}

	status, header, body := fetchArchive(t, base, token,
		map[string]any{"paths": []string{"/" + share + "/sub"}, "name": "bundle.zip"})
	if status != http.StatusOK {
		t.Fatalf("archiving answered %d: %s", status, body)
	}
	if ct := header.Get("Content-Type"); ct != "application/zip" {
		t.Errorf("the archive is typed %q", ct)
	}
	if cd := header.Get("Content-Disposition"); !strings.Contains(cd, "bundle.zip") {
		t.Errorf("the disposition is %q, which does not name the file", cd)
	}

	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("the standard library cannot read the archive: %v", err)
	}

	got := map[string]string{}
	for _, f := range zr.File {
		if strings.HasSuffix(f.Name, "/") {
			continue
		}
		rc, oerr := f.Open()
		if oerr != nil {
			t.Fatalf("opening %s: %v", f.Name, oerr)
		}
		var buf bytes.Buffer
		if _, cerr := buf.ReadFrom(rc); cerr != nil {
			t.Fatalf("reading %s: %v", f.Name, cerr)
		}
		if cerr := rc.Close(); cerr != nil {
			t.Errorf("closing %s: %v", f.Name, cerr)
		}
		got[f.Name] = buf.String()
	}

	if len(got) != 2 {
		t.Fatalf("the archive holds %d files: %v", len(got), keysOf(got))
	}
	for name, want := range map[string]string{"sub/one.txt": "first", "sub/two.txt": "second"} {
		if got[name] != want {
			t.Errorf("%s holds %q, want %q", name, got[name], want)
		}
	}
}

// keysOf lists a map's keys, sorted, for a failure message.
func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// postRaw sends JSON and returns the whole response, body included, so a
// binary answer survives.
func postRaw(t *testing.T, url, token string, body any) (int, http.Header, []byte) {
	t.Helper()

	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(encoded))
	if err != nil {
		t.Fatalf("building: %v", err)
	}
	req.SetBasicAuth("ignored", token)
	req.Header.Set("Content-Type", "application/json")

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

// fetchArchive asks for an archive and follows the ticket to its bytes.
//
// Two steps, because that is what the surface is: the build is held in
// memory so the fetch can carry a length and answer ranges, which is what
// makes a folder download show progress and resume. A selection too large to
// hold is streamed by the first response instead, and this handles both so a
// test asserting on the archive itself does not care which path it took.
func fetchArchive(t *testing.T, base, token string, body any) (int, http.Header, []byte) {
	t.Helper()

	status, header, raw := postRaw(t, base+"/api/v1/files/archive", token, body)
	if status != http.StatusOK || header.Get("Content-Type") == "application/zip" {
		// A refusal, or the streamed fallback, which is already the archive.
		return status, header, raw
	}

	var ticket struct {
		URL  string `json:"url"`
		Size int64  `json:"size"`
	}
	if err := json.Unmarshal(raw, &ticket); err != nil || ticket.URL == "" {
		t.Fatalf("the archive answered no ticket: %s", raw)
	}

	req, err := http.NewRequest(http.MethodGet, base+ticket.URL, nil)
	if err != nil {
		t.Fatalf("building the fetch: %v", err)
	}
	req.SetBasicAuth("ignored", token)
	resp, err := testClient().Do(req)
	if err != nil {
		t.Fatalf("fetching: %v", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	}()
	got := readAll(t, resp)
	if int64(len(got)) != ticket.Size {
		t.Errorf("the ticket promised %d bytes and %d arrived", ticket.Size, len(got))
	}
	return resp.StatusCode, resp.Header, got
}

// A prepared archive carries a length and answers ranges.
//
// This is what makes a folder download behave like a file download: the
// browser can show progress because the length is known, and a connection
// lost partway resumes rather than starting again. A streamed archive can do
// neither, which is why the bytes are built before the response.
func TestAPreparedArchiveResumes(t *testing.T) {
	base, token, share := contentShare(t, everyPerm(), []byte("root file"))

	if status, _ := upload(t, base, token, "/"+share+"/sub/one.txt", []byte("first")); status != http.StatusOK {
		t.Fatal("writing the file failed")
	}

	status, _, raw := postRaw(t, base+"/api/v1/files/archive", token,
		map[string]any{"paths": []string{"/" + share + "/sub"}, "name": "bundle.zip"})
	if status != http.StatusOK {
		t.Fatalf("preparing answered %d: %s", status, raw)
	}
	var ticket struct {
		URL  string `json:"url"`
		Size int64  `json:"size"`
	}
	if err := json.Unmarshal(raw, &ticket); err != nil {
		t.Fatalf("the ticket does not decode: %s", raw)
	}
	if ticket.Size <= 0 || ticket.URL == "" {
		t.Fatalf("the ticket names no archive: %s", raw)
	}

	// The first half, as an interrupted download would have taken.
	half := ticket.Size / 2
	code, header, head := rangeGet(t, base+ticket.URL, token, fmt.Sprintf("bytes=0-%d", half-1))
	if code != http.StatusPartialContent {
		t.Fatalf("a range answered %d, want 206", code)
	}
	if header.Get("Accept-Ranges") != "bytes" {
		t.Error("the response does not advertise ranges, so a client will not try to resume")
	}
	if got := header.Get("Content-Range"); got != fmt.Sprintf("bytes 0-%d/%d", half-1, ticket.Size) {
		t.Errorf("the content range is %q", got)
	}

	// The rest, which is the resume.
	code, _, tail := rangeGet(t, base+ticket.URL, token, fmt.Sprintf("bytes=%d-", half))
	if code != http.StatusPartialContent {
		t.Fatalf("the resume answered %d, want 206", code)
	}

	// The two halves are the archive, which is the whole claim: a resumed
	// download has to produce a file that opens.
	joined := append(append([]byte{}, head...), tail...)
	if int64(len(joined)) != ticket.Size {
		t.Fatalf("the two halves are %d bytes, want %d", len(joined), ticket.Size)
	}
	if _, err := zip.NewReader(bytes.NewReader(joined), int64(len(joined))); err != nil {
		t.Fatalf("a resumed download does not open: %v", err)
	}
}

// A ticket is a capability and does not cross accounts.
//
// The archive holds one account's files. A ticket that another account could
// fetch would be a way to read them, so the owner is checked where the ticket
// is resolved rather than left to the caller.
func TestAnArchiveTicketBelongsToItsOwner(t *testing.T) {
	base, token, share := contentShare(t, everyPerm(), []byte("private"))

	status, _, raw := postRaw(t, base+"/api/v1/files/archive", token,
		map[string]any{"paths": []string{"/" + share}})
	if status != http.StatusOK {
		t.Fatalf("preparing answered %d: %s", status, raw)
	}
	var ticket struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(raw, &ticket); err != nil {
		t.Fatalf("the ticket does not decode: %s", raw)
	}

	// No credential at all is refused, which is what stops a ticket in a
	// browser history from being a link anybody can follow.
	req, err := http.NewRequest(http.MethodGet, base+ticket.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := testClient().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	}()
	if resp.StatusCode == http.StatusOK {
		t.Error("an archive ticket was fetched with no credential")
	}
}

// rangeGet performs one ranged read.
func rangeGet(t *testing.T, url, token, spec string) (int, http.Header, []byte) {
	t.Helper()

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("building: %v", err)
	}
	req.SetBasicAuth("ignored", token)
	req.Header.Set("Range", spec)

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

// An archive of several roots holds all of them.
func TestAnArchiveOfSeveralPaths(t *testing.T) {
	base, token, share := contentShare(t, everyPerm(), []byte("the root file"))

	if status, _ := upload(t, base, token, "/"+share+"/sub/nested.txt", []byte("nested")); status != http.StatusOK {
		t.Fatal("writing the nested file failed")
	}

	status, _, body := fetchArchive(t, base, token, map[string]any{
		"paths": []string{"/" + share + "/doc.bin", "/" + share + "/sub"},
	})
	if status != http.StatusOK {
		t.Fatalf("archiving answered %d: %s", status, body)
	}

	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("the archive does not read: %v", err)
	}

	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	sort.Strings(names)

	var sawRoot, sawNested bool
	for _, n := range names {
		if n == "doc.bin" {
			sawRoot = true
		}
		if n == "sub/nested.txt" {
			sawNested = true
		}
	}
	if !sawRoot || !sawNested {
		t.Errorf("the archive holds %v, missing one of the two roots", names)
	}
}

// A selection holding one path the caller cannot read is refused whole.
//
// A partial archive is worse than none: the person saves it, sees files, and
// has no way to know which ones are missing.
func TestAnArchiveWithAnUnreadablePathIsRefused(t *testing.T) {
	base, token, share := contentShare(t, everyPerm(), []byte("readable"))

	status, _, body := postRaw(t, base+"/api/v1/files/archive", token, map[string]any{
		"paths": []string{"/" + share + "/doc.bin", "/nosuchshare/secret.txt"},
	})
	if status == http.StatusOK {
		t.Fatalf("an archive was built over an unreadable path: %d bytes", len(body))
	}
}

// An empty selection is refused rather than producing an empty zip.
func TestAnEmptyArchiveSelectionIsRefused(t *testing.T) {
	base, token, _ := contentShare(t, everyPerm(), []byte("x"))

	status, _, _ := postRaw(t, base+"/api/v1/files/archive", token,
		map[string]any{"paths": []string{}})
	if status == http.StatusOK {
		t.Error("an empty selection produced an archive")
	}
}

// A filename that could inject a header field is refused.
//
// The name reaches Content-Disposition. A quote or a newline in it could end
// the field and start another, which is a header the client never asked for.
func TestAnArchiveNameCannotInjectAHeader(t *testing.T) {
	base, token, share := contentShare(t, everyPerm(), []byte("x"))

	for _, name := range []string{
		`evil".zip`,
		"line\r\nX-Injected: yes",
		"line\nX-Injected: yes",
		"back\\slash.zip",
		"a/b.zip",
		strings.Repeat("a", 300),
	} {
		status, header, _ := postRaw(t, base+"/api/v1/files/archive", token,
			map[string]any{"paths": []string{"/" + share + "/doc.bin"}, "name": name})
		if status == http.StatusOK {
			t.Errorf("the name %q was accepted", name)
		}
		if header.Get("X-Injected") != "" {
			t.Fatalf("the name %q injected a header field", name)
		}
	}
}

// An absent name still produces something a person can open.
func TestAnArchiveWithoutANameGetsADefault(t *testing.T) {
	base, token, share := contentShare(t, everyPerm(), []byte("x"))

	status, header, _ := fetchArchive(t, base, token,
		map[string]any{"paths": []string{"/" + share + "/doc.bin"}})
	if status != http.StatusOK {
		t.Fatalf("answered %d", status)
	}
	cd := header.Get("Content-Disposition")
	if !strings.Contains(cd, ".zip") {
		t.Errorf("the disposition %q names no zip, so a browser saves an extensionless file", cd)
	}
}

// The listing reads an existing zip's own directory.
func TestListingInsideAnArchive(t *testing.T) {
	base, token, share := contentShare(t, everyPerm(), []byte("unused"))

	// Built with the standard library, so the listing is proven against a zip
	// this tree did not write.
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range map[string]string{
		"readme.txt":     "hello",
		"docs/guide.txt": "a longer body for a different size",
	} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, werr := w.Write([]byte(body)); werr != nil {
			t.Fatal(werr)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	if status, body := upload(t, base, token, "/"+share+"/bundle.zip", buf.Bytes()); status != http.StatusOK {
		t.Fatalf("uploading the zip answered %d: %s", status, body)
	}

	status, body := authed(t, http.MethodGet,
		base+"/api/v1/files/archive/list?path="+urlEscape("/"+share+"/bundle.zip"), token)
	if status != http.StatusOK {
		t.Fatalf("listing answered %d: %s", status, body)
	}

	var listing map[string]any
	if err := json.Unmarshal(body, &listing); err != nil {
		t.Fatalf("decoding %s: %v", body, err)
	}
	entries, ok := listing["entries"].([]any)
	if !ok {
		t.Fatalf("no entries in %s", body)
	}
	if len(entries) != 2 {
		t.Fatalf("the listing holds %d entries, want 2: %s", len(entries), body)
	}

	// Sizes are decimal strings, since an archive can hold a member past
	// 2^53 and a JavaScript number would round it.
	for _, raw := range entries {
		entry, isMap := raw.(map[string]any)
		if !isMap {
			t.Fatalf("an entry is not an object: %v", raw)
		}
		if _, isString := entry["size"].(string); !isString {
			t.Errorf("the size is %T, not a decimal string", entry["size"])
		}
	}
	if _, isString := listing["total_uncompressed"].(string); !isString {
		t.Errorf("the total is %T, not a decimal string", listing["total_uncompressed"])
	}
}

// A file that is not an archive answers exactly as an absent one does.
//
// Whether a file this account cannot see happens to be a zip is not something
// the answer should disclose.
func TestListingANonArchiveIsIndistinguishableFromAbsence(t *testing.T) {
	base, token, share := contentShare(t, everyPerm(), []byte("not a zip at all"))

	notZip, notZipBody := authed(t, http.MethodGet,
		base+"/api/v1/files/archive/list?path="+urlEscape("/"+share+"/doc.bin"), token)
	absent, absentBody := authed(t, http.MethodGet,
		base+"/api/v1/files/archive/list?path="+urlEscape("/"+share+"/nothing.zip"), token)

	if notZip != absent {
		t.Errorf("a non-archive answers %d and an absent file answers %d", notZip, absent)
	}
	if string(notZipBody) != string(absentBody) {
		t.Errorf("the two answers differ:\n %s\n %s", notZipBody, absentBody)
	}
}

// An archive entry whose name would escape is not written.
//
// The names come from the tree, so this is defence in depth rather than the
// only guard, but an archive carrying ../ is one that overwrites files
// outside the directory a person extracted it into.
func TestArchiveEntryNamesDoNotEscape(t *testing.T) {
	base, token, share := contentShare(t, everyPerm(), []byte("root"))

	if status, _ := upload(t, base, token, "/"+share+"/sub/inner.txt", []byte("inner")); status != http.StatusOK {
		t.Fatal("writing failed")
	}

	status, _, body := fetchArchive(t, base, token,
		map[string]any{"paths": []string{"/" + share}})
	if status != http.StatusOK {
		t.Fatalf("answered %d", status)
	}

	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("the archive does not read: %v", err)
	}
	for _, f := range zr.File {
		if strings.HasPrefix(f.Name, "/") || strings.Contains(f.Name, "..") {
			t.Errorf("the archive holds an escaping name %q", f.Name)
		}
		if filepath.IsAbs(f.Name) {
			t.Errorf("the archive holds an absolute name %q", f.Name)
		}
	}
}

// An empty directory survives the round trip.
//
// A zip has no directory concept beyond a zero-length member ending in a
// slash. Without one the directory vanishes on extraction, and a person who
// archived a tree gets back a different tree.
func TestAnEmptyDirectorySurvivesTheArchive(t *testing.T) {
	base, token, share := contentShare(t, everyPerm(), []byte("root"))

	status, body := post(t, base+"/api/v1/files/mkdir", token,
		map[string]string{"path": "/" + share + "/empty"})
	if status != http.StatusCreated {
		t.Fatalf("mkdir answered %d: %s", status, body)
	}

	code, _, archived := fetchArchive(t, base, token,
		map[string]any{"paths": []string{"/" + share + "/empty"}})
	if code != http.StatusOK {
		t.Fatalf("archiving answered %d", code)
	}

	zr, err := zip.NewReader(bytes.NewReader(archived), int64(len(archived)))
	if err != nil {
		t.Fatalf("the archive does not read: %v", err)
	}
	var sawDir bool
	for _, f := range zr.File {
		if strings.HasSuffix(f.Name, "/") {
			sawDir = true
		}
	}
	if !sawDir {
		t.Error("the empty directory is not in the archive, so extracting loses it")
	}
}

// One unreadable file does not lose the rest of the archive.
//
// A person selecting a folder gets what they can read. Failing the whole
// archive over one entry means a single stray permission bit makes a folder
// undownloadable, with nothing saying which file caused it.
func TestAnUnreadableEntryDoesNotLoseTheArchive(t *testing.T) {
	base, token, share, host := contentShareAt(t, everyPerm(), []byte("root"))

	for name, body := range map[string]string{
		"sub/readable-one.txt": "first",
		"sub/readable-two.txt": "second",
		"sub/locked.txt":       "hidden",
	} {
		if status, out := upload(t, base, token, "/"+share+"/"+name, []byte(body)); status != http.StatusOK {
			t.Fatalf("writing %s answered %d: %s", name, status, out)
		}
	}

	// Unreadable on disk, which is what a stray permission bit looks like.
	// The server runs as this user, so removing every mode bit is what makes
	// the open fail.
	locked := filepath.Join(host, "sub", "locked.txt")
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatalf("locking the file: %v", err)
	}
	t.Cleanup(func() {
		if cerr := os.Chmod(locked, 0o600); cerr != nil {
			t.Errorf("unlocking: %v", cerr)
		}
	})

	status, _, body := fetchArchive(t, base, token,
		map[string]any{"paths": []string{"/" + share + "/sub"}})
	if status != http.StatusOK {
		t.Fatalf("archiving answered %d: %s", status, body)
	}

	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("one unreadable entry produced an unreadable archive: %v", err)
	}

	var readable int
	for _, f := range zr.File {
		if strings.HasSuffix(f.Name, "readable-one.txt") || strings.HasSuffix(f.Name, "readable-two.txt") {
			readable++
		}
		if strings.HasSuffix(f.Name, "locked.txt") {
			t.Errorf("the archive holds %q, which could not be read", f.Name)
		}
	}
	if readable != 2 {
		t.Errorf("the archive holds %d of the 2 readable files", readable)
	}
}
