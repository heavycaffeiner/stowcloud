//go:build linux

package e2e_test

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	app "github.com/heavycaffeiner/stowcloud/backend/internal/app"
	"github.com/heavycaffeiner/stowcloud/backend/internal/feature/auth"
	"github.com/heavycaffeiner/stowcloud/backend/internal/feature/files"
	"github.com/heavycaffeiner/stowcloud/backend/internal/feature/shares/acl"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/middleware"
	"github.com/heavycaffeiner/stowcloud/backend/internal/platform/concurrency"
	secret "github.com/heavycaffeiner/stowcloud/backend/internal/platform/security/secret"
)

// openEngine opens an engine on a fresh data directory and closes it when the
// test ends.
func openEngine(t *testing.T) *app.Engine {
	t.Helper()
	return openEngineAt(t, t.TempDir())
}

// openEngineAt opens an engine on dir and closes it when the test ends.
func openEngineAt(t *testing.T, dir string) *app.Engine {
	t.Helper()
	e, err := app.Open(context.Background(), app.Options{DataDir: dir, PasswordParams: fastPasswordParams()})
	if err != nil {
		t.Fatalf("opening the engine: %v", err)
	}
	t.Cleanup(func() {
		if cerr := e.Close(); cerr != nil {
			t.Errorf("closing the engine: %v", cerr)
		}
	})
	return e
}

// fastPasswordParams are cheap Argon2id parameters for a test-built engine,
// so the lifecycle suite is not paying the real deployment cost on every
// boot. auth.New refuses parameters this weak outside a test binary, which
// is what keeps a deployment from being lowered by whatever constructed it.
func fastPasswordParams() auth.Params {
	return auth.Params{MemoryKiB: 8192, Iterations: 1, Parallelism: 1, KeyLen: 32}
}

// boot opens an engine and serves it on a real socket.
func boot(t *testing.T) string {
	t.Helper()
	return serve(t, openEngine(t))
}

// serve mounts an already-open engine and puts it behind a real listener.
func serve(t *testing.T, e *app.Engine) string {
	t.Helper()

	app := gin.New()
	if err := e.Mount(app); err != nil {
		t.Fatalf("mounting: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}

	served := make(chan error, 1)
	srv := &http.Server{
		Handler:           app,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      5 * time.Second,
		IdleTimeout:       5 * time.Second,
	}
	concurrency.Go(context.Background(), "test listener", func() { served <- srv.Serve(ln) })

	t.Cleanup(func() {
		if lerr := ln.Close(); lerr != nil && !errors.Is(lerr, net.ErrClosed) {
			t.Errorf("closing the listener: %v", lerr)
		}
		select {
		case <-served:
		case <-time.After(shutdownBudget):
			t.Error("the test listener did not stop after shutdown")
		}
	})

	return "http://" + ln.Addr().String()
}

// testClient is a client that does not hold connections open.
//
// Keep-alive is what makes a shutdown wait: the server has an idle connection
// it must not cut, and closing after each response means the test's own
// cleanup does not race the client's idle timeout.
func testClient() *http.Client {
	return &http.Client{
		Timeout:   5 * time.Second,
		Transport: &http.Transport{DisableKeepAlives: true},
	}
}

// get performs a real request and returns the status and body.
func get(t *testing.T, url string) (int, []byte) {
	t.Helper()

	resp, err := testClient().Get(url)
	if err != nil {
		t.Fatalf("requesting %s: %v", url, err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			t.Errorf("closing the body: %v", cerr)
		}
	}()

	return resp.StatusCode, readAll(t, resp)
}

// shutdownBudget bounds every test server's wind-down. Well under the
// client's 90-second idle timeout, so a real hang is still caught, and well
// above what a loaded machine needs to close an idle listener.
const shutdownBudget = 20 * time.Second

// session is a signed-in browser's credential: the cookie the login response
// set, and the CSRF token that has to ride beside it on every mutation.
//
// The native API now admits only this. middleware.Scope refuses every
// non-public route unless the resolved principal is a session cookie, so a
// helper that drives /api/v1 needs the cookie-and-token pair rather than a
// device credential's token string.
type session struct {
	cookie *http.Cookie
	csrf   string
}

// attach adds the session to a request: the cookie always, and the CSRF
// header for every method but GET and HEAD, which is what the chain's own
// CSRFRequired rule demands of a session credential.
//
// The zero session attaches nothing. That is how a test drives a route
// anonymously through the same helper, rather than through a second one that
// would drift from this.
func (s session) attach(req *http.Request) {
	if s.cookie == nil {
		return
	}
	req.AddCookie(s.cookie)
	if req.Method != http.MethodGet && req.Method != http.MethodHead {
		req.Header.Set(middleware.CSRFHeader, s.csrf)
		req.Header.Set("Origin", req.URL.Scheme+"://"+req.Host)
	}
}

// signIn signs an account in over HTTP and returns its session, failing the
// test if the sign-in produced no cookie.
func signIn(t *testing.T, base, login, password string) session {
	t.Helper()

	resp := postJSON(t, base+"/api/v1/auth/login", map[string]string{"login": login, "password": password})
	cookie := resp.sessionCookie()
	if cookie == nil {
		t.Fatalf("%s could not sign in: %v", login, resp.body)
	}
	return session{cookie: cookie, csrf: resp.field("csrf")}
}

// bootWithUser serves an engine holding one account, and returns the base
// URL, an app password for it, and a session for the same account.
//
// Both credentials, because the two surfaces take different ones: the DAV and
// compat mounts resolve a device credential directly, and the native API
// admits only the browser session.
func bootWithUser(t *testing.T) (string, string, session) {
	t.Helper()
	ctx := context.Background()

	e := openEngine(t)

	id, err := e.Auth.CreateUser(ctx, "alice", "Alice", secret.New([]byte("a-long-enough-password")))
	if err != nil {
		t.Fatalf("creating the account: %v", err)
	}
	token, err := e.Auth.CreateAppPassword(ctx, id, "test",
		auth.Scope{Perms: uint16(acl.Read | acl.Write | acl.Download)}, 0)
	if err != nil {
		t.Fatalf("minting an app password: %v", err)
	}

	base := serve(t, e)
	sess := signIn(t, base, "alice", "a-long-enough-password")
	return base, token, sess
}

// authed performs a request carrying a browser session, which is what every
// /api/v1 route now requires.
func authed(t *testing.T, method, url string, sess session) (int, []byte) {
	t.Helper()

	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	sess.attach(req)

	resp, err := testClient().Do(req)
	if err != nil {
		t.Fatalf("requesting %s: %v", url, err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	}()

	return resp.StatusCode, readAll(t, resp)
}

// contentShare serves an engine holding one share with a file of known bytes.
func contentShare(t *testing.T, perms acl.Perms, content []byte) (base string, sess session, share string) {
	b, sess, sh, _ := contentShareAt(t, perms, content)
	return b, sess, sh
}

// contentShareAt is contentShare plus the host directory, for a test that has
// to act on the files themselves rather than through the API.
func contentShareAt(t *testing.T, perms acl.Perms, content []byte) (base string, sess session, share, host string) {
	b, sess, sh, h, _, _ := contentShareGrant(t, perms, content)
	return b, sess, sh, h
}

// contentShareGrant is contentShareAt plus the engine and the grant's id, for
// a test that has to change what the account may reach while the server runs.
func contentShareGrant(t *testing.T, perms acl.Perms, content []byte) (
	base string, sess session, share, host string, e *app.Engine, grant int64,
) {
	t.Helper()
	ctx := context.Background()

	e = openEngine(t)

	id, err := e.Auth.CreateUser(ctx, "alice", "Alice", secret.New([]byte("a-long-enough-password")))
	if err != nil {
		t.Fatal(err)
	}

	host = t.TempDir()
	if werr := os.WriteFile(filepath.Join(host, "doc.bin"), content, 0o600); werr != nil {
		t.Fatal(werr)
	}
	if merr := os.Mkdir(filepath.Join(host, "sub"), 0o700); merr != nil {
		t.Fatal(merr)
	}

	sh, err := e.Core.CreateShare(ctx, core.ShareSpec{Name: "files", Host: host})
	if err != nil {
		t.Fatal(err)
	}
	g, gerr := e.Core.CreateGrant(ctx, core.GrantSpec{
		User: &id, Share: sh.ID, Allow: perms, Inherit: true, Label: sh.Name,
	})
	if gerr != nil {
		t.Fatal(gerr)
	}

	served := serve(t, e)
	return served, signIn(t, served, "alice", "a-long-enough-password"), sh.Name, host, e, g.ID
}

// readAll drains a response body, failing the test if the read breaks off.
func readAll(t *testing.T, resp *http.Response) []byte {
	t.Helper()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the body: %v", err)
	}
	return body
}

// everyPerm is the full mask.
func everyPerm() acl.Perms {
	return acl.Read | acl.Write | acl.Create | acl.Delete |
		acl.Rename | acl.Move | acl.Share | acl.Download
}

// pwOf wraps a plaintext password for the service.
func pwOf(s string) secret.Secret { return secret.New([]byte(s)) }
