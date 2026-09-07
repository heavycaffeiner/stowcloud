//go:build linux && compat_nc

package lifecycle_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/lifecycle"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/auth"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
)

// The recent listing is one list, whichever client asks for it.
//
// Three surfaces answer it from one journal: the interface's own route, the
// OCS route the app's files screen reads, and the DAV search its recent screen
// actually sends. They each had their own window and row count, so the same
// account saw a different list depending on which client it opened, which is
// the thing "recent" is least able to survive being wrong about.

// recentFixture writes n files through the compat surface, newest last, and
// returns the base URL with a credential for both surfaces.
func recentFixture(t *testing.T, n int) (base, basic string, client *http.Client) {
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
		ID: 1, Name: "files", Host: t.TempDir(), Policy: vfs.DefaultSharePolicy(),
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

	base = serveCompatEngine(t, e)
	client = compatClient()
	t.Cleanup(client.CloseIdleConnections)
	basic = "Basic " + base64.StdEncoding.EncodeToString([]byte("alice:"+token))

	// Written through the server, because the journal is what a recent
	// listing reads and only this server's own writes are in it.
	for i := range n {
		name := fmt.Sprintf("note-%02d.txt", i)
		code, body := davSend(t, client, basic, http.MethodPut,
			base+"/remote.php/dav/files/alice/files/"+name,
			strings.NewReader("x"), nil)
		if code != http.StatusCreated {
			t.Fatalf("writing %s answered %d: %s", name, code, body)
		}
	}
	return base, basic, client
}

// The OCS listing and the DAV search report the same number of files.
//
// The app's files screen reads the first and its recent screen sends the
// second. One took thirty rows and the other fifty, so the same account saw
// two different lists inside one client.
func TestBothCompatRecentSurfacesAgree(t *testing.T) {
	t.Parallel()
	const written = 40
	base, basic, client := recentFixture(t, written)

	code, body := davSend(t, client, basic, http.MethodGet,
		base+"/ocs/v2.php/apps/files/api/v1/recent?format=json", nil,
		map[string]string{"OCS-APIRequest": "true"})
	if code != http.StatusOK {
		t.Fatalf("the OCS listing answered %d: %s", code, body)
	}
	var ocs struct {
		OCS struct {
			Data struct {
				Entries []json.RawMessage `json:"entries"`
			} `json:"data"`
		} `json:"ocs"`
	}
	if err := json.Unmarshal([]byte(body), &ocs); err != nil {
		t.Fatalf("decoding the OCS listing: %v", err)
	}

	// The search the app's recent screen sends: a modification-time filter
	// with no explicit row count.
	search := `<?xml version="1.0" encoding="utf-8"?>
<d:searchrequest xmlns:d="DAV:" xmlns:oc="http://nextcloud.com/ns">
  <d:basicsearch>
    <d:select><d:prop><d:getetag/></d:prop></d:select>
    <d:from><d:scope><d:href>/files/alice</d:href><d:depth>infinity</d:depth></d:scope></d:from>
    <d:where><d:gt><d:prop><d:getlastmodified/></d:prop>
      <d:literal>2020-01-01T00:00:00Z</d:literal></d:gt></d:where>
  </d:basicsearch>
</d:searchrequest>`
	code, davBody := davSend(t, client, basic, "SEARCH", base+"/remote.php/dav",
		strings.NewReader(search), map[string]string{"Content-Type": "text/xml"})
	if code != http.StatusMultiStatus {
		t.Fatalf("the DAV search answered %d: %s", code, davBody)
	}
	davCount := strings.Count(davBody, "<D:response>")

	if len(ocs.OCS.Data.Entries) != davCount {
		t.Errorf("the OCS listing has %d entries and the DAV search %d; one account, two lists",
			len(ocs.OCS.Data.Entries), davCount)
	}
	if davCount != written {
		t.Errorf("the listings hold %d of %d files written", davCount, written)
	}
}

// A search naming a row count gets that many.
//
// The reference client sends its limit as DAV:nresults, and it was dropped:
// a recent screen asking for a hundred rows got whatever the server chose.
func TestACompatSearchHonoursTheRequestedRowCount(t *testing.T) {
	t.Parallel()
	base, basic, client := recentFixture(t, 12)

	search := func(limit int) string {
		return fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>
<d:searchrequest xmlns:d="DAV:" xmlns:oc="http://nextcloud.com/ns">
  <d:basicsearch>
    <d:select><d:prop><d:getetag/></d:prop></d:select>
    <d:from><d:scope><d:href>/files/alice</d:href><d:depth>infinity</d:depth></d:scope></d:from>
    <d:where><d:gt><d:prop><d:getlastmodified/></d:prop>
      <d:literal>2020-01-01T00:00:00Z</d:literal></d:gt></d:where>
    <d:limit><d:nresults>%d</d:nresults></d:limit>
  </d:basicsearch>
</d:searchrequest>`, limit)
	}

	code, body := davSend(t, client, basic, "SEARCH", base+"/remote.php/dav",
		strings.NewReader(search(5)), map[string]string{"Content-Type": "text/xml"})
	if code != http.StatusMultiStatus {
		t.Fatalf("the search answered %d: %s", code, body)
	}
	if got := strings.Count(body, "<D:response>"); got > 5 {
		t.Errorf("a search asking for 5 rows answered %d", got)
	}
}
