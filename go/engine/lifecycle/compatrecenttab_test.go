//go:build linux && compat_nc

package lifecycle_test

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/clock"
	"github.com/heavycaffeiner/stowcloud/go/engine/lifecycle"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/auth"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
)

// The app's Recent Files tab reads the window from its lower bound.
//
// That screen asks for a range: everything modified between a start and an end.
// The client sends both as bare epoch seconds under the same property, and the
// only thing separating them is the comparison each sits inside, DAV:gt opening
// the range and DAV:lt closing it. The upper bound is emitted first, so reading
// whichever literal arrived first took the end of the range as the start, and
// the answer became "modified since now": the tab showed almost nothing while
// the interface showed the real list off the same journal.
func TestTheRecentTabReadsItsWindowFromTheLowerBound(t *testing.T) {
	t.Parallel()
	e, openErr := lifecycle.Open(context.Background(), lifecycle.Options{DataDir: t.TempDir()})
	if openErr != nil {
		t.Fatalf("opening the engine: %v", openErr)
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

	base := serveCompatEngine(t, e)
	client := compatClient()
	t.Cleanup(client.CloseIdleConnections)
	basic := "Basic " + base64.StdEncoding.EncodeToString([]byte("alice:"+token))

	const written = 5
	for i := range written {
		name := fmt.Sprintf("note-%d.txt", i)
		if code, body := davSend(t, client, basic, http.MethodPut,
			base+"/remote.php/dav/files/alice/files/"+name,
			strings.NewReader("x"), nil); code != http.StatusCreated {
			t.Fatalf("writing %s answered %d: %s", name, code, body)
		}
	}

	// The body the tab sends: an AND whose DAV:lt carries the end of the range
	// and whose DAV:gt carries the start, the upper bound written first.
	//
	// The end is a second past the writes rather than the current instant,
	// because Unix() truncates: an end taken as the window's start would then
	// sit fractionally before the files and still admit them, and the test
	// would pass against the very defect it is written for.
	now := clock.System().Now().Unix() + 1
	start := now - 14*24*60*60
	body := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<d:searchrequest xmlns:d="DAV:" xmlns:oc="http://nextcloud.com/ns">
 <d:basicsearch>
  <d:select><d:prop><d:getetag/></d:prop></d:select>
  <d:from><d:scope><d:href>/files/alice</d:href><d:depth>infinity</d:depth></d:scope></d:from>
  <d:where>
   <d:and>
    <d:lt><d:prop><d:getlastmodified/></d:prop><d:literal>%d</d:literal></d:lt>
    <d:gt><d:prop><d:getlastmodified/></d:prop><d:literal>%d</d:literal></d:gt>
   </d:and>
  </d:where>
  <d:limit><d:nresults>100</d:nresults></d:limit>
 </d:basicsearch>
</d:searchrequest>`, now, start)

	// Addressed at the share rather than the virtual root: the root has its own
	// handler that queries every share in turn, which never reaches the filter
	// this test is about.
	code, out := davSend(t, client, basic, "SEARCH", base+"/remote.php/dav/files/alice",
		strings.NewReader(body), map[string]string{"Content-Type": "text/xml"})
	if code != http.StatusMultiStatus {
		t.Fatalf("the search answered %d: %s", code, out)
	}

	// Every file is inside a fortnight-wide window that ends now. Taking the
	// upper bound as the start answers with none of them.
	if got := strings.Count(out, "<D:response>"); got != written {
		t.Errorf("the range search reported %d files, want %d: %s", got, written, out)
	}

	// The discriminating half: the upper bound alone. Read as the start of the
	// window it means "written after now", which excludes every file just
	// written. Ignored, as an upper bound must be, the window falls back to
	// the default and the files are all still there.
	upperOnly := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<d:searchrequest xmlns:d="DAV:" xmlns:oc="http://nextcloud.com/ns">
 <d:basicsearch>
  <d:select><d:prop><d:getetag/></d:prop></d:select>
  <d:from><d:scope><d:href>/files/alice</d:href><d:depth>infinity</d:depth></d:scope></d:from>
  <d:where>
   <d:lt><d:prop><d:getlastmodified/></d:prop><d:literal>%d</d:literal></d:lt>
  </d:where>
 </d:basicsearch>
</d:searchrequest>`, now)

	code, out = davSend(t, client, basic, "SEARCH", base+"/remote.php/dav/files/alice",
		strings.NewReader(upperOnly), map[string]string{"Content-Type": "text/xml"})
	if code != http.StatusMultiStatus {
		t.Fatalf("the upper-bound search answered %d: %s", code, out)
	}
	if got := strings.Count(out, "<D:response>"); got != written {
		t.Errorf("an upper bound was read as the start of the window: %d files, want %d", got, written)
	}
}
