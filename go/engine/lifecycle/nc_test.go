//go:build linux && compat_nc

package lifecycle_test

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	neturl "net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/lifecycle"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
)

// The compatibility surface, driven the way the real clients drive it.
//
// Every test in this family replays a sequence one of the three clients
// actually sends, and asserts what that client actually reads. Where a client
// is strict, the assertion is strict: a listing that omits an etag, a
// multistatus whose prefixes are capitalised or an upload whose response
// carries no file id are all answers that leave a real client with an empty
// screen, and none of them is visible from a status code.

// ncFixture is an engine with one share, one account and one device
// credential, served over a real listener.
type ncFixture struct {
	base  string
	share string
	host  string
	login string
	// token is the device credential, which is what every client sends as the
	// password half of HTTP Basic once its login flow has finished.
	token string
	user  int64
	e     *lifecycle.Engine
	sess  session
}

// newNCFixture serves an engine holding one file of known bytes.
func newNCFixture(t *testing.T, content []byte) ncFixture {
	t.Helper()
	ctx := context.Background()

	base, sess, share, host, e, _ := contentShareGrant(t, ncEveryPerm(), content)

	id, err := e.Auth.UserIDByName(ctx, "alice")
	if err != nil {
		t.Fatalf("looking up the account: %v", err)
	}
	token, _, err := e.Auth.CreateSyncCredential(ctx, id, "device login")
	if err != nil {
		t.Fatalf("minting a device credential: %v", err)
	}

	return ncFixture{
		base: base, share: share, host: host, login: "alice",
		token: token, user: id, e: e, sess: sess,
	}
}

// everyPerm is the grant a client of this surface needs: it browses, reads,
// writes, deletes, renames, moves and shares.
func ncEveryPerm() acl.Perms {
	return acl.Read | acl.Write | acl.Create | acl.Delete |
		acl.Rename | acl.Move | acl.Share | acl.Download
}

// davRoot is the collection a client mounts: the account's own files tree.
func (f ncFixture) davRoot() string {
	return f.base + "/remote.php/dav/files/" + f.login
}

// filePath addresses one path inside the fixture's share.
func (f ncFixture) filePath(rest string) string {
	if rest == "" {
		return f.davRoot() + "/" + f.share
	}
	return f.davRoot() + "/" + f.share + "/" + rest
}

// request performs one authenticated request with the device credential.
//
// Basic authentication with the token as the password, which is what every
// client sends: none of them uses a bearer header, and the surface has to
// accept the one they send rather than the one that would be tidier.
func (f ncFixture) request(
	t *testing.T, method, url string, body io.Reader, headers map[string]string,
) (ncReply, []byte) {
	t.Helper()

	req, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatalf("building %s %s: %v", method, url, err)
	}
	req.Header.Set("Authorization", "Basic "+
		base64.StdEncoding.EncodeToString([]byte(f.login+":"+f.token)))
	req.Header.Set("OCS-APIRequest", "true")
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := ncClient().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			t.Errorf("closing the response: %v", cerr)
		}
	}()

	read, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the response to %s %s: %v", method, url, err)
	}
	return ncReply{StatusCode: resp.StatusCode, Header: resp.Header}, read
}

// anonymous performs one request with no credential, for the paths a client
// reads before it has one and for a public link.
func ncAnonymous(t *testing.T, method, url string, body io.Reader) (ncReply, []byte) {
	t.Helper()

	req, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatalf("building %s %s: %v", method, url, err)
	}
	return doAnonymous(t, req)
}

// doAnonymous sends a request a test built itself and reads the whole answer.
func doAnonymous(t *testing.T, req *http.Request) (ncReply, []byte) {
	t.Helper()

	resp, err := ncClient().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", req.Method, req.URL, err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			t.Errorf("closing the response: %v", cerr)
		}
	}()

	read, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the response to %s %s: %v", req.Method, req.URL, err)
	}
	return ncReply{StatusCode: resp.StatusCode, Header: resp.Header}, read
}

// jsonRequest is the shape one client sends for the documents it wants as
// JSON: no query parameter, an Accept header alone.
func jsonRequest(t *testing.T, url string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("building %s: %v", url, err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("OCS-APIRequest", "true")
	return req
}

// postForm builds a form submission, for the browser half of the device login.
func postForm(t *testing.T, url string, form neturl.Values) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("building %s: %v", url, err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return req
}

// ncReply is one answer, read and closed.
//
// A value rather than the live response: the body is drained and closed
// before a test sees it, and handing back an object whose body is already
// gone is a trap for the next test written against this fixture.
type ncReply struct {
	StatusCode int
	Header     http.Header
}

// ncClient is built per call rather than shared, so one test mutating a
// transport cannot reach another. Redirects are not followed: a client here
// treats a redirect on most of these paths as a captive portal, and a test
// that followed one would assert about the wrong response.
func ncClient() *http.Client {
	return &http.Client{
		Timeout:       15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// writeHostFile puts a file into the share behind the server's back, for a
// test that needs something to read without uploading it first.
func writeHostFile(t *testing.T, host, name string, content []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(host, name), content, 0o600); err != nil {
		t.Fatalf("seeding %s: %v", name, err)
	}
}

// hasProp reports whether a property is present at all, in either the
// value-carrying or the empty-element spelling.
func hasProp(element, prop string) bool {
	return strings.Contains(element, "<"+prop+">") || strings.Contains(element, "<"+prop+"/>")
}

// sumContentLengths adds up every content length a multistatus reports,
// which is how a client turns a resume listing into the offset it carries on
// from.
func sumContentLengths(t *testing.T, body string) int {
	t.Helper()
	total := 0
	for _, part := range strings.Split(body, "<d:getcontentlength>")[1:] {
		end := strings.Index(part, "</d:getcontentlength>")
		if end < 0 {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(part[:end]))
		if err != nil {
			t.Fatalf("a content length does not parse: %q", part[:end])
		}
		total += n
	}
	return total
}

// entryOf pulls one response element out of a multistatus body by the href it
// carries, and reports the whole element so a test can assert about the
// properties inside it.
//
// String matching rather than an XML parse, deliberately: the clients match
// on literal prefixes too, and a test that parsed namespaces would pass
// against a document those clients cannot read.
func entryOf(t *testing.T, body, href string) string {
	t.Helper()
	for _, part := range strings.Split(body, "<d:response>") {
		if strings.Contains(part, "<d:href>"+href+"</d:href>") {
			return part
		}
	}
	t.Fatalf("no response for %s in\n%s", href, body)
	return ""
}

// propValue reads one property's text out of a response element.
func propValue(t *testing.T, element, prop string) string {
	t.Helper()
	open := "<" + prop + ">"
	i := strings.Index(element, open)
	if i < 0 {
		t.Fatalf("%s is absent from\n%s", prop, element)
	}
	rest := element[i+len(open):]
	j := strings.Index(rest, "</"+prop+">")
	if j < 0 {
		t.Fatalf("%s is not closed in\n%s", prop, element)
	}
	return rest[:j]
}
