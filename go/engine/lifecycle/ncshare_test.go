//go:build linux && compat_nc

package lifecycle_test

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/secret"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
)

// Public links, from the client's request to a stranger fetching the file.
//
// The share family is XML by default: one client never asks for anything else
// and parses the envelope with a namespace-unaware reader. So these tests read
// XML unless they explicitly ask for JSON, which is what the other client
// does.

const sharesPath = "/ocs/v2.php/apps/files_sharing/api/v1/shares"

// createLink mints a public link for a path and returns the whole XML body.
func createLink(t *testing.T, f ncFixture, path string, extra url.Values) (int, string) {
	t.Helper()
	form := url.Values{}
	form.Set("path", path)
	form.Set("shareType", "3")
	for k, vs := range extra {
		for _, v := range vs {
			form.Add(k, v)
		}
	}
	resp, body := f.request(t, "POST", f.base+sharesPath, strings.NewReader(form.Encode()),
		map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
	return resp.StatusCode, string(body)
}

// xmlField reads one element's text out of the share envelope.
func xmlField(t *testing.T, body, name string) string {
	t.Helper()
	head, tail := "<"+name+">", "</"+name+">"
	i := strings.Index(body, head)
	if i < 0 {
		t.Fatalf("%s is absent from\n%s", name, body)
	}
	rest := body[i+len(head):]
	j := strings.Index(rest, tail)
	if j < 0 {
		t.Fatalf("%s is not closed in\n%s", name, body)
	}
	return rest[:j]
}

// A client spells a folder with a trailing separator, and the path it asks
// about is the path it shows. The panel that fronts sharing reads the shares
// of that path before it opens, so a refusal here closed the whole screen
// with "unable to fetch sharees" and no folder could be shared at all.
func TestASharePathIsAcceptedInEverySpellingAClientSends(t *testing.T) {
	t.Parallel()
	f := newNCFixture(t, []byte("hello"))

	for _, path := range []string{
		"/" + f.share + "/",        // a share root, as a folder is spelled
		"/" + f.share,              // the same folder without the separator
		"/" + f.share + "/sub/",    // a folder inside it
		"/" + f.share + "/doc.bin", // a file
		"/",                        // the account root
		"",                         // no path at all
	} {
		resp, body := f.request(t, "GET",
			f.base+sharesPath+"?path="+url.QueryEscape(path)+"&reshares=true&subfiles=false",
			nil, nil)
		if resp.StatusCode != 200 {
			t.Errorf("listing the shares of %q answered %d\n%s", path, resp.StatusCode, body)
			continue
		}
		if got := xmlField(t, string(body), "statuscode"); got != "200" {
			t.Errorf("listing the shares of %q reported %s", path, got)
		}
	}
}

// And a share is created on the same spelling: the client posts the path it
// showed, separator and all.
func TestAFolderIsSharedUnderTheSpellingAClientSends(t *testing.T) {
	t.Parallel()
	f := newNCFixture(t, []byte("hello"))

	status, body := createLink(t, f, "/"+f.share+"/sub/", nil)
	if status != 200 {
		t.Fatalf("creating answered %d\n%s", status, body)
	}
	if got := xmlField(t, body, "item_type"); got != "folder" {
		t.Errorf("item_type is %q", got)
	}
	if xmlField(t, body, "token") == "" {
		t.Error("no token, so no client can build the link")
	}

	// The panel for that folder now finds the share it just made, which is
	// the round trip the screen actually performs.
	resp, listed := f.request(t, "GET",
		f.base+sharesPath+"?path="+url.QueryEscape("/"+f.share+"/sub/")+"&reshares=true&subfiles=false",
		nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("the panel's listing answered %d\n%s", resp.StatusCode, listed)
	}
	if !strings.Contains(string(listed), "<element>") {
		t.Errorf("the folder's own share is missing from its panel:\n%s", listed)
	}
}

// The shared-files screen lists what the account shared and then reads each
// path back. Both halves have to agree about how a folder is spelled, or the
// screen lists a row it cannot open.
func TestTheSharedScreenListsAndThenReadsEachShare(t *testing.T) {
	t.Parallel()
	f := newNCFixture(t, []byte("hello"))

	if status, body := createLink(t, f, "/"+f.share+"/sub", nil); status != 200 {
		t.Fatalf("sharing the folder answered %d\n%s", status, body)
	}
	if status, body := createLink(t, f, "/"+f.share+"/doc.bin", nil); status != 200 {
		t.Fatalf("sharing the file answered %d\n%s", status, body)
	}

	resp, body := f.request(t, "GET", f.base+sharesPath+"?include_tags=true", nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("the screen's listing answered %d\n%s", resp.StatusCode, body)
	}
	doc := string(body)
	if got := strings.Count(doc, "<element>"); got != 2 {
		t.Fatalf("the screen lists %d shares, want 2\n%s", got, doc)
	}

	// Every path the screen was given resolves, which is what it does next.
	for _, path := range pathsIn(doc) {
		resp, read := f.request(t, "PROPFIND", f.davRoot()+pathEscape(path),
			strings.NewReader(propfindBody),
			map[string]string{"Depth": "0", "Content-Type": "application/xml"})
		if resp.StatusCode != 207 {
			t.Errorf("reading %q back answered %d\n%s", path, resp.StatusCode, read)
		}
	}
}

// pathsIn reads every share path out of a share list.
func pathsIn(doc string) []string {
	var out []string
	for _, part := range strings.Split(doc, "<path>")[1:] {
		if i := strings.Index(part, "</path>"); i >= 0 {
			out = append(out, part[:i])
		}
	}
	return out
}

// pathEscape spells a share path as a URL path, one component at a time.
func pathEscape(path string) string {
	out := ""
	for _, comp := range strings.Split(strings.Trim(path, "/"), "/") {
		out += "/" + url.PathEscape(comp)
	}
	if strings.HasSuffix(path, "/") {
		out += "/"
	}
	return out
}

// The whole point of the feature: a link is created, its URL is absolute, and
// a stranger holding it reaches the file.
func TestAPublicLinkServesTheFileToAStranger(t *testing.T) {
	t.Parallel()
	const content = "shared bytes"
	f := newNCFixture(t, []byte(content))

	status, body := createLink(t, f, "/"+f.share+"/doc.bin", nil)
	if status != 200 {
		t.Fatalf("creating a link answered %d\n%s", status, body)
	}
	if got := xmlField(t, body, "statuscode"); got != "200" {
		t.Fatalf("the envelope reports %s\n%s", got, body)
	}

	if got := xmlField(t, body, "share_type"); got != "3" {
		t.Errorf("share_type is %s", got)
	}
	token := xmlField(t, body, "token")
	if token == "" {
		t.Fatal("no token, and one client builds the link URL out of it")
	}
	link := xmlField(t, body, "url")
	if !strings.HasPrefix(link, "http://") {
		t.Fatalf("url is %q, and a client hands it to a person verbatim", link)
	}
	if !strings.HasSuffix(link, token) {
		t.Errorf("url %q does not name the token %q", link, token)
	}
	if id := xmlField(t, body, "id"); id == "" {
		t.Error("no id, and one client discards a share that has none")
	}

	// The URL is not decoration: it has to open the file for somebody with no
	// credential at all.
	resp, _ := ncAnonymous(t, "GET", link, nil)
	if resp.StatusCode != 200 && resp.StatusCode != 302 {
		t.Fatalf("the link answered %d to a stranger", resp.StatusCode)
	}

	resp, served := ncAnonymous(t, "GET", link+"/download", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("the download answered %d", resp.StatusCode)
	}
	if string(served) != content {
		t.Errorf("the link served %q", served)
	}
}

// A link is listed back on the path it was made for, which is how the client
// shows the sharing state of a file it is looking at.
func TestALinkIsListedForItsPath(t *testing.T) {
	t.Parallel()
	f := newNCFixture(t, []byte("hello"))

	if status, body := createLink(t, f, "/"+f.share+"/doc.bin", nil); status != 200 {
		t.Fatalf("creating answered %d\n%s", status, body)
	}

	resp, body := f.request(t, "GET",
		f.base+sharesPath+"?path=%2F"+f.share+"%2Fdoc.bin&reshares=false&subfiles=false", nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("listing answered %d\n%s", resp.StatusCode, body)
	}
	doc := string(body)
	if !strings.Contains(doc, "<element>") {
		t.Fatalf("the list has no elements:\n%s", doc)
	}
	if got := xmlField(t, doc, "path"); !strings.Contains(got, "doc.bin") {
		t.Errorf("path is %q", got)
	}
	if got := xmlField(t, doc, "item_type"); got != "file" {
		t.Errorf("item_type is %q", got)
	}
}

// The other client asks for the same family as JSON, so both encodings have to
// answer the same facts.
func TestTheShareListAnswersJSONWhenAsked(t *testing.T) {
	t.Parallel()
	f := newNCFixture(t, []byte("hello"))

	if status, body := createLink(t, f, "/"+f.share+"/doc.bin", nil); status != 200 {
		t.Fatalf("creating answered %d\n%s", status, body)
	}

	resp, body := f.request(t, "GET", f.base+sharesPath, nil,
		map[string]string{"Accept": "application/json"})
	if resp.StatusCode != 200 {
		t.Fatalf("answered %d\n%s", resp.StatusCode, body)
	}

	var doc struct {
		OCS struct {
			Meta struct {
				StatusCode int `json:"statuscode"`
			} `json:"meta"`
			Data []struct {
				ID          any    `json:"id"`
				ShareType   int    `json:"share_type"`
				Token       string `json:"token"`
				URL         string `json:"url"`
				Permissions int    `json:"permissions"`
				Path        string `json:"path"`
			} `json:"data"`
		} `json:"ocs"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("the JSON does not parse: %v\n%s", err, body)
	}
	if doc.OCS.Meta.StatusCode != 200 {
		t.Errorf("the envelope reports %d", doc.OCS.Meta.StatusCode)
	}
	if len(doc.OCS.Data) != 1 {
		t.Fatalf("the list holds %d shares", len(doc.OCS.Data))
	}
	got := doc.OCS.Data[0]
	if got.ShareType != 3 || got.Token == "" || got.URL == "" {
		t.Errorf("the share is %#v", got)
	}
	if got.Permissions == 0 {
		t.Error("permissions are zero, which no client can render")
	}
}

// A password and an expiry are set through the update call, which is what the
// clients use: one of them creates minimally and then patches.
func TestALinkTakesAPasswordAndAnExpiry(t *testing.T) {
	t.Parallel()
	f := newNCFixture(t, []byte("hello"))

	status, body := createLink(t, f, "/"+f.share+"/doc.bin", nil)
	if status != 200 {
		t.Fatalf("creating answered %d\n%s", status, body)
	}
	id := xmlField(t, body, "id")
	link := xmlField(t, body, "url")

	patch := `{"password":"a-long-enough-secret","expireDate":"2030-12-31","note":"for review"}`
	resp, updated := f.request(t, "PUT", f.base+sharesPath+"/"+id, strings.NewReader(patch),
		map[string]string{"Content-Type": "application/json"})
	if resp.StatusCode != 200 {
		t.Fatalf("the update answered %d\n%s", resp.StatusCode, updated)
	}

	// The password is now required, so a stranger with the URL alone gets the
	// unlock page rather than the bytes.
	direct, _ := ncAnonymous(t, "GET", link+"/download", nil)
	if direct.StatusCode == 200 {
		t.Error("the download served the file without the password")
	}

	// And the expiry travels back in the shape a client parses.
	resp, listed := f.request(t, "GET", f.base+sharesPath+"/"+id, nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("reading the share back answered %d\n%s", resp.StatusCode, listed)
	}
	if got := xmlField(t, string(listed), "expiration"); !strings.HasPrefix(got, "2030-12-31") {
		t.Errorf("expiration is %q", got)
	}

	// A date picker reaches years a nanosecond timestamp cannot count to. It
	// has to be accepted rather than refused as an expiry already past, which
	// is what the unclamped conversion turned it into.
	far := `{"expireDate":"2999-12-31"}`
	if resp, body := f.request(t, "PUT", f.base+sharesPath+"/"+id, strings.NewReader(far),
		map[string]string{"Content-Type": "application/json"}); resp.StatusCode != 200 {
		t.Fatalf("a far-future expiry answered %d\n%s", resp.StatusCode, body)
	}
}

// Deleting a link revokes it, which is the whole of what the client's own
// unshare button does.
func TestDeletingALinkRevokesIt(t *testing.T) {
	t.Parallel()
	f := newNCFixture(t, []byte("hello"))

	status, body := createLink(t, f, "/"+f.share+"/doc.bin", nil)
	if status != 200 {
		t.Fatalf("creating answered %d\n%s", status, body)
	}
	id := xmlField(t, body, "id")
	link := xmlField(t, body, "url")

	resp, deleted := f.request(t, "DELETE", f.base+sharesPath+"/"+id, nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("the delete answered %d\n%s", resp.StatusCode, deleted)
	}

	after, _ := ncAnonymous(t, "GET", link+"/download", nil)
	if after.StatusCode == 200 {
		t.Error("the link still serves the file after being deleted")
	}
}

// A link on a folder is a folder link, and the client renders it differently
// on the strength of item_type alone.
func TestALinkOnAFolderReportsItAsOne(t *testing.T) {
	t.Parallel()
	f := newNCFixture(t, []byte("hello"))

	status, body := createLink(t, f, "/"+f.share+"/sub", nil)
	if status != 200 {
		t.Fatalf("creating answered %d\n%s", status, body)
	}
	if got := xmlField(t, body, "item_type"); got != "folder" {
		t.Errorf("item_type is %q", got)
	}
}

// A share the caller does not own is not theirs to read, and the answer is
// absence rather than a refusal: the id space is guessable.
func TestAnotherAccountsShareIsNotFound(t *testing.T) {
	t.Parallel()
	f := newNCFixture(t, []byte("hello"))

	resp, body := f.request(t, "GET", f.base+sharesPath+"/999999", nil, nil)
	if resp.StatusCode != 404 {
		t.Fatalf("answered %d, want 404\n%s", resp.StatusCode, body)
	}
	if got := xmlField(t, string(body), "statuscode"); got != "404" {
		t.Errorf("the envelope reports %s", got)
	}
}

// The share picker opens before the panel that also fronts public links, and
// one client reads several of the response's arrays without checking they are
// there. It offers nobody: sharing with an account is not something a client
// does here.
func TestTheShareePickerAnswersEveryGroupItReads(t *testing.T) {
	t.Parallel()
	f := newNCFixture(t, []byte("hello"))

	resp, body := f.request(t, "GET",
		f.base+"/ocs/v2.php/apps/files_sharing/api/v1/sharees?search=al&itemType=file&format=json",
		nil, map[string]string{"Accept": "application/json"})
	if resp.StatusCode != 200 {
		t.Fatalf("answered %d\n%s", resp.StatusCode, body)
	}

	var doc struct {
		OCS struct {
			Data map[string]json.RawMessage `json:"data"`
		} `json:"ocs"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("the JSON does not parse: %v\n%s", err, body)
	}
	exact, ok := doc.OCS.Data["exact"]
	if !ok {
		t.Fatalf("there is no exact group:\n%s", body)
	}
	var groups map[string]json.RawMessage
	if err := json.Unmarshal(exact, &groups); err != nil {
		t.Fatalf("the exact group does not parse: %v", err)
	}
	for _, key := range []string{"users", "groups", "remotes", "remote_groups", "emails"} {
		if _, present := groups[key]; !present {
			t.Errorf("exact.%s is absent, and one client reads it without checking", key)
		}
		raw, present := doc.OCS.Data[key]
		if !present {
			t.Errorf("%s is absent from the top level", key)
			continue
		}
		var candidates []json.RawMessage
		if err := json.Unmarshal(raw, &candidates); err != nil {
			t.Errorf("%s does not parse as a list: %v", key, err)
			continue
		}
		if len(candidates) != 0 {
			t.Errorf("the picker offers %d %s to share with", len(candidates), key)
		}
	}
}

// grantTo shares a path with an account rather than by link, which is what
// this surface refuses.
func grantTo(t *testing.T, f ncFixture, path, login string, shareType int) (int, string) {
	t.Helper()
	form := url.Values{}
	form.Set("path", path)
	form.Set("shareType", strconv.Itoa(shareType))
	form.Set("shareWith", login)
	resp, body := f.request(t, "POST", f.base+sharesPath, strings.NewReader(form.Encode()),
		map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
	return resp.StatusCode, string(body)
}

// idsIn reads every share id out of a share document.
func idsIn(doc string) []string {
	var out []string
	for _, part := range strings.Split(doc, "<id>")[1:] {
		if i := strings.Index(part, "</id>"); i >= 0 {
			out = append(out, part[:i])
		}
	}
	return out
}

// grantAnAccount hands a second account access to the fixture's share the
// way this deployment's administration does, straight through the engine.
func grantAnAccount(t *testing.T, f ncFixture, login string) {
	t.Helper()
	ctx := context.Background()
	id, err := f.e.Auth.CreateUser(ctx, login, login, secret.New([]byte("another-long-password")))
	if err != nil {
		t.Fatalf("creating the second account: %v", err)
	}
	mine, err := f.e.Core.ListGrants(ctx, core.GrantFilter{User: f.user})
	if err != nil || len(mine) == 0 {
		t.Fatalf("reading the account's own grants: %v", err)
	}
	share, nerr := num.Narrow[core.ShareID](mine[0].Share)
	if nerr != nil {
		t.Fatalf("reading the share id: %v", nerr)
	}
	if _, gerr := f.e.Core.CreateGrant(ctx, core.GrantSpec{
		User: &id, Share: share, Allow: ncEveryPerm(), Inherit: true, Label: f.share,
	}); gerr != nil {
		t.Fatalf("granting the second account: %v", gerr)
	}
}

// An access grant is not a share. It is how this deployment hands an account
// the share in the first place, and the shared-files screen lists what the
// account published, so a grant reported there shows a person their own
// access as something they shared out.
func TestAnAccessGrantIsNeverListedAsAShare(t *testing.T) {
	t.Parallel()
	f := newNCFixture(t, []byte("hello"))
	grantAnAccount(t, f, "bob")

	for _, query := range []string{
		"?include_tags=true",                               // the shared-files screen
		"?shared_with_me=true",                             // what other people shared with me
		"?path=" + url.QueryEscape("/"+f.share),            // one folder's sharing panel
		"?path=" + url.QueryEscape("/") + "&subfiles=true", // the root, folder by folder
	} {
		resp, body := f.request(t, "GET", f.base+sharesPath+query, nil, nil)
		if resp.StatusCode != 200 {
			t.Errorf("listing %s answered %d\n%s", query, resp.StatusCode, body)
			continue
		}
		if strings.Contains(string(body), "<element>") {
			t.Errorf("listing %s reports a grant as a share:\n%s", query, body)
		}
	}

	// The screen is empty because nothing is published, not because the
	// listing is broken: a link lands in it.
	if status, body := createLink(t, f, "/"+f.share+"/doc.bin", nil); status != 200 {
		t.Fatalf("sharing by link answered %d\n%s", status, body)
	}
	resp, body := f.request(t, "GET", f.base+sharesPath+"?include_tags=true", nil, nil)
	if got := strings.Count(string(body), "<element>"); got != 1 {
		t.Fatalf("the screen lists %d shares, want the one link\n%s", got, body)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("the screen's listing answered %d", resp.StatusCode)
	}
	if got := xmlField(t, string(body), "share_type"); got != "3" {
		t.Errorf("the listed share is type %q, want a link", got)
	}
}

// And it cannot be created either. A share a client can write but never see
// again is worse than one it cannot write: the account would believe it had
// shared something that no screen of theirs will ever show.
func TestSharingWithAnAccountIsRefused(t *testing.T) {
	t.Parallel()
	f := newNCFixture(t, []byte("hello"))
	grantAnAccount(t, f, "bob")
	ctx := context.Background()

	before, err := f.e.Core.ListGrants(ctx, core.GrantFilter{})
	if err != nil {
		t.Fatalf("reading the grants: %v", err)
	}
	for _, shareType := range []int{0, 1} {
		status, body := grantTo(t, f, "/"+f.share+"/sub", "bob", shareType)
		if status != 403 {
			t.Errorf("share type %d answered %d, want a refusal\n%s", shareType, status, body)
		}
	}
	after, err := f.e.Core.ListGrants(ctx, core.GrantFilter{})
	if err != nil {
		t.Fatalf("reading the grants back: %v", err)
	}
	if len(after) != len(before) {
		t.Errorf("the refusal still wrote a grant: %d grants, was %d", len(after), len(before))
	}
}

// The shared-files screen reads a share id with a 32-bit parse, and one id
// past that ceiling costs the whole document rather than the one entry: the
// screen reports an error and lists nothing at all.
func TestEveryShareIDFitsTheParseAClientReadsItWith(t *testing.T) {
	t.Parallel()
	f := newNCFixture(t, []byte("hello"))

	status, created := createLink(t, f, "/"+f.share+"/doc.bin", nil)
	if status != 200 {
		t.Fatalf("sharing by link answered %d\n%s", status, created)
	}
	resp, body := f.request(t, "GET", f.base+sharesPath+"?include_tags=true", nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("the screen's listing answered %d\n%s", resp.StatusCode, body)
	}
	for _, doc := range []string{created, string(body)} {
		ids := idsIn(doc)
		if len(ids) == 0 {
			t.Fatalf("no id to read in\n%s", doc)
		}
		for _, id := range ids {
			if _, err := strconv.ParseInt(id, 10, 32); err != nil {
				t.Errorf("the id %s does not fit a 32-bit parse, so the whole document is lost", id)
			}
		}
	}
}
