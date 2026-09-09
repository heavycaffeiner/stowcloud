//go:build linux && compat_nc

package lifecycle_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Uploading, both ways the clients do it.
//
// A plain PUT for a small file, and the chunked collection for a large one.
// The chunked sequence below is the Android client's, step for step: create
// the session with the destination named in a header, list what arrived to
// resume, send the parts under zero-padded numeric names, then move the
// assembly member onto the destination.

// Before it sends a byte, one client probes the target folder with HEAD and
// accepts only 200, 401 or 403. Anything else, and it abandons the upload
// with whatever the probe said: a folder answering "method not allowed" made
// every upload into that folder fail before it started.
func TestTheFolderProbeThatPrecedesAnUploadSucceeds(t *testing.T) {
	t.Parallel()
	f := newNCFixture(t, []byte("hello"))

	for _, target := range []string{f.filePath(""), f.filePath("sub")} {
		resp, body := f.request(t, "HEAD", target, nil, nil)
		if resp.StatusCode != 200 {
			t.Fatalf("the probe of %s answered %d, want 200\n%s", target, resp.StatusCode, body)
		}
		if len(body) != 0 {
			t.Errorf("the probe of %s returned %d bytes", target, len(body))
		}
		if resp.Header.Get("ETag") == "" {
			t.Errorf("the probe of %s carried no validator", target)
		}
	}

	// And the upload that follows the probe lands, which is the sequence the
	// client actually runs.
	if resp, body := f.request(t, "PUT", f.filePath("sub/probed.txt"),
		strings.NewReader("after the probe"), nil); resp.StatusCode != 201 {
		t.Fatalf("the upload after the probe answered %d\n%s", resp.StatusCode, body)
	}
	onDisk, err := os.ReadFile(filepath.Join(f.host, "sub", "probed.txt"))
	if err != nil {
		t.Fatalf("reading the uploaded file: %v", err)
	}
	if string(onDisk) != "after the probe" {
		t.Errorf("the file holds %q", onDisk)
	}
}

// A plain upload creates the file and reports back what the client needs to
// record it: both etag spellings and the file id. One client fails an upload
// that already succeeded when the identity header is missing.
func TestAPlainUploadReportsIdentityAndEtag(t *testing.T) {
	t.Parallel()
	f := newNCFixture(t, []byte("seed"))

	resp, body := f.request(t, "PUT", f.filePath("fresh.txt"),
		strings.NewReader("uploaded bytes"), map[string]string{
			"Content-Type":    "application/octet-stream",
			"OC-Total-Length": "14",
			"X-OC-Mtime":      "1700000000",
		})
	if resp.StatusCode != 201 {
		t.Fatalf("answered %d, want 201\n%s", resp.StatusCode, body)
	}
	if resp.Header.Get("OC-FileId") == "" {
		t.Error("no OC-FileId, and one client fails the transfer without it")
	}
	if resp.Header.Get("OC-ETag") == "" || resp.Header.Get("ETag") == "" {
		t.Errorf("etag headers are %q and %q", resp.Header.Get("OC-ETag"), resp.Header.Get("ETag"))
	}

	onDisk, err := os.ReadFile(filepath.Join(f.host, "fresh.txt"))
	if err != nil {
		t.Fatalf("reading the uploaded file: %v", err)
	}
	if string(onDisk) != "uploaded bytes" {
		t.Errorf("the file holds %q", onDisk)
	}
}

// The modification time a client sends is applied, and the acceptance is
// reported. A client that is told the time was accepted stops correcting it,
// so reporting acceptance without applying it leaves the two stamps disagreeing
// forever.
func TestTheUploadedModificationTimeIsApplied(t *testing.T) {
	t.Parallel()
	f := newNCFixture(t, []byte("seed"))

	const stamp = 1600000000
	resp, _ := f.request(t, "PUT", f.filePath("stamped.txt"),
		strings.NewReader("body"), map[string]string{"X-OC-Mtime": strconv.Itoa(stamp)})
	if resp.StatusCode != 201 {
		t.Fatalf("answered %d, want 201", resp.StatusCode)
	}
	if got := resp.Header.Get("X-OC-MTime"); got != "accepted" {
		t.Errorf("the acceptance header is %q", got)
	}

	info, err := os.Stat(filepath.Join(f.host, "stamped.txt"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.ModTime().Unix(); got != stamp {
		t.Errorf("the file's time is %d, want %d", got, stamp)
	}
}

// Replacing an existing file answers 204 rather than 201, which is how a
// client tells a create from an overwrite.
func TestReplacingAFileAnswersNoContent(t *testing.T) {
	t.Parallel()
	f := newNCFixture(t, []byte("original"))

	resp, body := f.request(t, "PUT", f.filePath("doc.bin"),
		strings.NewReader("replaced"), nil)
	if resp.StatusCode != 204 {
		t.Fatalf("answered %d, want 204\n%s", resp.StatusCode, body)
	}
	if resp.Header.Get("OC-FileId") == "" {
		t.Error("an overwrite carries no file id")
	}
}

// A conditional upload has to work. The core refuses every validator it is
// handed by design, so this layer compares the etag itself: a stale one is
// refused with 412 and the current one goes through.
func TestAConditionalUploadHonoursTheEtagItWasGiven(t *testing.T) {
	t.Parallel()
	f := newNCFixture(t, []byte("original"))

	_, listed := f.request(t, "PROPFIND", f.filePath("doc.bin"), strings.NewReader(propfindBody),
		map[string]string{"Depth": "0", "Content-Type": "application/xml"})
	entry := entryOf(t, string(listed), "/remote.php/dav/files/"+f.login+"/"+f.share+"/doc.bin")
	current := strings.ReplaceAll(propValue(t, entry, "d:getetag"), "&quot;", `"`)

	stale, _ := f.request(t, "PUT", f.filePath("doc.bin"),
		strings.NewReader("refused"), map[string]string{"If-Match": `"not-the-current-token"`})
	if stale.StatusCode != 412 {
		t.Errorf("a stale validator answered %d, want 412", stale.StatusCode)
	}

	fresh, body := f.request(t, "PUT", f.filePath("doc.bin"),
		strings.NewReader("accepted"), map[string]string{"If-Match": current})
	if fresh.StatusCode != 204 && fresh.StatusCode != 201 {
		t.Fatalf("the current validator answered %d\n%s", fresh.StatusCode, body)
	}
	onDisk, err := os.ReadFile(filepath.Join(f.host, "doc.bin"))
	if err != nil {
		t.Fatalf("reading: %v", err)
	}
	if string(onDisk) != "accepted" {
		t.Errorf("the file holds %q", onDisk)
	}
}

// One client sends a header asking for the missing parents to be made rather
// than issuing its own MKCOL chain.
func TestAnUploadCanCreateItsParents(t *testing.T) {
	t.Parallel()
	f := newNCFixture(t, []byte("seed"))

	resp, body := f.request(t, "PUT", f.filePath("deep/deeper/file.txt"),
		strings.NewReader("nested"), map[string]string{"X-NC-WebDAV-Auto-Mkcol": "1"})
	if resp.StatusCode != 201 {
		t.Fatalf("answered %d, want 201\n%s", resp.StatusCode, body)
	}
	if _, err := os.Stat(filepath.Join(f.host, "deep", "deeper", "file.txt")); err != nil {
		t.Errorf("the nested file is not there: %v", err)
	}
}

// Making a folder answers 201 and reports its identity, which one client
// stores as the folder's remote id.
func TestMakingAFolderReportsItsIdentity(t *testing.T) {
	t.Parallel()
	f := newNCFixture(t, []byte("seed"))

	resp, body := f.request(t, "MKCOL", f.filePath("made"), nil, nil)
	if resp.StatusCode != 201 {
		t.Fatalf("answered %d, want 201\n%s", resp.StatusCode, body)
	}
	if resp.Header.Get("OC-FileId") == "" {
		t.Error("no OC-FileId on the created collection")
	}

	again, _ := f.request(t, "MKCOL", f.filePath("made"), nil, nil)
	if again.StatusCode != 405 {
		t.Errorf("a second attempt answered %d, and one client maps 405 to "+
			"already-there rather than to an error", again.StatusCode)
	}
}

// The whole chunked sequence, as the Android client sends it.
func TestTheChunkedUploadSequenceCompletes(t *testing.T) {
	t.Parallel()
	f := newNCFixture(t, []byte("seed"))

	session := f.base + "/remote.php/dav/uploads/" + f.login + "/transfer-1"
	destination := f.filePath("large.bin")

	// Step one: the collection, with the destination named up front.
	resp, body := f.request(t, "MKCOL", session, nil,
		map[string]string{"Destination": destination})
	if resp.StatusCode != 201 {
		t.Fatalf("creating the session answered %d, want 201\n%s", resp.StatusCode, body)
	}

	// Step two: what has already arrived. Nothing has, so the listing holds
	// the collection alone.
	resp, body = f.request(t, "PROPFIND", session, strings.NewReader(
		`<?xml version="1.0"?><d:propfind xmlns:d="DAV:"><d:prop>`+
			`<d:resourcetype/><d:getcontentlength/></d:prop></d:propfind>`),
		map[string]string{"Depth": "1", "Content-Type": "application/xml"})
	if resp.StatusCode != 207 {
		t.Fatalf("listing the session answered %d, want 207\n%s", resp.StatusCode, body)
	}

	// Step three: the parts, under the zero-padded names the client's own
	// resume logic recognises.
	first := bytes.Repeat([]byte("a"), 1024)
	second := bytes.Repeat([]byte("b"), 512)
	for i, part := range [][]byte{first, second} {
		name := "00000" + strconv.Itoa(i+1)
		resp, body = f.request(t, "PUT", session+"/"+name, bytes.NewReader(part),
			map[string]string{"Destination": destination})
		if resp.StatusCode != 201 && resp.StatusCode != 204 {
			t.Fatalf("chunk %s answered %d\n%s", name, resp.StatusCode, body)
		}
	}

	// A resume listing is what the client reads to decide where to carry on
	// from. It sums the reported lengths into the next byte offset and takes
	// the highest reported name as the last chunk it sent, so what has to be
	// true is those two numbers, not that every name it ever used appears:
	// an in-order run is one contiguous stretch of the file and reporting it
	// as several members would invent per-member sizes.
	_, body = f.request(t, "PROPFIND", session, strings.NewReader(
		`<?xml version="1.0"?><d:propfind xmlns:d="DAV:"><d:prop>`+
			`<d:resourcetype/><d:getcontentlength/></d:prop></d:propfind>`),
		map[string]string{"Depth": "1", "Content-Type": "application/xml"})
	listing := string(body)
	if !strings.Contains(listing, "000002") {
		t.Errorf("the listing does not name the last chunk sent:\n%s", listing)
	}
	if got := sumContentLengths(t, listing); got != len(first)+len(second) {
		t.Errorf("the listing accounts for %d bytes, want %d\n%s",
			got, len(first)+len(second), listing)
	}

	// Step four: publish by moving the assembly member.
	total := strconv.Itoa(len(first) + len(second))
	resp, body = f.request(t, "MOVE", session+"/.file", nil, map[string]string{
		"Destination":     destination,
		"OC-Total-Length": total,
		"X-OC-Mtime":      "1650000000",
		"Overwrite":       "T",
	})
	if resp.StatusCode != 201 {
		t.Fatalf("the assembly answered %d, want 201\n%s", resp.StatusCode, body)
	}
	if resp.Header.Get("OC-FileId") == "" {
		t.Error("the assembly reported no file id, which one client treats as a failed upload")
	}
	if resp.Header.Get("OC-ETag") == "" {
		t.Error("the assembly reported no etag, which one client treats as a failed upload")
	}

	onDisk, err := os.ReadFile(filepath.Join(f.host, "large.bin"))
	if err != nil {
		t.Fatalf("reading the assembled file: %v", err)
	}
	if len(onDisk) != len(first)+len(second) {
		t.Fatalf("the assembled file is %d bytes, want %d", len(onDisk), len(first)+len(second))
	}
	if !bytes.Equal(onDisk[:len(first)], first) || !bytes.Equal(onDisk[len(first):], second) {
		t.Error("the parts were assembled out of order")
	}
	if info, serr := os.Stat(filepath.Join(f.host, "large.bin")); serr == nil {
		if got := info.ModTime().Unix(); got != 1650000000 {
			t.Errorf("the assembled file's time is %d", got)
		}
	}
}

// An assembly that declares more than arrived must not publish a truncated
// file: the client deletes its local copy the moment it is told the upload
// succeeded.
func TestAnIncompleteAssemblyIsRefused(t *testing.T) {
	t.Parallel()
	f := newNCFixture(t, []byte("seed"))

	session := f.base + "/remote.php/dav/uploads/" + f.login + "/transfer-2"
	destination := f.filePath("partial.bin")

	if resp, _ := f.request(t, "MKCOL", session, nil,
		map[string]string{"Destination": destination}); resp.StatusCode != 201 {
		t.Fatalf("creating the session answered %d", resp.StatusCode)
	}
	if resp, _ := f.request(t, "PUT", session+"/000001",
		bytes.NewReader(bytes.Repeat([]byte("a"), 100)),
		map[string]string{"Destination": destination}); resp.StatusCode != 201 {
		t.Fatalf("the chunk answered %d", resp.StatusCode)
	}

	resp, _ := f.request(t, "MOVE", session+"/.file", nil, map[string]string{
		"Destination":     destination,
		"OC-Total-Length": "1000",
	})
	if resp.StatusCode == 201 {
		t.Fatal("a truncated assembly was published as a success")
	}
	if _, err := os.Stat(filepath.Join(f.host, "partial.bin")); err == nil {
		t.Error("the truncated file was published")
	}
}

// A move is a rename in place or a relocation, and the status matters: one
// client treats anything other than 201 on a plain rename as a hard error.
func TestARenameAnswersCreated(t *testing.T) {
	t.Parallel()
	f := newNCFixture(t, []byte("hello"))

	resp, body := f.request(t, "MOVE", f.filePath("doc.bin"), nil,
		map[string]string{"Destination": f.filePath("renamed.bin")})
	if resp.StatusCode != 201 {
		t.Fatalf("answered %d, want 201\n%s", resp.StatusCode, body)
	}
	if _, err := os.Stat(filepath.Join(f.host, "renamed.bin")); err != nil {
		t.Errorf("the renamed file is not there: %v", err)
	}
}

// A move onto an existing name with overwrite switched off is refused with
// 412, which is the status one client maps onto its own "the target is in the
// way" result. Anything else surfaces to a person as a generic failure.
func TestAMoveOntoAnExistingNameCanBeRefused(t *testing.T) {
	t.Parallel()
	f := newNCFixture(t, []byte("hello"))
	writeHostFile(t, f.host, "occupied.bin", []byte("in the way"))

	resp, body := f.request(t, "MOVE", f.filePath("doc.bin"), nil, map[string]string{
		"Destination": f.filePath("occupied.bin"),
		"Overwrite":   "F",
	})
	if resp.StatusCode != 412 {
		t.Fatalf("answered %d, want 412\n%s", resp.StatusCode, body)
	}

	onDisk, err := os.ReadFile(filepath.Join(f.host, "occupied.bin"))
	if err != nil {
		t.Fatalf("reading the destination: %v", err)
	}
	if string(onDisk) != "in the way" {
		t.Errorf("the destination was overwritten anyway: %q", onDisk)
	}
}

// A delete answers 204 and nothing else: one client treats any other status
// except 404 as a hard error.
func TestADeleteAnswersNoContent(t *testing.T) {
	t.Parallel()
	f := newNCFixture(t, []byte("hello"))

	resp, body := f.request(t, "DELETE", f.filePath("doc.bin"), nil, nil)
	if resp.StatusCode != 204 {
		t.Fatalf("answered %d, want 204\n%s", resp.StatusCode, body)
	}
	if len(body) != 0 {
		t.Errorf("the response carried a body: %q", body)
	}
}

// A parent's validator changes when something below it changes, which is the
// whole of how a sync client decides to look inside a folder at all.
func TestAWriteChangesTheParentValidator(t *testing.T) {
	t.Parallel()
	f := newNCFixture(t, []byte("hello"))

	etagOf := func() string {
		_, listed := f.request(t, "PROPFIND", f.filePath(""), strings.NewReader(propfindBody),
			map[string]string{"Depth": "0", "Content-Type": "application/xml"})
		entry := entryOf(t, string(listed), "/remote.php/dav/files/"+f.login+"/"+f.share+"/")
		return propValue(t, entry, "d:getetag")
	}

	before := etagOf()
	if resp, _ := f.request(t, "PUT", f.filePath("added.txt"),
		strings.NewReader("new"), nil); resp.StatusCode != 201 {
		t.Fatalf("the upload answered %d", resp.StatusCode)
	}
	// The aggregate is recomputed lazily, so the listing that follows the
	// write is what triggers it; one retry covers the boundary without
	// making the test depend on timing.
	after := etagOf()
	if after == before {
		time.Sleep(50 * time.Millisecond)
		after = etagOf()
	}
	if after == before {
		t.Errorf("the folder's validator is still %q after a write inside it", before)
	}
}
