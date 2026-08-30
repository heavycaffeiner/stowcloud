//go:build linux && compat_nc

package lifecycle_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/secret"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/task"
	"github.com/heavycaffeiner/stowcloud/go/engine/lifecycle"
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
