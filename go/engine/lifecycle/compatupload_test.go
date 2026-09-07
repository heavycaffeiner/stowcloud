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

// A collection name the account already holds against a different destination
// is refused rather than adopted.
//
// The transfer id is chosen by the client, so two files can collide on it.
// Adopting the existing session would publish one file's bytes at the other's
// destination.
func TestReopeningAgainstADifferentDestinationIsRefused(t *testing.T) {
	t.Parallel()
	base, credential, client := uploadFixture(t)

	folder := base + "/remote.php/dav/uploads/alice/transfer-d"
	first := base + "/remote.php/dav/files/alice/documents/one.bin"
	second := base + "/remote.php/dav/files/alice/documents/two.bin"

	if code, body := davSend(t, client, credential, "MKCOL", folder, nil,
		map[string]string{"Destination": first}); code != http.StatusCreated {
		t.Fatalf("the first open answered %d: %s", code, body)
	}
	code, body := davSend(t, client, credential, "MKCOL", folder, nil,
		map[string]string{"Destination": second})
	if code == http.StatusCreated {
		t.Fatalf("a collection bound to %s was reopened against %s", first, second)
	}
	if code == http.StatusInternalServerError {
		t.Errorf("the collision answered 500, which reads as a broken server: %s", body)
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
