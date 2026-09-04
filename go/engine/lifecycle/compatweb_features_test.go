//go:build linux && compat_nc

package lifecycle_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/lifecycle"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/auth"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
)

func closeRespBody(t *testing.T, resp *http.Response) {
	t.Helper()
	if resp != nil && resp.Body != nil {
		if err := resp.Body.Close(); err != nil {
			t.Errorf("closing response body: %v", err)
		}
	}
}

func readAllBody(t *testing.T, r io.Reader) []byte {
	t.Helper()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("reading response body: %v", err)
	}
	return b
}

func newReq(t *testing.T, method, target string, body io.Reader) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, target, body)
	if err != nil {
		t.Fatalf("new request %s %s: %v", method, target, err)
	}
	return req
}

func TestCompatPrefixesAndIndexPhp(t *testing.T) {
	t.Parallel()
	base := serveCompat(t)

	for _, p := range []string{"/status.php", "/index.php/status.php"} {
		status, body := getOCSJSON(t, base+p)
		if status != http.StatusOK {
			t.Errorf("%s status = %d, want 200", p, status)
		}
		if v, ok := body["version"].(string); !ok || v == "" {
			t.Errorf("%s missing version", p)
		}
	}

	for _, p := range []string{"/204", "/index.php/204"} {
		resp, err := compatClient().Get(base + p)
		if err != nil {
			t.Fatalf("GET %s: %v", p, err)
		}
		closeRespBody(t, resp)
		if resp.StatusCode != http.StatusNoContent {
			t.Errorf("%s status = %d, want 204", p, resp.StatusCode)
		}
	}

	status, body := getOCSJSON(t, base+"/index.php/ocs/v2.php/cloud/capabilities")
	if status != http.StatusOK {
		t.Fatalf("/index.php/ocs/v2.php/cloud/capabilities status = %d", status)
	}
	ocs, ok := body["ocs"].(map[string]any)
	if !ok {
		t.Fatalf("missing ocs wrapper: %v", body)
	}
	data, ok := ocs["data"].(map[string]any)
	if !ok {
		t.Fatalf("missing data: %v", ocs)
	}
	caps, ok := data["capabilities"].(map[string]any)
	if !ok {
		t.Fatalf("missing capabilities: %v", data)
	}
	if _, ok := caps["theming"]; !ok {
		t.Error("missing theming in capabilities")
	}
	if _, ok := caps["files_sharing"]; !ok {
		t.Error("missing files_sharing in capabilities")
	}
}

func TestCompatFavoriteToggleAndListing(t *testing.T) {
	t.Parallel()
	e, openErr := lifecycle.Open(context.Background(), lifecycle.Options{DataDir: t.TempDir()})
	if openErr != nil {
		t.Fatalf("opening engine: %v", openErr)
	}
	t.Cleanup(func() {
		if cerr := e.Close(); cerr != nil {
			t.Errorf("closing engine: %v", cerr)
		}
	})
	ctx := context.Background()

	dir := t.TempDir()
	if rerr := e.Core.RegisterShare(ctx, core.ShareDef{
		ID: 1, Name: "files", Host: dir, Policy: vfs.DefaultSharePolicy(),
	}); rerr != nil {
		t.Fatalf("registering share: %v", rerr)
	}
	uid, aerr := e.Auth.CreateAdmin(ctx, "alice", "Alice", pwOf(loginPassword))
	if aerr != nil {
		t.Fatalf("creating admin: %v", aerr)
	}
	if gerr := e.Core.GrantEveryShare(ctx, uid); gerr != nil {
		t.Fatalf("granting: %v", gerr)
	}
	base := serveCompatEngine(t, e)
	dav := davAuth(t, e, base, uid)

	put := newReq(t, http.MethodPut, base+"/remote.php/webdav/files/star.txt", strings.NewReader("star-content"))
	put.Header.Set("Authorization", dav)
	putResp, err := compatClient().Do(put)
	if err != nil || putResp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT test file failed: %v, resp: %v", err, putResp)
	}
	closeRespBody(t, putResp)

	const propfindFav = `<?xml version="1.0"?>
<d:propfind xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns">
  <d:prop><oc:favorite/></d:prop>
</d:propfind>`
	find := newReq(t, "PROPFIND", base+"/remote.php/webdav/files/star.txt", strings.NewReader(propfindFav))
	find.Header.Set("Authorization", dav)
	find.Header.Set("Depth", "0")
	findResp, err := compatClient().Do(find)
	if err != nil || findResp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("PROPFIND failed: %v", err)
	}
	body := readAllBody(t, findResp.Body)
	closeRespBody(t, findResp)
	if !strings.Contains(string(body), ":favorite>0<") {
		t.Errorf("initial favorite want 0, got: %s", string(body))
	}

	const proppatchFav = `<?xml version="1.0"?>
<d:propertyupdate xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns">
  <d:set>
    <d:prop><oc:favorite>1</oc:favorite></d:prop>
  </d:set>
</d:propertyupdate>`
	patch := newReq(t, "PROPPATCH", base+"/remote.php/webdav/files/star.txt", strings.NewReader(proppatchFav))
	patch.Header.Set("Authorization", dav)
	patchResp, err := compatClient().Do(patch)
	if err != nil || patchResp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("PROPPATCH failed: %v", err)
	}
	closeRespBody(t, patchResp)

	find2 := newReq(t, "PROPFIND", base+"/remote.php/webdav/files/star.txt", strings.NewReader(propfindFav))
	find2.Header.Set("Authorization", dav)
	find2.Header.Set("Depth", "0")
	findResp2, err := compatClient().Do(find2)
	if err != nil {
		t.Fatalf("PROPFIND 2 failed: %v", err)
	}
	body2 := readAllBody(t, findResp2.Body)
	closeRespBody(t, findResp2)
	if !strings.Contains(string(body2), ":favorite>1<") {
		t.Errorf("favorite after PROPPATCH want 1, got: %s", string(body2))
	}

	favReq := newReq(t, http.MethodGet, base+"/ocs/v2.php/apps/files/api/v1/favorites", nil)
	favReq.Header.Set("Authorization", dav)
	favReq.Header.Set("Accept", "application/json")
	favResp, err := compatClient().Do(favReq)
	if err != nil || favResp.StatusCode != http.StatusOK {
		t.Fatalf("GET favorites failed: %v", err)
	}
	favBody := readAllBody(t, favResp.Body)
	closeRespBody(t, favResp)
	if !strings.Contains(string(favBody), "star.txt") {
		t.Errorf("favorites endpoint does not contain star.txt: %s", string(favBody))
	}
}

func TestCompatSharesCRUD(t *testing.T) {
	t.Parallel()
	e, openErr := lifecycle.Open(context.Background(), lifecycle.Options{DataDir: t.TempDir()})
	if openErr != nil {
		t.Fatalf("opening engine: %v", openErr)
	}
	t.Cleanup(func() {
		if cerr := e.Close(); cerr != nil {
			t.Errorf("closing engine: %v", cerr)
		}
	})
	ctx := context.Background()

	dir := t.TempDir()
	if rerr := e.Core.RegisterShare(ctx, core.ShareDef{
		ID: 1, Name: "files", Host: dir, Policy: vfs.DefaultSharePolicy(),
	}); rerr != nil {
		t.Fatalf("registering share: %v", rerr)
	}
	uid, aerr := e.Auth.CreateAdmin(ctx, "alice", "Alice", pwOf(loginPassword))
	if aerr != nil {
		t.Fatalf("creating admin: %v", aerr)
	}
	if gerr := e.Core.GrantEveryShare(ctx, uid); gerr != nil {
		t.Fatalf("granting: %v", gerr)
	}
	base := serveCompatEngine(t, e)
	dav := davAuth(t, e, base, uid)

	put := newReq(t, http.MethodPut, base+"/remote.php/webdav/files/shareme.txt", strings.NewReader("hello-share"))
	put.Header.Set("Authorization", dav)
	putResp, perr := compatClient().Do(put)
	if perr != nil || putResp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT test file failed: %v", perr)
	}
	closeRespBody(t, putResp)

	form := url.Values{
		"path":      {"files/shareme.txt"},
		"shareType": {"3"},
	}
	postReq := newReq(t, http.MethodPost, base+"/ocs/v2.php/apps/files_sharing/api/v1/shares", strings.NewReader(form.Encode()))
	postReq.Header.Set("Authorization", dav)
	postReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	postReq.Header.Set("Accept", "application/json")
	postResp, err := compatClient().Do(postReq)
	if err != nil || postResp.StatusCode != http.StatusOK {
		t.Fatalf("POST shares failed: %v", err)
	}
	postBody := readAllBody(t, postResp.Body)
	closeRespBody(t, postResp)
	if !strings.Contains(string(postBody), "shareme.txt") {
		t.Fatalf("created share response does not contain path: %s", string(postBody))
	}

	listReq := newReq(t, http.MethodGet, base+"/ocs/v2.php/apps/files_sharing/api/v1/shares", nil)
	listReq.Header.Set("Authorization", dav)
	listReq.Header.Set("Accept", "application/json")
	listResp, err := compatClient().Do(listReq)
	if err != nil || listResp.StatusCode != http.StatusOK {
		t.Fatalf("GET shares failed: %v", err)
	}
	listBody := readAllBody(t, listResp.Body)
	closeRespBody(t, listResp)
	if !strings.Contains(string(listBody), "shareme.txt") {
		t.Fatalf("list shares does not contain shareme.txt: %s", string(listBody))
	}
}

func TestCompatGroupShareAndSearch(t *testing.T) {
	t.Parallel()
	e, openErr := lifecycle.Open(context.Background(), lifecycle.Options{DataDir: t.TempDir()})
	if openErr != nil {
		t.Fatalf("opening engine: %v", openErr)
	}
	t.Cleanup(func() {
		if cerr := e.Close(); cerr != nil {
			t.Errorf("closing engine: %v", cerr)
		}
	})
	ctx := context.Background()

	dir := t.TempDir()
	if rerr := e.Core.RegisterShare(ctx, core.ShareDef{
		ID: 1, Name: "files", Host: dir, Policy: vfs.DefaultSharePolicy(),
	}); rerr != nil {
		t.Fatalf("registering share: %v", rerr)
	}
	alice, aerr := e.Auth.CreateAdmin(ctx, "alice", "Alice", pwOf(loginPassword))
	if aerr != nil {
		t.Fatalf("creating alice: %v", aerr)
	}
	bob, berr := e.Auth.CreateAdmin(ctx, "bob", "Bob", pwOf(loginPassword))
	if berr != nil {
		t.Fatalf("creating bob: %v", berr)
	}
	if gerr := e.Core.GrantEveryShare(ctx, alice); gerr != nil {
		t.Fatalf("granting alice: %v", gerr)
	}
	groupID, gerr := e.Auth.CreateGroup(ctx, "team")
	if gerr != nil {
		t.Fatalf("creating group: %v", gerr)
	}
	if merr := e.Auth.AddToGroup(ctx, bob, groupID); merr != nil {
		t.Fatalf("adding bob to group: %v", merr)
	}

	base := serveCompatEngine(t, e)
	aliceAuth := davAuth(t, e, base, alice)
	put := newReq(t, http.MethodPut, base+"/remote.php/webdav/files/needle.txt", strings.NewReader("needle"))
	put.Header.Set("Authorization", aliceAuth)
	putResp, perr := compatClient().Do(put)
	if perr != nil || putResp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT test file failed: %v", perr)
	}
	closeRespBody(t, putResp)

	form := url.Values{
		"path":      {"files/needle.txt"},
		"shareType": {"1"},
		"shareWith": {"team"},
	}
	create := newReq(t, http.MethodPost, base+"/ocs/v2.php/apps/files_sharing/api/v1/shares", strings.NewReader(form.Encode()))
	create.Header.Set("Authorization", aliceAuth)
	create.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	create.Header.Set("Accept", "application/json")
	createResp, cerr := compatClient().Do(create)
	if cerr != nil || createResp.StatusCode != http.StatusOK {
		t.Fatalf("creating group share failed: %v", cerr)
	}
	createBody := readAllBody(t, createResp.Body)
	closeRespBody(t, createResp)
	var created struct {
		OCS struct {
			Data struct {
				ID string `json:"id"`
			} `json:"data"`
		} `json:"ocs"`
	}
	if derr := jsonDecode(createBody, &created); derr != nil || created.OCS.Data.ID == "" {
		t.Fatalf("decoding group share: %v, raw: %s", derr, createBody)
	}
	if !strings.Contains(string(createBody), `"share_with":"team"`) {
		t.Fatalf("group name missing from group share: %s", createBody)
	}

	bobToken, _, terr := e.Auth.CreateSyncCredential(ctx, bob, "bob device")
	if terr != nil {
		t.Fatalf("minting bob credential: %v", terr)
	}
	list := newReq(t, http.MethodGet,
		base+"/ocs/v2.php/apps/files_sharing/api/v1/shares?shared_with_me=true", nil)
	list.SetBasicAuth("bob", bobToken)
	list.Header.Set("Accept", "application/json")
	listResp, lerr := compatClient().Do(list)
	if lerr != nil || listResp.StatusCode != http.StatusOK {
		t.Fatalf("listing shared group files failed: %v", lerr)
	}
	listBody := readAllBody(t, listResp.Body)
	closeRespBody(t, listResp)
	if !strings.Contains(string(listBody), "needle.txt") {
		t.Fatalf("group share is not visible to member: %s", listBody)
	}

	remove := newReq(t, http.MethodDelete,
		base+"/ocs/v2.php/apps/files_sharing/api/v1/shares/"+created.OCS.Data.ID, nil)
	remove.SetBasicAuth("bob", bobToken)
	remove.Header.Set("Accept", "application/json")
	removeResp, rerr := compatClient().Do(remove)
	if rerr != nil {
		t.Fatalf("member delete request failed: %v", rerr)
	}
	removeBody := readAllBody(t, removeResp.Body)
	closeRespBody(t, removeResp)
	if removeResp.StatusCode != http.StatusNotFound ||
		!strings.Contains(string(removeBody), `"statuscode":998`) {
		t.Fatalf("member unexpectedly managed group share: status=%d body=%s",
			removeResp.StatusCode, removeBody)
	}

	search := newReq(t, http.MethodGet,
		base+"/ocs/v2.php/search/providers/files/search?term=needle", nil)
	search.Header.Set("Authorization", aliceAuth)
	search.Header.Set("Accept", "application/json")
	searchResp, serr := compatClient().Do(search)
	if serr != nil || searchResp.StatusCode != http.StatusOK {
		t.Fatalf("searching files failed: %v", serr)
	}
	searchBody := readAllBody(t, searchResp.Body)
	closeRespBody(t, searchResp)
	if !strings.Contains(string(searchBody), "needle.txt") {
		t.Fatalf("search does not include matching file: %s", searchBody)
	}

	// The share picker's directory search. Decoded into the exact shape the
	// reference client's parser demands: it reads every array by name and
	// treats a missing one as a failed search, so the assertion is on the
	// structure and not only on the names inside it.
	sharees := newReq(t, http.MethodGet,
		base+"/ocs/v2.php/apps/files_sharing/api/v1/sharees?format=json&itemType=file&search=bob&page=1&perPage=50", nil)
	sharees.Header.Set("Authorization", aliceAuth)
	shareesResp, sherr := compatClient().Do(sharees)
	if sherr != nil || shareesResp.StatusCode != http.StatusOK {
		t.Fatalf("sharee search failed: %v", sherr)
	}
	shareesBody := readAllBody(t, shareesResp.Body)
	closeRespBody(t, shareesResp)

	type shareeValue struct {
		ShareType int    `json:"shareType"`
		ShareWith string `json:"shareWith"`
	}
	type shareeItem struct {
		Label string      `json:"label"`
		Name  string      `json:"name"`
		Value shareeValue `json:"value"`
	}
	type shareeLists struct {
		Users        *[]shareeItem `json:"users"`
		Groups       *[]shareeItem `json:"groups"`
		Remotes      *[]shareeItem `json:"remotes"`
		RemoteGroups *[]shareeItem `json:"remote_groups"`
		Emails       *[]shareeItem `json:"emails"`
	}
	var page struct {
		OCS struct {
			Data struct {
				shareeLists
				Exact *shareeLists `json:"exact"`
			} `json:"data"`
		} `json:"ocs"`
	}
	if derr := jsonDecode(shareesBody, &page); derr != nil {
		t.Fatalf("decoding sharees: %v, raw: %s", derr, shareesBody)
	}
	data := page.OCS.Data
	if data.Exact == nil {
		t.Fatalf("the sharee document has no exact block: %s", shareesBody)
	}
	for name, list := range map[string]*[]shareeItem{
		"users": data.Users, "groups": data.Groups,
		"remotes": data.Remotes, "remote_groups": data.RemoteGroups,
		"emails":      data.Emails,
		"exact.users": data.Exact.Users, "exact.groups": data.Exact.Groups,
		"exact.remotes": data.Exact.Remotes, "exact.remote_groups": data.Exact.RemoteGroups,
		"exact.emails": data.Exact.Emails,
	} {
		if list == nil {
			t.Errorf("the sharee document omits %q, which the client's parser requires: %s",
				name, shareesBody)
		}
	}
	if data.Exact.Users == nil || len(*data.Exact.Users) != 1 {
		t.Fatalf("an exact search for bob did not return one user: %s", shareesBody)
	}
	if got := (*data.Exact.Users)[0]; got.Value.ShareWith != "bob" ||
		got.Value.ShareType != 0 || got.Label != "Bob" {
		t.Errorf("the exact user entry is %+v", got)
	}
	if data.Users != nil && len(*data.Users) != 0 {
		t.Errorf("an exact match was also reported as partial: %s", shareesBody)
	}

	// The caller is never a target of their own share dialog.
	self := newReq(t, http.MethodGet,
		base+"/ocs/v2.php/apps/files_sharing/api/v1/sharees?format=json&itemType=file&search=alice", nil)
	self.Header.Set("Authorization", aliceAuth)
	selfResp, seerr := compatClient().Do(self)
	if seerr != nil || selfResp.StatusCode != http.StatusOK {
		t.Fatalf("self sharee search failed: %v", seerr)
	}
	selfBody := readAllBody(t, selfResp.Body)
	closeRespBody(t, selfResp)
	if strings.Contains(string(selfBody), `"shareWith":"alice"`) {
		t.Errorf("the caller offered themselves as a share target: %s", selfBody)
	}

	// A group the caller may share with is found by name.
	groupSearch := newReq(t, http.MethodGet,
		base+"/ocs/v2.php/apps/files_sharing/api/v1/sharees?format=json&itemType=file&search=team", nil)
	groupSearch.Header.Set("Authorization", aliceAuth)
	groupResp, goerr := compatClient().Do(groupSearch)
	if goerr != nil || groupResp.StatusCode != http.StatusOK {
		t.Fatalf("group sharee search failed: %v", goerr)
	}
	groupBody := readAllBody(t, groupResp.Body)
	closeRespBody(t, groupResp)
	if !strings.Contains(string(groupBody), `"shareType":1`) ||
		!strings.Contains(string(groupBody), `"shareWith":"team"`) {
		t.Errorf("the group is not offered as a share target: %s", groupBody)
	}

	// subfiles asks for the shares of a folder's children, which is how a
	// listing badges them without one call per entry.
	subfiles := newReq(t, http.MethodGet,
		base+"/ocs/v2.php/apps/files_sharing/api/v1/shares?path=files&subfiles=true&format=json", nil)
	subfiles.Header.Set("Authorization", aliceAuth)
	subResp, suerr := compatClient().Do(subfiles)
	if suerr != nil || subResp.StatusCode != http.StatusOK {
		t.Fatalf("subfiles share listing failed: %v", suerr)
	}
	subBody := readAllBody(t, subResp.Body)
	closeRespBody(t, subResp)
	if !strings.Contains(string(subBody), "needle.txt") {
		t.Fatalf("subfiles listing omits the shared child: %s", subBody)
	}

	// The same listing without subfiles is about the folder itself, so the
	// child's share does not belong in it.
	exactPath := newReq(t, http.MethodGet,
		base+"/ocs/v2.php/apps/files_sharing/api/v1/shares?path=files&format=json", nil)
	exactPath.Header.Set("Authorization", aliceAuth)
	exactResp, exerr := compatClient().Do(exactPath)
	if exerr != nil || exactResp.StatusCode != http.StatusOK {
		t.Fatalf("exact path share listing failed: %v", exerr)
	}
	exactBody := readAllBody(t, exactResp.Body)
	closeRespBody(t, exactResp)
	if strings.Contains(string(exactBody), "needle.txt") {
		t.Fatalf("a child share leaked into the folder's own listing: %s", exactBody)
	}
}

func TestCompatAppPasswordRevokeRevokesCaller(t *testing.T) {
	t.Parallel()
	e, openErr := lifecycle.Open(context.Background(), lifecycle.Options{DataDir: t.TempDir()})
	if openErr != nil {
		t.Fatalf("opening engine: %v", openErr)
	}
	t.Cleanup(func() {
		if cerr := e.Close(); cerr != nil {
			t.Errorf("closing engine: %v", cerr)
		}
	})
	ctx := context.Background()
	user, uerr := e.Auth.CreateAdmin(ctx, "alice", "Alice", pwOf(loginPassword))
	if uerr != nil {
		t.Fatalf("creating user: %v", uerr)
	}
	first, firstID, ferr := e.Auth.CreateSyncCredential(ctx, user, "first device")
	if ferr != nil {
		t.Fatalf("minting first credential: %v", ferr)
	}
	second, secondID, serr := e.Auth.CreateSyncCredential(ctx, user, "second device")
	if serr != nil {
		t.Fatalf("minting second credential: %v", serr)
	}

	base := serveCompatEngine(t, e)
	revoke := newReq(t, http.MethodDelete, base+"/ocs/v2.php/core/apppassword", nil)
	revoke.SetBasicAuth("alice", first)
	revoke.Header.Set("Accept", "application/json")
	revokeResp, rerr := compatClient().Do(revoke)
	if rerr != nil || revokeResp.StatusCode != http.StatusOK {
		t.Fatalf("revoking current credential failed: %v", rerr)
	}
	closeRespBody(t, revokeResp)
	if _, _, err := e.Auth.VerifyAppPassword(ctx, first); !errors.Is(err, auth.ErrCredentials) {
		t.Fatalf("revoked current credential verification error = %v, want credentials error", err)
	}
	if _, _, err := e.Auth.VerifyAppPassword(ctx, second); err != nil {
		t.Fatalf("unrelated credential stopped verifying: %v", err)
	}
	rows, rowsErr := e.Auth.AppPasswords(ctx, user)
	if rowsErr != nil {
		t.Fatalf("listing credentials: %v", rowsErr)
	}
	for _, row := range rows {
		if row.ID == firstID {
			t.Fatal("the request credential still exists")
		}
		if row.ID == secondID {
			return
		}
	}
	t.Fatal("the unrelated credential was removed")
}

func TestCompatDirectMediaStream(t *testing.T) {
	t.Parallel()
	e, openErr := lifecycle.Open(context.Background(), lifecycle.Options{DataDir: t.TempDir()})
	if openErr != nil {
		t.Fatalf("opening engine: %v", openErr)
	}
	t.Cleanup(func() {
		if cerr := e.Close(); cerr != nil {
			t.Errorf("closing engine: %v", cerr)
		}
	})
	ctx := context.Background()

	dir := t.TempDir()
	if rerr := e.Core.RegisterShare(ctx, core.ShareDef{
		ID: 1, Name: "files", Host: dir, Policy: vfs.DefaultSharePolicy(),
	}); rerr != nil {
		t.Fatalf("registering share: %v", rerr)
	}
	uid, aerr := e.Auth.CreateAdmin(ctx, "alice", "Alice", pwOf(loginPassword))
	if aerr != nil {
		t.Fatalf("creating admin: %v", aerr)
	}
	if gerr := e.Core.GrantEveryShare(ctx, uid); gerr != nil {
		t.Fatalf("granting: %v", gerr)
	}
	base := serveCompatEngine(t, e)
	dav := davAuth(t, e, base, uid)

	content := "0123456789abcdefghijklmnopqrstuvwxyz"
	put := newReq(t, http.MethodPut, base+"/remote.php/webdav/files/video.mp4", strings.NewReader(content))
	put.Header.Set("Authorization", dav)
	putResp, perr := compatClient().Do(put)
	if perr != nil || putResp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT test file failed: %v", perr)
	}
	closeRespBody(t, putResp)

	find := newReq(t, "PROPFIND", base+"/remote.php/webdav/files/video.mp4", strings.NewReader(`<?xml version="1.0"?><d:propfind xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns"><d:prop><oc:fileid/></d:prop></d:propfind>`))
	find.Header.Set("Authorization", dav)
	find.Header.Set("Depth", "0")
	findResp, ferr := compatClient().Do(find)
	if ferr != nil || findResp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("PROPFIND failed: %v", ferr)
	}
	findBody := readAllBody(t, findResp.Body)
	closeRespBody(t, findResp)

	var fileID string
	if parts := strings.Split(string(findBody), ":fileid>"); len(parts) > 1 {
		fileID = strings.Split(parts[1], "<")[0]
	}
	if fileID == "" {
		t.Fatalf("could not extract fileid from PROPFIND: %s", string(findBody))
	}

	form := url.Values{"fileId": {fileID}}
	postReq := newReq(t, http.MethodPost, base+"/ocs/v2.php/apps/dav/api/v1/direct", strings.NewReader(form.Encode()))
	postReq.Header.Set("Authorization", dav)
	postReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	postReq.Header.Set("Accept", "application/json")
	postResp, err := compatClient().Do(postReq)
	if err != nil || postResp.StatusCode != http.StatusOK {
		t.Fatalf("POST direct failed: %v", err)
	}
	postBody := readAllBody(t, postResp.Body)
	closeRespBody(t, postResp)

	if !strings.Contains(string(postBody), "direct") {
		t.Fatalf("direct response does not contain direct URL: %s", string(postBody))
	}

	var ocsResp struct {
		OCS struct {
			Data struct {
				URL string `json:"url"`
			} `json:"data"`
		} `json:"ocs"`
	}
	if derr := jsonDecode(postBody, &ocsResp); derr != nil || ocsResp.OCS.Data.URL == "" {
		t.Fatalf("failed decoding direct URL response: %v, raw: %s", derr, string(postBody))
	}

	getReq := newReq(t, http.MethodGet, ocsResp.OCS.Data.URL, nil)
	getReq.Header.Set("Range", "bytes=0-9")
	streamResp, err := compatClient().Do(getReq)
	if err != nil {
		t.Fatalf("streaming GET failed: %v", err)
	}
	defer func() {
		if cerr := streamResp.Body.Close(); cerr != nil {
			t.Errorf("closing stream body: %v", cerr)
		}
	}()
	if streamResp.StatusCode != http.StatusPartialContent {
		t.Errorf("streaming status = %d, want 206", streamResp.StatusCode)
	}
	streamBytes := readAllBody(t, streamResp.Body)
	if string(streamBytes) != "0123456789" {
		t.Errorf("streamed bytes = %q, want '0123456789'", string(streamBytes))
	}
}

func TestCompatTrashLifecycle(t *testing.T) {
	t.Parallel()
	e, openErr := lifecycle.Open(context.Background(), lifecycle.Options{DataDir: t.TempDir()})
	if openErr != nil {
		t.Fatalf("opening engine: %v", openErr)
	}
	t.Cleanup(func() {
		if cerr := e.Close(); cerr != nil {
			t.Errorf("closing engine: %v", cerr)
		}
	})
	ctx := context.Background()

	dir := t.TempDir()
	on := true
	if rerr := e.Core.RegisterShare(ctx, core.ShareDef{
		ID: 1, Name: "files", Host: dir, Policy: vfs.DefaultSharePolicy(), TrashEnabled: on,
	}); rerr != nil {
		t.Fatalf("registering share: %v", rerr)
	}
	uid, aerr := e.Auth.CreateAdmin(ctx, "alice", "Alice", pwOf(loginPassword))
	if aerr != nil {
		t.Fatalf("creating admin: %v", aerr)
	}
	if gerr := e.Core.GrantEveryShare(ctx, uid); gerr != nil {
		t.Fatalf("granting: %v", gerr)
	}
	base := serveCompatEngine(t, e)
	dav := davAuth(t, e, base, uid)

	put := newReq(t, http.MethodPut, base+"/remote.php/webdav/files/trashme.txt", strings.NewReader("trash-content"))
	put.Header.Set("Authorization", dav)
	putResp, perr := compatClient().Do(put)
	if perr != nil || putResp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT test file failed: %v", perr)
	}
	closeRespBody(t, putResp)

	del := newReq(t, http.MethodDelete, base+"/remote.php/webdav/files/trashme.txt", nil)
	del.Header.Set("Authorization", dav)
	delResp, derr := compatClient().Do(del)
	if derr != nil || delResp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE file failed: %v", derr)
	}
	closeRespBody(t, delResp)

	trashFind := newReq(t, "PROPFIND", base+"/remote.php/dav/trashbin/alice/trash", strings.NewReader(`<?xml version="1.0"?><d:propfind xmlns:d="DAV:" xmlns:nc="http://nextcloud.org/ns"><d:prop><nc:trashbin-filename/></d:prop></d:propfind>`))
	trashFind.Header.Set("Authorization", dav)
	trashFind.Header.Set("Depth", "1")
	trashResp, err := compatClient().Do(trashFind)
	if err != nil || trashResp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("PROPFIND trash failed: %v", err)
	}
	trashBody := readAllBody(t, trashResp.Body)
	closeRespBody(t, trashResp)
	if !strings.Contains(string(trashBody), "trashme.txt") {
		t.Fatalf("trash listing does not contain trashme.txt: %s", string(trashBody))
	}
}

func jsonDecode(data []byte, v any) error {
	return json.NewDecoder(strings.NewReader(string(data))).Decode(v)
}
