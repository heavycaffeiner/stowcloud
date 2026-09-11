//go:build linux && compat_nc

package lifecycle_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/lifecycle"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
)

// The host roles, as the compatibility surface sees them: the app host serves
// the vocabulary, the content host serves the direct stream and nothing
// else, and only an operator-listed origin reads an OCS response across
// origins.

const (
	ncAppHost      = "app.example"
	ncContentHost  = "files.example"
	ncAllowedOrign = "https://reader.example"
)

// namedNCFixture serves an engine whose app and content hosts are declared,
// with one allowed origin, one account, one share holding one file, and a
// device credential for that account.
func namedNCFixture(t *testing.T, content []byte) ncFixture {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()

	first, err := lifecycle.Open(ctx, lifecycle.Options{DataDir: dir, PasswordParams: fastPasswordParams()})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	if merr := first.State.MergeSettings(ctx, "network", map[string]any{
		"app_hosts":       []any{ncAppHost},
		"content_hosts":   []any{ncContentHost},
		"allowed_origins": []any{ncAllowedOrign},
	}); merr != nil {
		t.Fatalf("saving the hosts: %v", merr)
	}
	id, err := first.Auth.CreateUser(ctx, "alice", "Alice", pwOf("a-long-enough-password"))
	if err != nil {
		t.Fatalf("creating the account: %v", err)
	}
	host := t.TempDir()
	if werr := os.WriteFile(filepath.Join(host, "doc.bin"), content, 0o600); werr != nil {
		t.Fatalf("seeding the file: %v", werr)
	}
	if merr := os.Mkdir(filepath.Join(host, "sub"), 0o700); merr != nil {
		t.Fatalf("seeding the folder: %v", merr)
	}
	sh, err := first.Core.CreateShare(ctx, core.ShareSpec{Name: "files", Host: host})
	if err != nil {
		t.Fatalf("creating the share: %v", err)
	}
	if _, gerr := first.Core.CreateGrant(ctx, core.GrantSpec{
		User: &id, Share: sh.ID, Allow: ncEveryPerm(), Inherit: true, Label: sh.Name,
	}); gerr != nil {
		t.Fatalf("granting: %v", gerr)
	}
	token, _, err := first.Auth.CreateSyncCredential(ctx, id, "device login")
	if err != nil {
		t.Fatalf("minting a device credential: %v", err)
	}
	if cerr := first.Close(); cerr != nil {
		t.Fatalf("closing: %v", cerr)
	}

	// Reopened so the host lists are read from the stored document, which is
	// the order an operator configures in.
	e, err := lifecycle.Open(ctx, lifecycle.Options{DataDir: dir, PasswordParams: fastPasswordParams()})
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}
	t.Cleanup(func() {
		if cerr := e.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	})
	return ncFixture{
		base: serve(t, e), share: sh.Name, host: host, login: "alice",
		token: token, user: id, e: e,
	}
}

// hostRequest performs one request under a given Host header, with the device
// credential when authenticated is set.
func (f ncFixture) hostRequest(
	t *testing.T, method, url, host string, authenticated bool, body io.Reader, headers map[string]string,
) (ncReply, []byte) {
	t.Helper()

	req, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatalf("building %s %s: %v", method, url, err)
	}
	req.Host = host
	if authenticated {
		req.Header.Set("Authorization", "Basic "+
			base64.StdEncoding.EncodeToString([]byte(f.login+":"+f.token)))
		req.Header.Set("OCS-APIRequest", "true")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return doAnonymous(t, req)
}

// directURLOf mints a direct link for the fixture's file through the OCS
// route under a given Host and returns the URL the client is handed.
//
// The id comes from a PROPFIND, as a client's does. That listing is what
// records the id in the reverse index the minting route resolves it through;
// before that record existed, a file id was reported but never resolved and
// every direct link and preview asked for by id answered 404.
func directURLOf(t *testing.T, f ncFixture, host string) string {
	t.Helper()

	_, listing := f.hostRequest(t, "PROPFIND", f.filePath(""), host, true,
		strings.NewReader(propfindBody), map[string]string{"Depth": "1", "Content-Type": "application/xml"})
	entry := entryOf(t, string(listing), "/remote.php/dav/files/"+f.login+"/"+f.share+"/doc.bin")
	fileID := propValue(t, entry, "oc:fileid")

	resp, body := f.hostRequest(t, http.MethodPost, f.base+"/ocs/v2.php/apps/dav/api/v1/direct?format=json",
		host, true, strings.NewReader("fileId="+fileID),
		map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("minting the direct link answered %d: %s", resp.StatusCode, body)
	}
	var envelope struct {
		OCS struct {
			Data struct {
				URL string `json:"url"`
			} `json:"data"`
		} `json:"ocs"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decoding the direct link: %v: %s", err, body)
	}
	if envelope.OCS.Data.URL == "" {
		t.Fatalf("the direct link carries no URL: %s", body)
	}
	return envelope.OCS.Data.URL
}

// directTokenOf mints a direct link on the app host and returns the token it
// carries, so a test can fetch the stream under a chosen Host and spelling.
func directTokenOf(t *testing.T, f ncFixture, host string) string {
	t.Helper()
	url := directURLOf(t, f, host)
	i := strings.LastIndex(url, "/remote.php/direct/")
	if i < 0 {
		t.Fatalf("the direct link %q names no direct path", url)
	}
	return url[i+len("/remote.php/direct/"):]
}

// A direct link is minted for the content host, on the port the request
// arrived on, and the bytes it names are served there: the host lists carry
// names alone and the listener decides the port.
func TestADirectLinkIsMintedForTheContentHost(t *testing.T) {
	t.Parallel()
	const content = "minted bytes"
	f := namedNCFixture(t, []byte(content))

	url := directURLOf(t, f, ncAppHost+":8443")
	const want = "http://" + ncContentHost + ":8443/remote.php/direct/"
	if !strings.HasPrefix(url, want) {
		t.Fatalf("the direct link is %q, want a %q prefix", url, want)
	}

	path := strings.TrimPrefix(url, "http://"+ncContentHost+":8443")
	served, body := f.hostRequest(t, http.MethodGet, f.base+path, ncContentHost+":8443", false, nil, nil)
	if served.StatusCode != http.StatusOK || string(body) != content {
		t.Errorf("the minted link answered %d %q, want 200 %q", served.StatusCode, body, content)
	}
}

// The direct stream is served under the content host only, in both
// spellings. The app host refuses it once a content host is named, so
// uploaded bytes never render on the origin that holds the session cookie.
func TestTheDirectStreamIsServedFromTheContentHostOnly(t *testing.T) {
	t.Parallel()
	const content = "direct bytes"
	f := namedNCFixture(t, []byte(content))
	token := directTokenOf(t, f, ncAppHost)

	for _, path := range []string{"/remote.php/direct/" + token, "/index.php/remote.php/direct/" + token} {
		served, body := f.hostRequest(t, http.MethodGet, f.base+path, ncContentHost, false, nil, nil)
		if served.StatusCode != http.StatusOK || string(body) != content {
			t.Errorf("the content host answered %d %q for %s, want 200 %q", served.StatusCode, body, path, content)
		}
		refused, _ := f.hostRequest(t, http.MethodGet, f.base+path, ncAppHost, false, nil, nil)
		if refused.StatusCode != http.StatusMisdirectedRequest {
			t.Errorf("the app host served %s: %d, want 421", path, refused.StatusCode)
		}
	}
}

// The content host serves the direct stream and nothing else of this
// vocabulary: neither the OCS routes nor the DAV mount answer there.
func TestTheContentHostServesNoCompatibilityRoute(t *testing.T) {
	t.Parallel()
	f := namedNCFixture(t, []byte("x"))

	for _, c := range []struct {
		what   string
		method string
		path   string
	}{
		{"capabilities", http.MethodGet, "/ocs/v2.php/cloud/capabilities?format=json"},
		{"the status document", http.MethodGet, "/status.php"},
		{"the DAV mount", "PROPFIND", "/remote.php/dav/files/" + f.login},
		{"the front-controller DAV mount", "PROPFIND", "/index.php/remote.php/webdav/"},
		{"a mutation on the direct path", http.MethodPost, "/remote.php/direct/" + directTokenOf(t, f, ncAppHost)},
	} {
		resp, _ := f.hostRequest(t, c.method, f.base+c.path, ncContentHost, true, nil, nil)
		if resp.StatusCode != http.StatusMisdirectedRequest {
			t.Errorf("%s answered %d on the content host, want 421", c.what, resp.StatusCode)
		}
	}
}

// With no content host named, the direct link and its stream stay on the
// request's own origin: the feature does not depend on the second host.
func TestADirectLinkStaysOnTheAppHostWithoutAContentHost(t *testing.T) {
	t.Parallel()
	const content = "same host bytes"
	f := newNCFixture(t, []byte(content))

	url := directURLOf(t, f, strings.TrimPrefix(f.base, "http://"))
	if !strings.HasPrefix(url, f.base+"/remote.php/direct/") {
		t.Fatalf("the direct link is %q, want it under %s", url, f.base)
	}
	served, got := ncAnonymous(t, http.MethodGet, url, nil)
	if served.StatusCode != http.StatusOK || string(got) != content {
		t.Errorf("the stream answered %d %q, want 200 %q", served.StatusCode, got, content)
	}
}

// The id a write reports resolves as well: a client fetches a preview of a
// file it just uploaded by that id, before any listing has reported it.
func TestTheIDAWriteReportsResolvesToTheFile(t *testing.T) {
	t.Parallel()
	f := newNCFixture(t, []byte("x"))

	resp, _ := f.request(t, http.MethodPut, f.filePath("fresh.txt"), strings.NewReader("just written"), nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("the upload answered %d", resp.StatusCode)
	}
	header := resp.Header.Get("OC-FileId")
	// The header is the zero-padded numeric id followed by a 24-character
	// instance tag, which is how a client stores it.
	if len(header) <= 24 {
		t.Fatalf("OC-FileId is %q", header)
	}
	fileID := strings.TrimLeft(header[:len(header)-24], "0")

	minted, body := f.request(t, http.MethodPost, f.base+"/ocs/v2.php/apps/dav/api/v1/direct?format=json",
		strings.NewReader("fileId="+fileID), map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
	if minted.StatusCode != http.StatusOK {
		t.Fatalf("a direct link for a just-written file answered %d: %s", minted.StatusCode, body)
	}
	var envelope struct {
		OCS struct {
			Data struct {
				URL string `json:"url"`
			} `json:"data"`
		} `json:"ocs"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decoding the direct link: %v: %s", err, body)
	}
	served, got := ncAnonymous(t, http.MethodGet, envelope.OCS.Data.URL, nil)
	if served.StatusCode != http.StatusOK || string(got) != "just written" {
		t.Errorf("the stream answered %d %q", served.StatusCode, got)
	}
}

// An OCS response is readable across origins only by an origin the operator
// listed, which is echoed rather than replaced by a wildcard. Any other
// origin, and a request carrying none, gets no cross-origin headers at all.
func TestOCSAnswersCrossOriginHeadersOnlyForAnAllowedOrigin(t *testing.T) {
	t.Parallel()
	f := namedNCFixture(t, []byte("x"))
	const capabilities = "/ocs/v2.php/cloud/capabilities?format=json"

	for _, c := range []struct {
		what   string
		method string
		origin string
		want   string
	}{
		{"an allowed origin", http.MethodGet, ncAllowedOrign, ncAllowedOrign},
		{"an allowed origin's preflight", http.MethodOptions, ncAllowedOrign, ncAllowedOrign},
		{"an allowed origin written with its default port", http.MethodGet, ncAllowedOrign + ":443", ncAllowedOrign + ":443"},
		{"a stranger", http.MethodGet, "https://evil.example", ""},
		{"a subdomain of an allowed origin", http.MethodGet, "https://sub.reader.example", ""},
		{"no origin", http.MethodGet, "", ""},
	} {
		headers := map[string]string{}
		if c.origin != "" {
			headers["Origin"] = c.origin
		}
		resp, body := f.hostRequest(t, c.method, f.base+capabilities, ncAppHost, false, nil, headers)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s: answered %d: %s", c.what, resp.StatusCode, body)
			continue
		}
		if got := resp.Header.Get("Access-Control-Allow-Origin"); got != c.want {
			t.Errorf("%s: Access-Control-Allow-Origin is %q, want %q", c.what, got, c.want)
		}
		if c.want != "" && !strings.Contains(resp.Header.Get("Vary"), "Origin") {
			t.Errorf("%s: Vary is %q, which lets a cache serve this answer to another origin", c.what, resp.Header.Get("Vary"))
		}
		if resp.Header.Get("Access-Control-Allow-Credentials") != "" {
			t.Errorf("%s: credentials were allowed across origins", c.what)
		}
	}
}
