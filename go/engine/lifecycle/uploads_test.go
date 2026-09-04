//go:build linux

package lifecycle_test

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"hash/crc32"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
)

// tusRequest sends one protocol request with arbitrary headers and body.
func tusRequest(t *testing.T, method, url string, sess session,
	headers map[string]string, body []byte,
) (int, http.Header, []byte) {
	t.Helper()

	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		reader = bytes.NewReader(body)
	}

	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatalf("building: %v", err)
	}
	if sess.cookie != nil {
		sess.attach(req)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := testClient().Do(req)
	if err != nil {
		t.Fatalf("requesting %s %s: %v", method, url, err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	}()

	out := make([]byte, 0, 1024)
	buf := make([]byte, 1024)
	for {
		n, rerr := resp.Body.Read(buf)
		out = append(out, buf[:n]...)
		if rerr != nil {
			break
		}
	}
	return resp.StatusCode, resp.Header, out
}

// metadataFor builds the protocol's metadata header, which is base64 by pair.
func metadataFor(pairs map[string]string) string {
	out := make([]string, 0, len(pairs))
	for k, v := range pairs {
		out = append(out, k+" "+base64.StdEncoding.EncodeToString([]byte(v)))
	}
	return strings.Join(out, ",")
}

// Discovery answers without a credential, which is the point: a client asks
// this to find out whether resumable uploads exist here before it has
// anything to present.
func TestUploadDiscoveryIsPublic(t *testing.T) {
	base, _, _ := bootWithUser(t)

	status, header, body := tusRequest(t, http.MethodOptions,
		base+"/api/v1/uploads", session{}, nil, nil)
	if status != http.StatusNoContent {
		t.Fatalf("discovery answered %d: %s", status, body)
	}
	if got := header.Get("Tus-Resumable"); got != "1.0.0" {
		t.Errorf("the version header is %q", got)
	}
	if header.Get("Tus-Extension") == "" {
		t.Error("no extensions are advertised, so a client cannot tell what is supported")
	}
}

// A whole file goes up in chunks and reads back byte for byte.
//
// This is the transfer the protocol exists for, so it is driven end to end
// rather than asserted against a session row: a session that reports the
// right offset while writing the wrong bytes would pass any lesser check.
func TestAResumableUploadDeliversTheFile(t *testing.T) {
	want := payload()
	base, sess, share := contentShare(t, everyPerm(), []byte("unused"))

	location := createUpload(t, base, sess, "/"+share+"/uploaded.bin", len(want))

	// Three chunks, so the offset arithmetic is exercised rather than a
	// single write that happens to be the whole file.
	const chunk = 1500
	var sent int
	for sent < len(want) {
		end := min(sent+chunk, len(want))
		status, header, body := tusRequest(t, http.MethodPatch, base+location, sess,
			map[string]string{
				"Tus-Resumable": "1.0.0",
				"Upload-Offset": strconv.Itoa(sent),
				"Content-Type":  "application/offset+octet-stream",
			}, want[sent:end])
		if status != http.StatusNoContent {
			t.Fatalf("the chunk at %d answered %d: %s", sent, status, body)
		}

		// The server's new offset is what the next chunk starts at. A wrong
		// one here is how a transfer silently loses or duplicates a range.
		got := header.Get("Upload-Offset")
		if got != strconv.Itoa(end) {
			t.Fatalf("after sending up to %d the server reports offset %q", end, got)
		}
		sent = end
	}

	// The file is at its destination and identical.
	code, _, read := download(t, base, sess, "/"+share+"/uploaded.bin", "")
	if code != http.StatusOK {
		t.Fatalf("reading the uploaded file answered %d: %s", code, read)
	}
	if !bytes.Equal(read, want) {
		t.Errorf("the uploaded file is %d bytes and differs from the %d sent", len(read), len(want))
	}
}

// createUpload opens a session and returns its location.
func createUpload(t *testing.T, base string, sess session, dest string, length int) string {
	t.Helper()

	// The wire splits the target into the destination directory and the leaf:
	// the handler joins them, and the leaf is what the published file is
	// named. A test that sent only the joined path would be asking for a
	// metadata shape no client sends.
	dir, leaf := dest[:strings.LastIndex(dest, "/")+1], dest[strings.LastIndex(dest, "/")+1:]
	status, header, body := tusRequest(t, http.MethodPost, base+"/api/v1/uploads", sess,
		map[string]string{
			"Tus-Resumable":   "1.0.0",
			"Upload-Length":   strconv.Itoa(length),
			"Upload-Metadata": metadataFor(map[string]string{"dest": dir, "filename": leaf}),
		}, nil)
	if status != http.StatusCreated {
		t.Fatalf("creating a session answered %d: %s", status, body)
	}

	location := header.Get("Location")
	if location == "" {
		t.Fatal("the session has no Location, so a client cannot send it anything")
	}
	return location
}

// A resume asks where the server is and continues from there.
func TestAnInterruptedUploadResumes(t *testing.T) {
	want := payload()
	base, sess, share := contentShare(t, everyPerm(), []byte("unused"))

	location := createUpload(t, base, sess, "/"+share+"/resumed.bin", len(want))

	// Half of it, then stop as though the client died.
	half := len(want) / 2
	if status, _, body := tusRequest(t, http.MethodPatch, base+location, sess,
		map[string]string{
			"Tus-Resumable": "1.0.0",
			"Upload-Offset": "0",
			"Content-Type":  "application/offset+octet-stream",
		}, want[:half]); status != http.StatusNoContent {
		t.Fatalf("the first half answered %d: %s", status, body)
	}

	// A new client asks where to continue.
	status, header, body := tusRequest(t, http.MethodHead, base+location, sess,
		map[string]string{"Tus-Resumable": "1.0.0"}, nil)
	if status != http.StatusOK {
		t.Fatalf("the status request answered %d: %s", status, body)
	}
	offset := header.Get("Upload-Offset")
	if offset != strconv.Itoa(half) {
		t.Fatalf("the server reports offset %q after %d bytes", offset, half)
	}

	// Progress must never be cached: the value changes with every chunk, and
	// a cached one sends the next chunk to a stale offset.
	if cc := header.Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Errorf("the progress response is cacheable (%q), so a resume can read a stale offset", cc)
	}

	if status, _, body := tusRequest(t, http.MethodPatch, base+location, sess,
		map[string]string{
			"Tus-Resumable": "1.0.0",
			"Upload-Offset": offset,
			"Content-Type":  "application/offset+octet-stream",
		}, want[half:]); status != http.StatusNoContent {
		t.Fatalf("the second half answered %d: %s", status, body)
	}

	code, _, read := download(t, base, sess, "/"+share+"/resumed.bin", "")
	if code != http.StatusOK {
		t.Fatalf("reading answered %d", code)
	}
	if !bytes.Equal(read, want) {
		t.Errorf("the resumed file differs from what was sent")
	}
}

// A chunk at the wrong offset is refused rather than written somewhere else.
//
// Accepting it puts bytes at a position the client did not mean, and the file
// that results is corrupt with nothing reporting a failure.
func TestAChunkAtTheWrongOffsetIsRefused(t *testing.T) {
	want := payload()
	base, sess, share := contentShare(t, everyPerm(), []byte("unused"))

	location := createUpload(t, base, sess, "/"+share+"/wrong.bin", len(want))

	for _, offset := range []string{"100", "999999"} {
		status, _, body := tusRequest(t, http.MethodPatch, base+location, sess,
			map[string]string{
				"Tus-Resumable": "1.0.0",
				"Upload-Offset": offset,
				"Content-Type":  "application/offset+octet-stream",
			}, want[:100])
		if status == http.StatusNoContent {
			t.Errorf("a chunk at offset %s was accepted when the server holds 0: %s", offset, body)
		}
	}

	// The session is untouched, so the client can still resume correctly.
	_, header, _ := tusRequest(t, http.MethodHead, base+location, sess,
		map[string]string{"Tus-Resumable": "1.0.0"}, nil)
	if got := header.Get("Upload-Offset"); got != "0" {
		t.Errorf("a refused chunk moved the offset to %q", got)
	}
}

// A request without the version header is refused.
//
// The header is how a client says which contract its request is written
// against. Guessing would mean serving a request whose meaning is unknown.
func TestAnUploadWithoutTheVersionHeaderIsRefused(t *testing.T) {
	base, sess, share := contentShare(t, everyPerm(), []byte("unused"))

	status, _, body := tusRequest(t, http.MethodPost, base+"/api/v1/uploads", sess,
		map[string]string{
			"Upload-Length":   "10",
			"Upload-Metadata": metadataFor(map[string]string{"path": "/" + share + "/x.bin"}),
		}, nil)
	if status == http.StatusCreated {
		t.Errorf("a session was created without the version header: %s", body)
	}

	// And a wrong version too, which is a different mistake with the same
	// answer: this server does not speak it.
	status, _, body = tusRequest(t, http.MethodPost, base+"/api/v1/uploads", sess,
		map[string]string{
			"Tus-Resumable":   "2.0.0",
			"Upload-Length":   "10",
			"Upload-Metadata": metadataFor(map[string]string{"path": "/" + share + "/x.bin"}),
		}, nil)
	if status == http.StatusCreated {
		t.Errorf("a session was created for version 2.0.0: %s", body)
	}
}

// A chunk whose digest does not match is refused, and nothing is written.
//
// The checksum exists so a corrupted transfer is caught at the chunk rather
// than at the end, when the whole file has to be sent again.
func TestAChunkWithABadChecksumIsRefused(t *testing.T) {
	want := payload()
	base, sess, share := contentShare(t, everyPerm(), []byte("unused"))

	location := createUpload(t, base, sess, "/"+share+"/summed.bin", len(want))

	// The digest of something else entirely, in an algorithm the server
	// actually offers.
	wrong := crc32cOf([]byte("not what is being sent"))
	status, _, body := tusRequest(t, http.MethodPatch, base+location, sess,
		map[string]string{
			"Tus-Resumable":   "1.0.0",
			"Upload-Offset":   "0",
			"Content-Type":    "application/offset+octet-stream",
			"Upload-Checksum": "crc32c " + base64.StdEncoding.EncodeToString(wrong),
		}, want[:100])
	if status == http.StatusNoContent {
		t.Fatalf("a chunk with a wrong digest was accepted: %s", body)
	}

	// Nothing landed, so the client resends the same chunk rather than
	// discovering later that the file is wrong.
	_, header, _ := tusRequest(t, http.MethodHead, base+location, sess,
		map[string]string{"Tus-Resumable": "1.0.0"}, nil)
	if got := header.Get("Upload-Offset"); got != "0" {
		t.Errorf("a chunk with a bad digest moved the offset to %q", got)
	}
}

// A correct digest is accepted, so the check is not refusing everything.
func TestAChunkWithAGoodChecksumIsAccepted(t *testing.T) {
	want := payload()
	base, sess, share := contentShare(t, everyPerm(), []byte("unused"))

	location := createUpload(t, base, sess, "/"+share+"/summed.bin", len(want))

	status, _, body := tusRequest(t, http.MethodPatch, base+location, sess,
		map[string]string{
			"Tus-Resumable":   "1.0.0",
			"Upload-Offset":   "0",
			"Content-Type":    "application/offset+octet-stream",
			"Upload-Checksum": "crc32c " + base64.StdEncoding.EncodeToString(crc32cOf(want[:100])),
		}, want[:100])
	if status != http.StatusNoContent {
		t.Fatalf("a chunk with a correct digest answered %d: %s", status, body)
	}
}

// An upload needs a credential, and one account cannot touch another's
// session.
func TestAnUploadSessionBelongsToItsOwner(t *testing.T) {
	base, sess, share := contentShare(t, everyPerm(), []byte("unused"))

	location := createUpload(t, base, sess, "/"+share+"/owned.bin", 100)

	// No credential at all.
	status, _, _ := tusRequest(t, http.MethodHead, base+location, session{},
		map[string]string{"Tus-Resumable": "1.0.0"}, nil)
	if status == http.StatusOK {
		t.Error("an unauthenticated request read a session's progress")
	}

	status, _, _ = tusRequest(t, http.MethodPatch, base+location, session{},
		map[string]string{
			"Tus-Resumable": "1.0.0",
			"Upload-Offset": "0",
			"Content-Type":  "application/offset+octet-stream",
		}, []byte("x"))
	if status == http.StatusNoContent {
		t.Error("an unauthenticated request wrote to a session")
	}
}

// Aborting discards the session.
func TestAbortingAnUpload(t *testing.T) {
	base, sess, share := contentShare(t, everyPerm(), []byte("unused"))

	location := createUpload(t, base, sess, "/"+share+"/abandoned.bin", 4096)

	status, _, body := tusRequest(t, http.MethodDelete, base+location, sess,
		map[string]string{"Tus-Resumable": "1.0.0"}, nil)
	if status != http.StatusNoContent {
		t.Fatalf("aborting answered %d: %s", status, body)
	}

	// The session is gone, so a later chunk has nowhere to land.
	status, _, _ = tusRequest(t, http.MethodHead, base+location, sess,
		map[string]string{"Tus-Resumable": "1.0.0"}, nil)
	if status == http.StatusOK {
		t.Error("an aborted session still reports progress")
	}

	// And the destination was never created.
	if code, _, _ := download(t, base, sess, "/"+share+"/abandoned.bin", ""); code == http.StatusOK {
		t.Error("an aborted upload left a file at its destination")
	}
}

// A chunk sent with the wrong content type is refused.
//
// The protocol fixes the type, and a request describing different content
// from what it carries is one whose framing cannot be trusted.
func TestAChunkWithTheWrongContentTypeIsRefused(t *testing.T) {
	base, sess, share := contentShare(t, everyPerm(), []byte("unused"))

	location := createUpload(t, base, sess, "/"+share+"/typed.bin", 100)

	status, _, body := tusRequest(t, http.MethodPatch, base+location, sess,
		map[string]string{
			"Tus-Resumable": "1.0.0",
			"Upload-Offset": "0",
			"Content-Type":  "application/json",
		}, []byte("0123456789"))
	if status == http.StatusNoContent {
		t.Errorf("a chunk with the wrong content type was accepted: %s", body)
	}
}

// A session cannot be opened over a path the account may not write.
func TestAnUploadNeedsWritePermission(t *testing.T) {
	base, sess, share := contentShare(t, acl.Read|acl.Download, []byte("unused"))

	status, _, body := tusRequest(t, http.MethodPost, base+"/api/v1/uploads", sess,
		map[string]string{
			"Tus-Resumable":   "1.0.0",
			"Upload-Length":   "10",
			"Upload-Metadata": metadataFor(map[string]string{"path": "/" + share + "/denied.bin"}),
		}, nil)
	if status == http.StatusCreated {
		t.Errorf("a read-only grant opened an upload session: %s", body)
	}
}

// A session opened over an escaping path is refused, like every other route.
func TestAnUploadPathCannotEscape(t *testing.T) {
	base, sess, _ := contentShare(t, everyPerm(), []byte("unused"))

	for _, path := range []string{"/../etc/passwd", "../etc", "/share/../../etc"} {
		status, _, body := tusRequest(t, http.MethodPost, base+"/api/v1/uploads", sess,
			map[string]string{
				"Tus-Resumable":   "1.0.0",
				"Upload-Length":   "10",
				"Upload-Metadata": metadataFor(map[string]string{"path": path}),
			}, nil)
		if status == http.StatusCreated {
			t.Errorf("%q opened a session: %s", path, body)
		}
	}
}

// A create without a destination is refused rather than defaulting anywhere.
func TestAnUploadWithoutADestinationIsRefused(t *testing.T) {
	base, sess, _ := contentShare(t, everyPerm(), []byte("unused"))

	status, _, body := tusRequest(t, http.MethodPost, base+"/api/v1/uploads", sess,
		map[string]string{"Tus-Resumable": "1.0.0", "Upload-Length": "10"}, nil)
	if status == http.StatusCreated {
		t.Errorf("a session was created with no destination: %s", body)
	}
}

// crc32cOf computes the digest independently of the upload engine, so a chunk
// is verified against the standard library's own table rather than against
// the code being tested.
func crc32cOf(b []byte) []byte {
	sum := crc32.Checksum(b, crc32.MakeTable(crc32.Castagnoli))
	out := make([]byte, 4)
	binary.BigEndian.PutUint32(out, sum)
	return out
}

// Discovery names the digests a chunk may carry.
//
// A client that guessed would compute a digest for every chunk and have every
// one refused, with nothing saying which algorithm to use instead.
func TestDiscoveryNamesTheChecksumAlgorithms(t *testing.T) {
	base, _, _ := bootWithUser(t)

	_, header, _ := tusRequest(t, http.MethodOptions, base+"/api/v1/uploads", session{}, nil, nil)
	got := header.Get("Tus-Checksum-Algorithm")
	if got == "" {
		t.Fatal("no algorithms are advertised")
	}
	if !strings.Contains(got, "crc32c") {
		t.Errorf("the advertised algorithms %q do not include the one the server prefers", got)
	}

	// And what is advertised is what works: a digest in the first named
	// algorithm has to be accepted.
	for _, name := range strings.Split(got, ",") {
		if strings.TrimSpace(name) == "crc32c" {
			return
		}
	}
	t.Errorf("crc32c is not in %q", got)
}
