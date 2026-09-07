//go:build linux && compat_nc

package lifecycle_test

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/lifecycle"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/auth"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
)

// The chunked upload the sync client actually drives.
//
// The reference operation opens a collection under its uploads root, PROPFINDs
// it to learn what is already there, PUTs zero-padded six-digit members and
// publishes by moving ".file". It declares no total on the chunk PUTs and its
// trailing chunk is whatever the file length leaves over, so these tests are
// written with a short final chunk on purpose: that is the shape every real
// transfer ends in.

// uploadFixture serves an engine with one share and returns the base URL and
// the Authorization header a device-login credential produces.
func uploadFixture(t *testing.T) (base, credential string, client *http.Client) {
	t.Helper()
	e, err := lifecycle.Open(context.Background(), lifecycle.Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("opening the engine: %v", err)
	}
	t.Cleanup(func() {
		if cerr := e.Close(); cerr != nil {
			t.Errorf("closing the engine: %v", cerr)
		}
	})

	ctx := context.Background()
	if rerr := e.Core.RegisterShare(ctx, core.ShareDef{
		ID: 1, Name: "documents", Host: t.TempDir(), Policy: vfs.DefaultSharePolicy(),
	}); rerr != nil {
		t.Fatalf("registering the share: %v", rerr)
	}
	uid, aerr := e.Auth.CreateAdmin(ctx, "alice", "Alice", pwOf(loginPassword))
	if aerr != nil {
		t.Fatalf("creating the account: %v", aerr)
	}
	if gerr := e.Core.GrantEveryShare(ctx, uid); gerr != nil {
		t.Fatalf("granting: %v", gerr)
	}
	token, terr := e.Auth.CreateAppPassword(ctx, uid, "phone",
		auth.Scope{Perms: auth.SyncScopePerms}, 0)
	if terr != nil {
		t.Fatalf("minting the credential: %v", terr)
	}

	client = compatClient()
	t.Cleanup(client.CloseIdleConnections)
	return serveCompatEngine(t, e), "Basic " +
		base64.StdEncoding.EncodeToString([]byte("alice:"+token)), client
}

// davSend performs one request against the compat WebDAV surface and returns
// its status and body.
func davSend(
	t *testing.T, client *http.Client, credential, method, url string,
	body io.Reader, headers map[string]string,
) (int, string) {
	t.Helper()
	if body == nil {
		body = http.NoBody
	}
	req := newReq(t, method, url, body)
	req.Header.Set("Authorization", credential)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer closeRespBody(t, resp)
	return resp.StatusCode, string(readAllBody(t, resp.Body))
}

// A transfer whose last chunk is shorter than the server's chunk floor lands
// whole.
//
// The floor governs offset-addressed sessions, where the client picks the
// offsets. A name-ordered client picks names, declares no total, and its final
// chunk is a remainder, so the floor's last-chunk exemption could never fire
// and every real transfer was refused on its final piece with a 500.
func TestAChunkedTransferWithAShortFinalChunkLandsWhole(t *testing.T) {
	t.Parallel()
	base, credential, client := uploadFixture(t)

	const (
		bulk = 6 << 20
		tail = 4096
	)
	folder := base + "/remote.php/dav/uploads/alice/transfer-a"
	dest := base + "/remote.php/dav/files/alice/documents/movie.mp4"

	if code, body := davSend(t, client, credential, "MKCOL", folder, nil,
		map[string]string{"Destination": dest}); code != http.StatusCreated {
		t.Fatalf("opening the collection answered %d: %s", code, body)
	}
	for i, size := range []int{bulk, tail} {
		name := fmt.Sprintf("%06d", i+1)
		if code, body := davSend(t, client, credential, http.MethodPut, folder+"/"+name,
			strings.NewReader(strings.Repeat("x", size)),
			map[string]string{"Destination": dest}); code != http.StatusCreated {
			t.Fatalf("chunk %s answered %d: %s", name, code, body)
		}
	}
	if code, body := davSend(t, client, credential, "MOVE", folder+"/.file", nil,
		map[string]string{"Destination": dest, "X-OC-Mtime": "1700000000"}); code != http.StatusCreated {
		t.Fatalf("publishing answered %d: %s", code, body)
	}

	code, got := davSend(t, client, credential, http.MethodGet, dest, nil, nil)
	want := strings.Repeat("x", bulk+tail)
	if code != http.StatusOK {
		t.Fatalf("reading the file back answered %d", code)
	}
	if got != want {
		t.Errorf("the assembled file holds %d bytes, want %d", len(got), len(want))
	}
}

// Publishing a transfer that is missing a chunk is refused rather than
// published short.
//
// The client declares the assembled length when it opens the collection. A
// gap past the write head leaves the spool with nothing to drain, so assembly
// looked complete: the short file was published and answered 201, and the
// client deleted its local copy on the strength of that answer. The status is
// the client's own fault rather than 500, since it is the one still holding
// the missing bytes.
func TestPublishingATransferMissingAChunkIsRefused(t *testing.T) {
	t.Parallel()
	base, credential, client := uploadFixture(t)

	folder := base + "/remote.php/dav/uploads/alice/transfer-b"
	dest := base + "/remote.php/dav/files/alice/documents/truncated.bin"

	if code, body := davSend(t, client, credential, "MKCOL", folder, nil, map[string]string{
		"Destination": dest, "OC-Total-Length": "2048",
	}); code != http.StatusCreated {
		t.Fatalf("opening the collection answered %d: %s", code, body)
	}
	if code, body := davSend(t, client, credential, http.MethodPut, folder+"/000001",
		strings.NewReader(strings.Repeat("y", 1024)),
		map[string]string{"Destination": dest}); code != http.StatusCreated {
		t.Fatalf("the first chunk answered %d: %s", code, body)
	}

	code, body := davSend(t, client, credential, "MOVE", folder+"/.file", nil,
		map[string]string{"Destination": dest})
	if code != http.StatusBadRequest {
		t.Fatalf("publishing a short transfer answered %d: %s", code, body)
	}
	if code, _ := davSend(t, client, credential, http.MethodGet, dest, nil, nil); code != http.StatusNotFound {
		t.Errorf("a refused assembly left a file behind: reading it answered %d", code)
	}
}

// Reopening a collection resumes it instead of failing.
//
// The reference client resumes by re-running the whole operation, so it opens
// the same collection again, reads back what is there and sends the rest.
// Refusing the second open ended every resumed transfer before its first
// chunk, and a client that lost the response to the first open could never
// proceed at all.
func TestReopeningAnUploadCollectionResumesIt(t *testing.T) {
	t.Parallel()
	base, credential, client := uploadFixture(t)

	folder := base + "/remote.php/dav/uploads/alice/transfer-c"
	dest := base + "/remote.php/dav/files/alice/documents/resumed.bin"
	open := map[string]string{"Destination": dest}

	if code, body := davSend(t, client, credential, "MKCOL", folder, nil, open); code != http.StatusCreated {
		t.Fatalf("the first open answered %d: %s", code, body)
	}
	if code, body := davSend(t, client, credential, http.MethodPut, folder+"/000001",
		strings.NewReader("first-"), open); code != http.StatusCreated {
		t.Fatalf("the first chunk answered %d: %s", code, body)
	}
	if code, body := davSend(t, client, credential, "MKCOL", folder, nil, open); code != http.StatusCreated {
		t.Fatalf("reopening answered %d: %s", code, body)
	}

	// The chunk sent before the reopen is still there, and the listing reports
	// how large it is. The client resumes by summing the lengths it is shown
	// and sending from that offset under the next name, so a listing that
	// names the member without its length has it restart at byte zero while
	// numbering past what it already sent: the assembled file then repeats its
	// opening bytes. The length is the half that makes this a resume.
	code, listed := davSend(t, client, credential, "PROPFIND", folder, nil,
		map[string]string{"Depth": "1"})
	if code != http.StatusMultiStatus {
		t.Fatalf("listing the collection answered %d: %s", code, listed)
	}
	if !strings.Contains(listed, "<D:getcontentlength>6</D:getcontentlength>") {
		t.Errorf("the listing does not report the stored chunk's length: %s", listed)
	}

	if code, body := davSend(t, client, credential, http.MethodPut, folder+"/000002",
		strings.NewReader("second"), open); code != http.StatusCreated {
		t.Fatalf("the second chunk answered %d: %s", code, body)
	}
	if code, body := davSend(t, client, credential, "MOVE", folder+"/.file", nil,
		open); code != http.StatusCreated {
		t.Fatalf("publishing answered %d: %s", code, body)
	}
	if _, got := davSend(t, client, credential, http.MethodGet, dest, nil, nil); got != "first-second" {
		t.Errorf("the resumed transfer holds %q", got)
	}
}

// A collection reopened against a different destination starts over rather
// than adopting what the first transfer sent.
//
// The transfer id is the client's, and both reference clients derive it from
// the file rather than from where it is going: the Android operation uses the
// file's MD5, so the same picture sent to two folders collides on it. Refusing
// the second open answered 405, which that operation reports as "the folder
// already exists" and the transfer never begins. Reopening therefore succeeds,
// and the session behind it is replaced so one file's bytes cannot be
// published at the other's destination.
func TestReopeningAgainstADifferentDestinationStartsOver(t *testing.T) {
	t.Parallel()
	base, credential, client := uploadFixture(t)

	folder := base + "/remote.php/dav/uploads/alice/transfer-d"
	first := base + "/remote.php/dav/files/alice/documents/one.bin"
	second := base + "/remote.php/dav/files/alice/documents/two.bin"

	if code, body := davSend(t, client, credential, "MKCOL", folder, nil,
		map[string]string{"Destination": first}); code != http.StatusCreated {
		t.Fatalf("the first open answered %d: %s", code, body)
	}
	if code, body := davSend(t, client, credential, http.MethodPut, folder+"/000001",
		strings.NewReader("first-destination-bytes"),
		map[string]string{"Destination": first}); code != http.StatusCreated {
		t.Fatalf("the first chunk answered %d: %s", code, body)
	}

	if code, body := davSend(t, client, credential, "MKCOL", folder, nil,
		map[string]string{"Destination": second}); code != http.StatusCreated {
		t.Fatalf("reopening against a second destination answered %d: %s", code, body)
	}

	// Nothing the first transfer sent is left to publish, so an assembly with
	// no fresh chunk cannot land the first file's bytes on the second name.
	if code, _ := davSend(t, client, credential, "MOVE", folder+"/.file", nil,
		map[string]string{"Destination": second}); code == http.StatusCreated {
		t.Fatal("the retargeted collection published the first transfer's bytes")
	}
	if code, _ := davSend(t, client, credential, http.MethodGet, second, nil, nil); code != http.StatusNotFound {
		t.Errorf("the second destination exists after a refused assembly: %d", code)
	}

	// A fresh transfer under the reopened id lands its own bytes.
	if code, body := davSend(t, client, credential, http.MethodPut, folder+"/000001",
		strings.NewReader("second"), map[string]string{"Destination": second}); code != http.StatusCreated {
		t.Fatalf("the retargeted chunk answered %d: %s", code, body)
	}
	if code, body := davSend(t, client, credential, "MOVE", folder+"/.file", nil,
		map[string]string{"Destination": second}); code != http.StatusCreated {
		t.Fatalf("publishing the retargeted transfer answered %d: %s", code, body)
	}
	if _, got := davSend(t, client, credential, http.MethodGet, second, nil, nil); got != "second" {
		t.Errorf("the second destination holds %q", got)
	}
}

// An upload asking the server to make its parents lands, and one that does not
// ask is still refused.
//
// The reference iOS client sets X-NC-WebDAV-Auto-Mkcol on every upload and
// accepts only a 2xx, so a picture whose folder does not exist yet failed with
// nothing the person holding the phone could do about it. The header is the
// client asking for the folders it named to be created; without it a missing
// parent stays an error, since a PUT that silently invents directories is how
// a mistyped path becomes a tree nobody meant to make.
func TestAnUploadCanAskForItsParentsToBeCreated(t *testing.T) {
	t.Parallel()
	base, credential, client := uploadFixture(t)

	dest := base + "/remote.php/dav/files/alice/documents/Camera/2026/09/photo.jpg"

	if code, _ := davSend(t, client, credential, http.MethodPut, dest,
		strings.NewReader("jpeg"), nil); code != http.StatusNotFound {
		t.Fatalf("an upload into a missing folder answered %d, want 404", code)
	}

	if code, body := davSend(t, client, credential, http.MethodPut, dest,
		strings.NewReader("jpeg-bytes"),
		map[string]string{"X-NC-WebDAV-Auto-Mkcol": "1"}); code != http.StatusCreated {
		t.Fatalf("the upload answered %d: %s", code, body)
	}
	if _, got := davSend(t, client, credential, http.MethodGet, dest, nil, nil); got != "jpeg-bytes" {
		t.Errorf("the uploaded file holds %q", got)
	}

	// The folders it made are real collections, not just a path that happened
	// to resolve: a client lists them straight after uploading into them.
	ask := `<?xml version="1.0"?><d:propfind xmlns:d="DAV:"><d:prop><d:resourcetype/></d:prop></d:propfind>`
	code, listed := davSend(t, client, credential, "PROPFIND",
		base+"/remote.php/dav/files/alice/documents/Camera/2026", strings.NewReader(ask),
		map[string]string{"Depth": "0", "Content-Type": "text/xml"})
	if code != http.StatusMultiStatus {
		t.Fatalf("listing a created parent answered %d: %s", code, listed)
	}
	if !strings.Contains(listed, "collection") {
		t.Errorf("the created parent is not a collection: %s", listed)
	}
}

// A transfer id is reusable once the transfer it named has been published.
//
// The Android operation derives the id from the file's own MD5, so sending the
// same picture again, or sending it somewhere else afterwards, arrives under
// the id that just finished. The alias has to be released when the collection
// is published, or that second send opens a collection bound to a session that
// no longer exists and the transfer cannot proceed.
func TestATransferIDIsReusableAfterItsUploadIsPublished(t *testing.T) {
	t.Parallel()
	base, credential, client := uploadFixture(t)

	folder := base + "/remote.php/dav/uploads/alice/transfer-reused"
	first := base + "/remote.php/dav/files/alice/documents/copy-one.bin"
	second := base + "/remote.php/dav/files/alice/documents/copy-two.bin"

	publish := func(dest, body string) {
		t.Helper()
		opts := map[string]string{"Destination": dest}
		if code, out := davSend(t, client, credential, "MKCOL", folder, nil, opts); code != http.StatusCreated {
			t.Fatalf("opening for %s answered %d: %s", dest, code, out)
		}
		if code, out := davSend(t, client, credential, http.MethodPut, folder+"/000001",
			strings.NewReader(body), opts); code != http.StatusCreated {
			t.Fatalf("the chunk for %s answered %d: %s", dest, code, out)
		}
		if code, out := davSend(t, client, credential, "MOVE", folder+"/.file", nil, opts); code != http.StatusCreated {
			t.Fatalf("publishing %s answered %d: %s", dest, code, out)
		}
	}

	publish(first, "one")
	// The same id again, which is what the client sends for the same file.
	publish(second, "two")

	if _, got := davSend(t, client, credential, http.MethodGet, first, nil, nil); got != "one" {
		t.Errorf("the first upload holds %q", got)
	}
	if _, got := davSend(t, client, credential, http.MethodGet, second, nil, nil); got != "two" {
		t.Errorf("the reused id published %q, so the second transfer did not land its own bytes", got)
	}
}

// An assembly that received nothing is refused, unless the transfer said it
// was storing an empty file.
//
// Publishing on an empty spool answers 201 for a transfer that sent no bytes,
// and both reference clients read that as "uploaded" and drop their local
// copy: an assembly racing an abandoned or retargeted collection would destroy
// the file it was meant to store. A transfer that means to store an empty file
// names a length of zero, and the bytes it sent agree with it, so that one
// still lands.
func TestAnAssemblyThatReceivedNothingIsRefusedUnlessItSaidSo(t *testing.T) {
	t.Parallel()
	base, credential, client := uploadFixture(t)

	silent := base + "/remote.php/dav/uploads/alice/transfer-silent"
	lost := base + "/remote.php/dav/files/alice/documents/must-not-appear.bin"
	if code, body := davSend(t, client, credential, "MKCOL", silent, nil,
		map[string]string{"Destination": lost}); code != http.StatusCreated {
		t.Fatalf("opening answered %d: %s", code, body)
	}
	if code, _ := davSend(t, client, credential, "MOVE", silent+"/.file", nil,
		map[string]string{"Destination": lost}); code == http.StatusCreated {
		t.Error("a transfer that sent nothing published a file, which the client reads as a stored upload")
	}
	if code, _ := davSend(t, client, credential, http.MethodGet, lost, nil, nil); code != http.StatusNotFound {
		t.Errorf("the refused assembly left a file behind: %d", code)
	}

	// The same shape, with the transfer declaring that it holds nothing.
	declared := base + "/remote.php/dav/uploads/alice/transfer-declared-empty"
	empty := base + "/remote.php/dav/files/alice/documents/empty.txt"
	opts := map[string]string{"Destination": empty, "OC-Total-Length": "0"}
	if code, body := davSend(t, client, credential, "MKCOL", declared, nil, opts); code != http.StatusCreated {
		t.Fatalf("opening the declared-empty transfer answered %d: %s", code, body)
	}
	if code, body := davSend(t, client, credential, "MOVE", declared+"/.file", nil, opts); code != http.StatusCreated {
		t.Fatalf("a declared-empty transfer answered %d: %s", code, body)
	}
	if code, got := davSend(t, client, credential, http.MethodGet, empty, nil, nil); code != http.StatusOK || got != "" {
		t.Errorf("the empty file answered %d holding %q", code, got)
	}
}

// Creating a folder names it in the header the client stores as its remote id.
//
// The reference operation reads OC-FileId from its own MKCOL response and
// keeps it as the folder's identity; without the header the folder it just
// created has none until something lists it again. The value has to be the
// same identity a PROPFIND reports, since two spellings of one folder make a
// client treat the one it created as a different folder the moment it syncs.
func TestCreatingAFolderReportsTheIdAListingWouldGive(t *testing.T) {
	t.Parallel()
	base, credential, client := uploadFixture(t)

	folder := base + "/remote.php/dav/files/alice/documents/Album"
	req := newReq(t, "MKCOL", folder, http.NoBody)
	req.Header.Set("Authorization", credential)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("MKCOL: %v", err)
	}
	defer closeRespBody(t, resp)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("creating the folder answered %d", resp.StatusCode)
	}

	created := resp.Header.Get("OC-FileId")
	if created == "" {
		t.Fatal("the create reported no file id, so the client has no remote id for the folder")
	}

	// What a listing says about the same folder.
	listed := `<?xml version="1.0"?><d:propfind xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns"><d:prop><oc:id/></d:prop></d:propfind>`
	code, body := davSend(t, client, credential, "PROPFIND", folder, strings.NewReader(listed),
		map[string]string{"Depth": "0", "Content-Type": "text/xml"})
	if code != http.StatusMultiStatus {
		t.Fatalf("listing the folder answered %d: %s", code, body)
	}
	if !strings.Contains(body, ">"+created+"<") {
		t.Errorf("the create reported id %q, which the listing does not repeat: %s", created, body)
	}
}

// Publishing a chunked transfer reports the identity of the file it created.
//
// The reference sync client reads this off the assembly reply and abandons the
// whole upload when it is absent, so the bytes land and the transfer is still
// reported as failed. The value has to be the identity a listing gives, since
// the client keys its journal on it and a second spelling makes the file it
// just uploaded look like a different one on the next sync.
func TestPublishingATransferReportsTheIdAListingWouldGive(t *testing.T) {
	t.Parallel()
	base, credential, client := uploadFixture(t)

	folder := base + "/remote.php/dav/uploads/alice/transfer-id"
	dest := base + "/remote.php/dav/files/alice/documents/clip.mp4"
	open := map[string]string{"Destination": dest}

	if code, body := davSend(t, client, credential, "MKCOL", folder, nil, open); code != http.StatusCreated {
		t.Fatalf("opening the collection answered %d: %s", code, body)
	}
	if code, body := davSend(t, client, credential, http.MethodPut, folder+"/000001",
		strings.NewReader("chunked-bytes"), open); code != http.StatusCreated {
		t.Fatalf("the chunk answered %d: %s", code, body)
	}

	req := newReq(t, "MOVE", folder+"/.file", http.NoBody)
	req.Header.Set("Authorization", credential)
	req.Header.Set("Destination", dest)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("MOVE: %v", err)
	}
	defer closeRespBody(t, resp)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("publishing answered %d", resp.StatusCode)
	}

	published := resp.Header.Get("OC-FileId")
	if published == "" {
		t.Fatal("publishing reported no file id, so the client abandons an upload whose bytes landed")
	}

	listed := `<?xml version="1.0"?><d:propfind xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns"><d:prop><oc:id/></d:prop></d:propfind>`
	code, body := davSend(t, client, credential, "PROPFIND", dest, strings.NewReader(listed),
		map[string]string{"Depth": "0", "Content-Type": "text/xml"})
	if code != http.StatusMultiStatus {
		t.Fatalf("listing the file answered %d: %s", code, body)
	}
	if !strings.Contains(body, ">"+published+"<") {
		t.Errorf("publishing reported id %q, which the listing does not repeat: %s", published, body)
	}
}
