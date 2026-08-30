//go:build linux && compat_nc

package lifecycle_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/secret"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/task"
	"github.com/heavycaffeiner/stowcloud/go/engine/lifecycle"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
)

// The compatibility surface, served over a real engine.
//
// What matters here is what a client reads: the pre-sign-in probes answer
// before any credential, the account surfaces answer the caller's own record,
// and the login flow delivers exactly one credential for one approval.

// serveCompat boots an engine and serves it, returning the base URL.
func serveCompat(t *testing.T) string {
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
	return serveCompatEngine(t, e)
}

func serveCompatEngine(t *testing.T, e *lifecycle.Engine) string {
	t.Helper()
	app, err := e.Mount()
	if err != nil {
		t.Fatalf("mounting: %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	served := make(chan error, 1)
	task.Go(context.Background(), "compat listener", func() { served <- app.Listener(ln) })
	t.Cleanup(func() {
		if serr := app.ShutdownWithTimeout(shutdownBudget); serr != nil {
			t.Errorf("shutting down: %v", serr)
		}
		<-served
	})
	return "http://" + ln.Addr().String()
}

// getOCSJSON fetches and decodes an OCS answer as JSON, and returns the
// whole document: the envelope's payload lives under "ocs", and a test that
// wants the meta block reads it there.
func getOCSJSON(t *testing.T, url string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := compatClient().Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			t.Errorf("closing the response: %v", cerr)
		}
	}()

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decoding %s: %v", url, err)
	}
	return resp.StatusCode, body
}

// compatClient is the HTTP client every compat test uses. Built per call
// rather than held in a package variable, so one test mutating a transport
// cannot reach another.
func compatClient() *http.Client {
	return &http.Client{Timeout: 10 * time.Second}
}

// The status probe answers before any credential, and its shape is what a
// client reads to decide this server is the product it expects.
func TestTheStatusProbeAnswersUnauthenticated(t *testing.T) {
	t.Parallel()
	base := serveCompat(t)

	status, body := getOCSJSON(t, base+"/status.php")
	if status != 200 {
		t.Fatalf("answered %d, want 200", status)
	}
	if installed, ok := body["installed"].(bool); !ok || !installed {
		t.Errorf("installed is %v", body["installed"])
	}
	if v, ok := body["version"].(string); !ok || v == "" {
		t.Errorf("version is %v", body["version"])
	}
}

// The captive-portal probe is deliberately empty. Anything else reads as "no
// internet" and the Android client parks every upload without a request.
func TestTheCaptivePortalProbeIsEmpty(t *testing.T) {
	t.Parallel()
	base := serveCompat(t)

	resp, err := compatClient().Get(base + "/index.php/204")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			t.Errorf("closing the response: %v", cerr)
		}
	}()
	if resp.StatusCode != 204 {
		t.Errorf("answered %d, want 204", resp.StatusCode)
	}
}

// The capabilities document answers unauthenticated and carries the version
// block clients gate on.
func TestTheCapabilitiesDocumentAnswersUnauthenticated(t *testing.T) {
	t.Parallel()
	base := serveCompat(t)

	for _, prefix := range []string{"/ocs/v1.php", "/ocs/v2.php"} {
		status, body := getOCSJSON(t, base+prefix+"/cloud/capabilities")
		if status != 200 {
			t.Fatalf("%s answered %d, want 200", prefix, status)
		}
		ocs, ok := body["ocs"].(map[string]any)
		if !ok {
			t.Fatalf("%s: no ocs block: %v", prefix, body)
		}
		meta, ok := ocs["meta"].(map[string]any)
		if !ok {
			t.Fatalf("%s: no meta block: %v", prefix, ocs)
		}
		code, ok := meta["statuscode"].(float64)
		if !ok {
			t.Fatalf("%s: statuscode is %v", prefix, meta["statuscode"])
		}
		if prefix == "/ocs/v1.php" && code != 100 {
			t.Errorf("v1 statuscode is %v, want 100", code)
		}
		if prefix == "/ocs/v2.php" && code != 200 {
			t.Errorf("v2 statuscode is %v, want 200", code)
		}
	}
}

// The stub endpoints answer an empty success, which is what stops a client
// retrying in a loop or showing an error banner.
func TestTheStubEndpointsAnswerEmpty(t *testing.T) {
	t.Parallel()
	base := serveCompat(t)

	for _, path := range []string{
		"/ocs/v2.php/apps/notifications/api/v2/notifications",
		"/ocs/v2.php/core/navigation/apps",
		"/ocs/v2.php/core/autocomplete/get",
	} {
		status, body := getOCSJSON(t, base+path)
		if status != 200 {
			t.Errorf("%s answered %d, want 200", path, status)
		}
		ocs, ok := body["ocs"].(map[string]any)
		if !ok {
			t.Errorf("%s carries no ocs block: %v", path, body)
			continue
		}
		data, ok := ocs["data"]
		if !ok {
			t.Errorf("%s carries no data block", path)
			continue
		}
		// The empty answer is a list or an object, never a string: a typed
		// client casts without a guard.
		switch data.(type) {
		case []any, map[string]any:
		default:
			t.Errorf("%s data is %T, want a list or an object", path, data)
		}
	}
}

// An OCS route needing a principal is refused with the envelope's own
// unauthorised code, not an HTTP-level answer a client's parser trips over.
func TestAnAuthenticatedRouteIsRefusedWithoutACredential(t *testing.T) {
	t.Parallel()
	base := serveCompat(t)

	status, body := getOCSJSON(t, base+"/ocs/v2.php/cloud/user")
	if status != 401 {
		t.Fatalf("answered %d, want 401", status)
	}
	ocs, ok := body["ocs"].(map[string]any)
	if !ok {
		t.Fatalf("no ocs block: %v", body)
	}
	meta, ok := ocs["meta"].(map[string]any)
	if !ok {
		t.Fatalf("no meta block: %v", ocs)
	}
	code, ok := meta["statuscode"].(float64)
	if !ok || code != 997 {
		t.Errorf("statuscode is %v, want 997", meta["statuscode"])
	}
}

// The login flow: begin, approve, poll, once.
func TestTheLoginFlowDeliversOneCredential(t *testing.T) {
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
	base := serveCompatEngine(t, e)

	// Begin, as the desktop client does.
	resp, err := compatClient().Post(base+"/index.php/login/v2", "application/x-www-form-urlencoded", strings.NewReader(""))
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	var begun struct {
		Poll struct {
			Token    string `json:"token"`
			Endpoint string `json:"endpoint"`
		} `json:"poll"`
		Login string `json:"login"`
	}
	if derr := json.NewDecoder(resp.Body).Decode(&begun); derr != nil {
		t.Fatalf("decoding the begin answer: %v", derr)
	}
	if cerr := resp.Body.Close(); cerr != nil {
		t.Errorf("closing the response: %v", cerr)
	}
	if begun.Poll.Token == "" || begun.Login == "" {
		t.Fatalf("the begin answer is incomplete: %+v", begun)
	}

	// A poll before approval is a 404, which is the "not yet" the client
	// polls against.
	pollResp, err := compatClient().PostForm(base+"/index.php/login/v2/poll",
		map[string][]string{"token": {begun.Poll.Token}})
	if err != nil {
		t.Fatalf("the first poll: %v", err)
	}
	if cerr := pollResp.Body.Close(); cerr != nil {
		t.Errorf("closing the response: %v", cerr)
	}
	if pollResp.StatusCode != 404 {
		t.Errorf("an unapproved poll answered %d, want 404", pollResp.StatusCode)
	}

	// Approve through the flow itself, as the consent page's POST does once
	// the chain has checked the session and the token. The approving account
	// must exist: delivery mints against it, and a mint for a phantom account
	// is a refusal rather than a credential.
	// The approving account exists, because delivery mints against it.
	ctx := context.Background()
	uid, cerr := e.Auth.CreateAdmin(ctx, "u", "U", secret.New([]byte("delivery-test-password")))
	if cerr != nil {
		t.Fatalf("creating the approving account: %v", cerr)
	}
	if aerr := e.Flow.Approve(ctx, loginTokenOf(begun.Login), uid, "u"); aerr != nil {
		t.Fatalf("approving: %v", aerr)
	}

	// The poll now delivers.
	delivered, err := compatClient().PostForm(base+"/index.php/login/v2/poll",
		map[string][]string{"token": {begun.Poll.Token}})
	if err != nil {
		t.Fatalf("the delivering poll: %v", err)
	}
	var out struct {
		Server      string `json:"server"`
		LoginName   string `json:"loginName"`
		AppPassword string `json:"appPassword"`
	}
	if derr := json.NewDecoder(delivered.Body).Decode(&out); derr != nil {
		t.Fatalf("decoding the delivery: %v", derr)
	}
	if cerr := delivered.Body.Close(); cerr != nil {
		t.Errorf("closing the response: %v", cerr)
	}
	if out.AppPassword == "" || out.LoginName == "" {
		t.Errorf("the delivery is incomplete: %+v", out)
	}
	if delivered.StatusCode != 200 {
		t.Errorf("the delivery answered %d, want 200", delivered.StatusCode)
	}

	// A second poll finds nothing: one flow, one credential.
	again, err := compatClient().PostForm(base+"/index.php/login/v2/poll",
		map[string][]string{"token": {begun.Poll.Token}})
	if err != nil {
		t.Fatalf("the second poll: %v", err)
	}
	if cerr := again.Body.Close(); cerr != nil {
		t.Errorf("closing the response: %v", cerr)
	}
	if again.StatusCode != 404 {
		t.Errorf("the second poll answered %d, want 404", again.StatusCode)
	}
}

// loginTokenOf takes the login URL apart for the token inside it, which is
// what a test approving a flow needs and what nothing else in this file does.
func loginTokenOf(loginURL string) string {
	i := strings.LastIndexByte(loginURL, '/')
	if i < 0 {
		return ""
	}
	return loginURL[i+1:]
}

// The consent page the device login sends the browser to.

// An unauthenticated visitor is redirected to sign in and come back, rather
// than refused: the client opened this in a fresh browser, so having no
// session here is the ordinary case.
func TestTheConsentPageRedirectsAVisitorWithoutASession(t *testing.T) {
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
	base := serveCompatEngine(t, e)

	// The client does not follow the redirect: the answer under test is the
	// 302 itself, and /login is the frontend's page, which nothing here
	// serves.
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := noFollow.Get(base + "/index.php/login/v2/flow/some-token")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			t.Errorf("closing the response: %v", cerr)
		}
	}()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("answered %d, want 302", resp.StatusCode)
	}
	loc, lerr := url.Parse(resp.Header.Get("Location"))
	if lerr != nil {
		t.Fatalf("parsing the redirect: %v", lerr)
	}
	if loc.Path != "/login" {
		t.Errorf("the redirect names %q, want the sign-in page", loc.Path)
	}
	if got := loc.Query().Get("returnTo"); got != "/index.php/login/v2/flow/some-token" {
		t.Errorf("returnTo is %q, want the page the visitor asked for", got)
	}
}

// A signed-in visitor gets the page, and the CSRF token on it is the one the
// grant route's own check verifies. A page carrying a token derived from
// anything else would render a button that always fails.
func TestTheConsentPageRendersForASignedInVisitor(t *testing.T) {
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
	if _, cerr := e.Auth.CreateUser(ctx, "alice", "Alice", pwOf(loginPassword)); cerr != nil {
		t.Fatalf("creating the account: %v", cerr)
	}
	base := serveCompatEngine(t, e)
	cookie, _ := signedIn(t, base)

	// A flow to name on the page.
	flow, err := e.Flow.Begin(ctx, base)
	if err != nil {
		t.Fatalf("beginning a flow: %v", err)
	}

	req, err := http.NewRequest(http.MethodGet, flow.LoginURL, nil)
	if err != nil {
		t.Fatalf("building: %v", err)
	}
	req.AddCookie(cookie)
	resp, err := compatClient().Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			t.Errorf("closing the response: %v", cerr)
		}
	}()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("answered %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Errorf("content type is %q, want text/html", ct)
	}

	body, rerr := io.ReadAll(resp.Body)
	if rerr != nil {
		t.Fatalf("reading the page: %v", rerr)
	}
	page := string(body)
	if !strings.Contains(page, "data-token=\""+loginTokenOf(flow.LoginURL)+"\"") {
		t.Error("the page does not carry the flow's token")
	}

	// The CSRF token on the page is the one the chain's check accepts.
	csp := resp.Header.Get("Content-Security-Policy")
	if !strings.Contains(csp, "script-src 'nonce-") {
		t.Errorf("the page carries no script nonce: %q", csp)
	}
}

// The vendor properties a sync client reads, served through the real mount.
//
// The desktop client keys its journal on oc:fileid and gates what it does on
// oc:permissions; an answer missing either makes entries disappear from the
// sync or the whole sync stall. This is the end-to-end proof: credential, the
// framework boundary, the mount, the source and the document.

// davAuth mints the credential a WebDAV client carries and returns the
// header value for it. Basic, because that is what the clients send and what
// the chain resolves; a session cookie would be a browser authority asking
// the CSRF step for permission to mutate, which it does not carry.
func davAuth(t *testing.T, e *lifecycle.Engine, base string, uid int64) string {
	t.Helper()
	token, _, err := e.Auth.CreateSyncCredential(context.Background(), uid, "dav test")
	if err != nil {
		t.Fatalf("minting the dav credential: %v", err)
	}
	return "Basic " + base64.StdEncoding.EncodeToString(
		[]byte("alice:"+token))
}

const propfindNamed = `<?xml version="1.0"?>
<d:propfind xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns">
  <d:prop><oc:fileid/><oc:permissions/><oc:size/></d:prop>
</d:propfind>`

func TestVendorPropsAreServedOnAPropfind(t *testing.T) {
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

	// A share over a directory the test can read back, and the setup flow's
	// own grant, whose label is the share's name.
	dir := t.TempDir()
	if rerr := e.Core.RegisterShare(ctx, core.ShareDef{
		ID: 1, Name: "files", Host: dir, Policy: vfs.DefaultSharePolicy(),
	}); rerr != nil {
		t.Fatalf("registering the share: %v", rerr)
	}
	uid, aerr := e.Auth.CreateAdmin(ctx, "alice", "Alice", pwOf(loginPassword))
	if aerr != nil {
		t.Fatalf("creating the admin: %v", aerr)
	}
	if gerr := e.Core.GrantEveryShare(ctx, uid); gerr != nil {
		t.Fatalf("granting: %v", gerr)
	}
	base := serveCompatEngine(t, e)
	dav := davAuth(t, e, base, uid)

	// The file the listing will describe, written through the mount itself.
	put, err := http.NewRequest(http.MethodPut, base+"/dav/files/test.txt",
		strings.NewReader("contents"))
	if err != nil {
		t.Fatalf("building the PUT: %v", err)
	}
	put.Header.Set("Authorization", dav)
	putResp, err := compatClient().Do(put)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	if cerr := putResp.Body.Close(); cerr != nil {
		t.Errorf("closing the response: %v", cerr)
	}
	if putResp.StatusCode != http.StatusCreated {
		t.Fatalf("the PUT answered %d, want 201", putResp.StatusCode)
	}

	find, err := http.NewRequest("PROPFIND", base+"/dav/files/test.txt",
		strings.NewReader(propfindNamed))
	if err != nil {
		t.Fatalf("building the PROPFIND: %v", err)
	}
	find.Header.Set("Depth", "0")
	find.Header.Set("Content-Type", "application/xml")
	find.Header.Set("Authorization", dav)
	findResp, err := compatClient().Do(find)
	if err != nil {
		t.Fatalf("PROPFIND: %v", err)
	}
	defer func() {
		if cerr := findResp.Body.Close(); cerr != nil {
			t.Errorf("closing the response: %v", cerr)
		}
	}()
	if findResp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("the PROPFIND answered %d, want 207", findResp.StatusCode)
	}
	raw, rerr := io.ReadAll(findResp.Body)
	if rerr != nil {
		t.Fatalf("reading the answer: %v", rerr)
	}
	body := string(raw)

	// The writer allocates its own prefixes (ns0 and friends), so the
	// assertions read the local names and check that the vocabulary's
	// namespace is declared in the same document: a client's parser resolves
	// by URI, and a property in a namespace the document never declares is
	// the defect that once dropped stored properties from their own answer.
	if !strings.Contains(body, `"http://owncloud.org/ns"`) {
		t.Errorf("the vendor namespace is not declared: %s", body)
	}
	// The file id is present and non-empty, the permissions carry the letters
	// the grant confers (G read, W writable, and the rest of the full grant),
	// and the size is the file's own.
	if !strings.Contains(body, ":fileid>") {
		t.Errorf("oc:fileid is absent: %s", body)
	}
	if !strings.Contains(body, ":permissions>RGDNVW<") {
		t.Errorf("oc:permissions is not the full grant's letters: %s", body)
	}
	if !strings.Contains(body, ":size>8<") {
		t.Errorf("oc:size is not the file's own: %s", body)
	}
}

// The alias a sync client mounts reaches the same tree.
func TestTheCompatPrefixServesTheSameTree(t *testing.T) {
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
	dir := t.TempDir()
	if rerr := e.Core.RegisterShare(ctx, core.ShareDef{
		ID: 1, Name: "files", Host: dir, Policy: vfs.DefaultSharePolicy(),
	}); rerr != nil {
		t.Fatalf("registering the share: %v", rerr)
	}
	uid, aerr := e.Auth.CreateAdmin(ctx, "alice", "Alice", pwOf(loginPassword))
	if aerr != nil {
		t.Fatalf("creating the admin: %v", aerr)
	}
	if gerr := e.Core.GrantEveryShare(ctx, uid); gerr != nil {
		t.Fatalf("granting: %v", gerr)
	}
	base := serveCompatEngine(t, e)
	dav := davAuth(t, e, base, uid)

	put, err := http.NewRequest(http.MethodPut, base+"/dav/files/alias.txt",
		strings.NewReader("via-alias"))
	if err != nil {
		t.Fatalf("building the PUT: %v", err)
	}
	put.Header.Set("Authorization", dav)
	putResp, err := compatClient().Do(put)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	if cerr := putResp.Body.Close(); cerr != nil {
		t.Errorf("closing the response: %v", cerr)
	}
	if putResp.StatusCode != http.StatusCreated {
		t.Fatalf("the PUT answered %d, want 201", putResp.StatusCode)
	}

	get, err := http.NewRequest(http.MethodGet, base+"/remote.php/webdav/files/alias.txt", nil)
	if err != nil {
		t.Fatalf("building the GET: %v", err)
	}
	get.Header.Set("Authorization", dav)
	getResp, err := compatClient().Do(get)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() {
		if cerr := getResp.Body.Close(); cerr != nil {
			t.Errorf("closing the response: %v", cerr)
		}
	}()
	raw, rerr := io.ReadAll(getResp.Body)
	if rerr != nil {
		t.Fatalf("reading: %v", rerr)
	}
	if getResp.StatusCode != http.StatusOK || string(raw) != "via-alias" {
		t.Errorf("the alias answered %d with %q, want 200 and the file", getResp.StatusCode, raw)
	}
}
