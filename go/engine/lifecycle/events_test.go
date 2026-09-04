//go:build linux

package lifecycle_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/clock"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/task"

	"github.com/heavycaffeiner/stowcloud/go/engine/lifecycle"
)

// eventsHost is the name the change-channel tests declare and then use.
//
// A named host is required rather than convenient: first boot admits no
// upgrade at all, because with nothing named there is no Origin to match and a
// browser would attach ambient cookies to the socket regardless.
const eventsHost = "files.example"

// eventsEngine serves an engine whose app host is declared, with an
// administrator and an ordinary account signed in.
func eventsEngine(t *testing.T) (base string, admin, plain *http.Cookie, e *lifecycle.Engine) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()

	// Saved before the engine that serves it opens: the host list is read at
	// construction, which is the order an operator configures in anyway.
	first, err := lifecycle.Open(ctx, lifecycle.Options{DataDir: dir})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	if merr := first.State.MergeSettings(ctx, "network", map[string]any{
		"app_hosts": []any{eventsHost},
	}); merr != nil {
		t.Fatalf("saving the host list: %v", merr)
	}
	if _, cerr := first.Auth.CreateAdmin(ctx, "root", "Root", pwOf(loginPassword)); cerr != nil {
		t.Fatalf("creating the administrator: %v", cerr)
	}
	if _, cerr := first.Auth.CreateUser(ctx, loginName, "Alice", pwOf(loginPassword)); cerr != nil {
		t.Fatalf("creating the account: %v", cerr)
	}
	if cerr := first.Close(); cerr != nil {
		t.Fatalf("closing: %v", cerr)
	}

	// Not closed by a cleanup: one test closes it itself, and a second close
	// of an engine whose files are already released reports an error the test
	// did not cause.
	opened, oerr := lifecycle.Open(ctx, lifecycle.Options{DataDir: dir})
	if oerr != nil {
		t.Fatalf("reopening: %v", oerr)
	}
	e = opened
	base = serve(t, e)

	return base, hostLogin(t, base, "root"), hostLogin(t, base, loginName), e
}

// hostLogin signs in against the declared host rather than the listener's
// address, which is what the boundary now admits.
func hostLogin(t *testing.T, base, login string) *http.Cookie {
	t.Helper()

	body := `{"login":"` + login + `","password":"` + loginPassword + `"}`
	req, err := http.NewRequest(http.MethodPost, base+"/api/v1/auth/login", strings.NewReader(body))
	if err != nil {
		t.Fatalf("building: %v", err)
	}
	req.Host = eventsHost
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://"+eventsHost)

	resp, err := testClient().Do(req)
	if err != nil {
		t.Fatalf("signing in: %v", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	}()
	for _, c := range resp.Cookies() {
		if strings.Contains(c.Name, "sid") {
			return c
		}
	}
	t.Fatalf("%s did not sign in: %d", login, resp.StatusCode)
	return nil
}

// dialEvents opens the change channel as a signed-in account.
//
// The chain runs on the upgrade like any other request, so the session cookie
// travels on it: this surface is not a second way in.
func dialEvents(t *testing.T, base string, cookie *http.Cookie) *websocket.Conn {
	t.Helper()

	url := "ws" + strings.TrimPrefix(base, "http") + "/api/v1/events"
	header := http.Header{}
	header.Set("Cookie", cookie.String())
	header.Set("Host", eventsHost)
	// An upgrade carries an Origin even though GET is ordinarily a safe
	// method: the socket that follows is a channel a cross-site page would
	// otherwise open carrying the caller's cookie. The boundary refuses
	// without one, which this satisfies rather than bypasses.
	header.Set("Origin", "https://"+eventsHost)

	dialer := *websocket.DefaultDialer
	conn, resp, err := dialer.Dial(url, header)
	if err != nil {
		status := 0
		if resp != nil {
			status = resp.StatusCode
			if cerr := resp.Body.Close(); cerr != nil {
				t.Errorf("closing: %v", cerr)
			}
		}
		t.Fatalf("dialing the change channel: %v (status %d)", err, status)
	}
	if cerr := resp.Body.Close(); cerr != nil {
		t.Errorf("closing: %v", cerr)
	}
	t.Cleanup(func() {
		if cerr := conn.Close(); cerr != nil && !strings.Contains(cerr.Error(), "use of closed") {
			t.Logf("closing the socket: %v", cerr)
		}
	})
	return conn
}

// watchedShare registers a share, grants the caller everything over it, and
// returns the name a client addresses it by with the directory on disk.
//
// The host path is kept because the test writes into the share from outside
// the server, which is exactly what the watcher exists to notice.
func watchedShare(t *testing.T, base string, cookie *http.Cookie, name string) (vpath, hostDir string) {
	t.Helper()
	hostDir = t.TempDir()

	body := `{"name":"` + name + `","host":"` + hostDir + `"}`
	req, err := http.NewRequest(http.MethodPost, base+"/api/v1/admin/shares", strings.NewReader(body))
	if err != nil {
		t.Fatalf("building: %v", err)
	}
	req.Host = eventsHost
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://"+eventsHost)
	req.Header.Set("Sc-Csrf", csrfFor(t, base, cookie))
	req.AddCookie(cookie)

	resp, err := testClient().Do(req)
	if err != nil {
		t.Fatalf("creating the share: %v", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	}()
	raw, rerr := io.ReadAll(resp.Body)
	if rerr != nil {
		t.Fatalf("reading the created share: %v", rerr)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("creating the share answered %d: %s", resp.StatusCode, raw)
	}
	var created map[string]any
	if uerr := json.Unmarshal(raw, &created); uerr != nil {
		t.Fatalf("decoding the created share: %v", uerr)
	}
	shareID, isString := created["id"].(string)
	if !isString || shareID == "" {
		t.Fatalf("the created share carries no id: %s", raw)
	}

	// No grant is written here. Registering a share grants its creator the
	// whole tree under the share's own name, which is exactly what a
	// subscription needs, and asking for a second grant over the same target
	// is now refused as the duplicate it always was.
	assertGranted(t, base, cookie, name)
	return "/" + name, hostDir
}

// assertGranted proves the signed-in account can resolve the share, which is
// what a subscription to a path inside it depends on. A fixture that quietly
// watched a path it could not see would fail somewhere far from the cause.
func assertGranted(t *testing.T, base string, cookie *http.Cookie, label string) {
	t.Helper()

	status, raw := hostGet(t, base, "/api/v1/files/list?path="+urlEscape("/"+label), cookie)
	if status != http.StatusOK {
		t.Fatalf("the creator cannot list its own share: %d: %s", status, raw)
	}
}

// currentUserID reads the signed-in account's id from the session.
func currentUserID(t *testing.T, base string, cookie *http.Cookie) (string, error) {
	t.Helper()

	status, raw := hostGet(t, base, "/api/v1/auth/session", cookie)
	if status != http.StatusOK {
		t.Fatalf("the session answered %d: %s", status, raw)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		return "", err
	}
	id, isString := body["id"].(string)
	if !isString {
		return "", errors.New("the session carries no account id")
	}
	return id, nil
}

// csrfFor reads a fresh token for the session, against the declared host.
func csrfFor(t *testing.T, base string, cookie *http.Cookie) string {
	t.Helper()

	req, err := http.NewRequest(http.MethodGet, base+"/api/v1/auth/session", nil)
	if err != nil {
		t.Fatalf("building: %v", err)
	}
	req.Host = eventsHost
	req.AddCookie(cookie)

	resp, err := testClient().Do(req)
	if err != nil {
		t.Fatalf("reading the session: %v", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	}()
	var body map[string]any
	if derr := json.NewDecoder(resp.Body).Decode(&body); derr != nil {
		t.Fatalf("decoding the session: %v", derr)
	}
	token, isString := body["csrf"].(string)
	if !isString || token == "" {
		t.Fatalf("the session carries no csrf token: %v", body)
	}
	return token
}

// frame is one message in either direction, in the shape both halves speak.
type frame struct {
	Type  string   `json:"t"`
	Paths []string `json:"paths,omitempty"`
	Path  string   `json:"path,omitempty"`
}

// send writes one client frame.
func send(t *testing.T, conn *websocket.Conn, f frame) {
	t.Helper()
	if err := conn.WriteJSON(f); err != nil {
		t.Fatalf("writing a %s frame: %v", f.Type, err)
	}
}

// awaitFrame reads until one arrives or the budget runs out.
//
// The budget is generous: the delivery is debounced, so a frame is not
// expected immediately and a tight bound would fail on a loaded machine
// rather than on a defect.
func awaitFrame(t *testing.T, conn *websocket.Conn, within time.Duration) (frame, bool) {
	t.Helper()
	if err := conn.SetReadDeadline(clock.System().Now().Add(within)); err != nil {
		t.Fatalf("setting the read deadline: %v", err)
	}
	var f frame
	if err := conn.ReadJSON(&f); err != nil {
		return frame{}, false
	}
	return f, true
}

// The change channel needs a credential, like every other authenticated
// surface. It is not a second way in.
func TestTheChangeChannelNeedsACredential(t *testing.T) {
	base, _, _, e := eventsEngine(t)
	defer closeEngine(t, e)

	url := "ws" + strings.TrimPrefix(base, "http") + "/api/v1/events"
	header := http.Header{}
	header.Set("Host", eventsHost)
	header.Set("Origin", "https://"+eventsHost)
	conn, resp, err := websocket.DefaultDialer.Dial(url, header)
	if err == nil {
		if cerr := conn.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
		t.Fatal("an anonymous caller opened the change channel")
	}
	if resp == nil {
		t.Fatalf("the upgrade failed without a response: %v", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	}()
	// Refused as an address that is not there, like every other route this
	// credential cannot reach, so a stranger cannot map the surface.
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("an anonymous upgrade answered %d, want 404", resp.StatusCode)
	}
}

// A plain GET is a client that has not upgraded, and is told so rather than
// left on a stream that will never carry a frame.
func TestAPlainGetOnTheChangeChannelAsksForAnUpgrade(t *testing.T) {
	base, adminCookie, _, e := eventsEngine(t)
	defer closeEngine(t, e)

	status, body := hostGet(t, base, "/api/v1/events", adminCookie)
	if status != http.StatusUpgradeRequired {
		t.Errorf("a plain GET answered %d, want 426: %s", status, body)
	}
}

// A write under a subscribed directory produces one invalidation naming the
// path the client subscribed to.
func TestAChangeUnderASubscribedPathIsDelivered(t *testing.T) {
	base, cookie, _, e := eventsEngine(t)
	defer closeEngine(t, e)
	share, hostDir := watchedShare(t, base, cookie, "docs")

	conn := dialEvents(t, base, cookie)
	send(t, conn, frame{Type: "sub", Paths: []string{share}})

	// The subscribe is applied by the reader on its own goroutine. Writing
	// before it lands would test nothing.
	time.Sleep(300 * time.Millisecond)

	if err := os.WriteFile(filepath.Join(hostDir, "new.txt"), []byte("x"), 0o600); err != nil {
		t.Fatalf("writing into the share: %v", err)
	}

	got, ok := awaitFrame(t, conn, 5*time.Second)
	if !ok {
		t.Fatal("no invalidation arrived for a change under a subscribed path")
	}
	if got.Type != "inval" {
		t.Fatalf("the frame is %q, want inval", got.Type)
	}
	if got.Path != share {
		t.Errorf("the frame names %q, want the path the client subscribed to", got.Path)
	}
}

// A frame carries a path and nothing else.
//
// The client re-fetches, and that re-fetch is what applies the permission the
// caller holds now. Pushing content would deliver what the subscriber was
// entitled to when they subscribed.
func TestAnInvalidationCarriesNoContent(t *testing.T) {
	base, cookie, _, e := eventsEngine(t)
	defer closeEngine(t, e)
	share, hostDir := watchedShare(t, base, cookie, "docs")

	conn := dialEvents(t, base, cookie)
	send(t, conn, frame{Type: "sub", Paths: []string{share}})
	time.Sleep(300 * time.Millisecond)

	const secret = "the-file-contents"
	if err := os.WriteFile(filepath.Join(hostDir, "new.txt"), []byte(secret), 0o600); err != nil {
		t.Fatalf("writing into the share: %v", err)
	}

	if err := conn.SetReadDeadline(clock.System().Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	_, raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("no invalidation arrived: %v", err)
	}

	for _, leak := range []string{secret, "etag", "size", hostDir} {
		if strings.Contains(string(raw), leak) {
			t.Errorf("the frame carries %q: %s", leak, raw)
		}
	}
	// Exactly the two fields the wire defines.
	var decoded map[string]any
	if uerr := json.Unmarshal(raw, &decoded); uerr != nil {
		t.Fatalf("the frame does not parse: %v", uerr)
	}
	for key := range decoded {
		if key != "t" && key != "path" {
			t.Errorf("the frame carries an unexpected field %q: %s", key, raw)
		}
	}
}

// A grant revoked after the subscribe stops delivery.
//
// This is the property the two permission checks exist for. Checking only at
// subscribe would keep delivering to an account whose access was withdrawn
// while its tab was open, which is a revocation the client never learns about
// and the server keeps ignoring.
func TestRevokingAGrantStopsDelivery(t *testing.T) {
	base, adminCookie, plainCookie, e := eventsEngine(t)
	defer closeEngine(t, e)
	share, hostDir := watchedShare(t, base, adminCookie, "docs")

	// The ordinary account is granted the share, so it can genuinely
	// subscribe: without the grant the path resolves to nothing and the test
	// would pass whether the checks ran or not.
	grantID := grantShareTo(t, base, adminCookie, plainCookie, "docs")

	conn := dialEvents(t, base, plainCookie)
	send(t, conn, frame{Type: "sub", Paths: []string{share}})
	time.Sleep(300 * time.Millisecond)

	// Delivery works while the grant stands.
	if err := os.WriteFile(filepath.Join(hostDir, "before.txt"), []byte("x"), 0o600); err != nil {
		t.Fatalf("writing into the share: %v", err)
	}
	if _, ok := awaitFrame(t, conn, 5*time.Second); !ok {
		t.Fatal("nothing was delivered while the grant stood")
	}

	revokeGrant(t, base, adminCookie, grantID)

	if err := os.WriteFile(filepath.Join(hostDir, "after.txt"), []byte("x"), 0o600); err != nil {
		t.Fatalf("writing into the share: %v", err)
	}
	if _, ok := awaitFrame(t, conn, 3*time.Second); ok {
		t.Error("a revoked account was still told about a change")
	}
}

// grantShareTo gives one account read access to a share and returns the grant.
func grantShareTo(t *testing.T, base string, adminCookie, holder *http.Cookie, label string) string {
	t.Helper()

	status, raw := hostGet(t, base, "/api/v1/admin/shares", adminCookie)
	if status != http.StatusOK {
		t.Fatalf("listing shares answered %d: %s", status, raw)
	}
	var shares []map[string]any
	if err := json.Unmarshal(raw, &shares); err != nil {
		t.Fatalf("decoding the share listing: %v", err)
	}
	if len(shares) == 0 {
		t.Fatal("no share to grant")
	}
	shareID, isString := shares[0]["id"].(string)
	if !isString {
		t.Fatalf("the share carries no id: %v", shares[0])
	}

	who, err := currentUserID(t, base, holder)
	if err != nil {
		t.Fatalf("reading the holder's id: %v", err)
	}

	body := `{"share":"` + shareID + `","user":"` + who + `","label":"` + label +
		`","allow":["read","download"]}`
	code, created := hostPost(t, base, "/api/v1/admin/grants", adminCookie, body)
	if code != http.StatusCreated {
		t.Fatalf("granting answered %d: %s", code, created)
	}
	var out map[string]any
	if uerr := json.Unmarshal(created, &out); uerr != nil {
		t.Fatalf("decoding the grant: %v", uerr)
	}
	id, isString := out["id"].(string)
	if !isString || id == "" {
		t.Fatalf("the grant carries no id: %s", created)
	}
	return id
}

// revokeGrant deletes one.
func revokeGrant(t *testing.T, base string, adminCookie *http.Cookie, id string) {
	t.Helper()

	req, err := http.NewRequest(http.MethodDelete, base+"/api/v1/admin/grants/"+id, nil)
	if err != nil {
		t.Fatalf("building: %v", err)
	}
	req.Host = eventsHost
	req.Header.Set("Origin", "https://"+eventsHost)
	req.Header.Set("Sc-Csrf", csrfFor(t, base, adminCookie))
	req.AddCookie(adminCookie)

	resp, derr := testClient().Do(req)
	if derr != nil {
		t.Fatalf("revoking: %v", derr)
	}
	code := resp.StatusCode
	if cerr := resp.Body.Close(); cerr != nil {
		t.Errorf("closing: %v", cerr)
	}
	if code != http.StatusOK && code != http.StatusNoContent {
		t.Fatalf("revoking answered %d", code)
	}
}

// hostPost sends a JSON body against the declared host.
func hostPost(t *testing.T, base, path string, cookie *http.Cookie, body string) (int, []byte) {
	t.Helper()

	req, err := http.NewRequest(http.MethodPost, base+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("building: %v", err)
	}
	req.Host = eventsHost
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://"+eventsHost)
	req.Header.Set("Sc-Csrf", csrfFor(t, base, cookie))
	req.AddCookie(cookie)

	resp, derr := testClient().Do(req)
	if derr != nil {
		t.Fatalf("requesting %s: %v", path, derr)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	}()
	raw, rerr := io.ReadAll(resp.Body)
	if rerr != nil {
		t.Fatalf("reading %s: %v", path, rerr)
	}
	return resp.StatusCode, raw
}

// Subscribing to a path the caller cannot read is refused rather than pinned.
//
// Silently, because the alternative tells the caller whether a directory they
// may not read exists.
func TestSubscribingToAnUnreadablePathDeliversNothing(t *testing.T) {
	base, adminCookie, plainCookie, e := eventsEngine(t)
	defer closeEngine(t, e)
	share, hostDir := watchedShare(t, base, adminCookie, "docs")

	// The ordinary account holds no grant over this share.
	conn := dialEvents(t, base, plainCookie)
	send(t, conn, frame{Type: "sub", Paths: []string{share}})
	time.Sleep(300 * time.Millisecond)

	if err := os.WriteFile(filepath.Join(hostDir, "new.txt"), []byte("x"), 0o600); err != nil {
		t.Fatalf("writing into the share: %v", err)
	}

	if _, ok := awaitFrame(t, conn, 2*time.Second); ok {
		t.Error("an account with no grant was told about a change")
	}
}

// A ping is answered, which is what keeps a connection through an idle proxy.
func TestAPingIsAnswered(t *testing.T) {
	base, cookie, _, e := eventsEngine(t)
	defer closeEngine(t, e)

	conn := dialEvents(t, base, cookie)
	send(t, conn, frame{Type: "ping"})

	got, ok := awaitFrame(t, conn, 5*time.Second)
	if !ok {
		t.Fatal("a ping went unanswered")
	}
	if got.Type != "pong" {
		t.Errorf("a ping was answered with %q", got.Type)
	}
}

// An unreadable frame ends the conversation rather than being skipped.
func TestAMalformedFrameClosesTheConnection(t *testing.T) {
	base, cookie, _, e := eventsEngine(t)
	defer closeEngine(t, e)

	conn := dialEvents(t, base, cookie)
	if err := conn.WriteMessage(websocket.TextMessage, []byte("{not json")); err != nil {
		t.Fatalf("writing: %v", err)
	}

	// The deadline is generous but the close has to be what ends the read.
	// A test that let the deadline expire would pass just as well against a
	// server that ignored the frame and kept the connection open, which is
	// the behaviour this is here to refuse.
	if err := conn.SetReadDeadline(clock.System().Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	_, _, err := conn.ReadMessage()
	if err == nil {
		t.Fatal("the connection survived a frame the server cannot read")
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		t.Errorf("the read timed out rather than the server closing: %v", err)
	}
}

// Unsubscribing stops delivery.
func TestUnsubscribingStopsDelivery(t *testing.T) {
	base, cookie, _, e := eventsEngine(t)
	defer closeEngine(t, e)
	share, hostDir := watchedShare(t, base, cookie, "docs")

	conn := dialEvents(t, base, cookie)
	send(t, conn, frame{Type: "sub", Paths: []string{share}})
	time.Sleep(300 * time.Millisecond)
	send(t, conn, frame{Type: "unsub", Paths: []string{share}})
	time.Sleep(300 * time.Millisecond)

	if err := os.WriteFile(filepath.Join(hostDir, "new.txt"), []byte("x"), 0o600); err != nil {
		t.Fatalf("writing into the share: %v", err)
	}

	if _, ok := awaitFrame(t, conn, 2*time.Second); ok {
		t.Error("an unsubscribed path still delivered")
	}
}

// Closing the engine with a socket open releases everything rather than
// hanging on a connection nobody is reading.
func TestClosingTheEngineReleasesOpenSockets(t *testing.T) {
	base, cookie, _, e := eventsEngine(t)
	dialEvents(t, base, cookie)

	// Closing runs through the same path a shutdown takes. It has to release
	// the socket rather than wait on a peer that is not reading.
	done := make(chan error, 1)
	task.Go(context.Background(), "engine close", func() { done <- e.Close() })

	select {
	case cerr := <-done:
		if cerr != nil {
			t.Errorf("closing with a socket open: %v", cerr)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("closing hung with a socket open")
	}
}

// hostGet reads a route against the declared host, which is the only one the
// boundary admits once a host list is saved.
func hostGet(t *testing.T, base, path string, cookie *http.Cookie) (int, []byte) {
	t.Helper()

	req, err := http.NewRequest(http.MethodGet, base+path, nil)
	if err != nil {
		t.Fatalf("building: %v", err)
	}
	req.Host = eventsHost
	req.AddCookie(cookie)

	resp, err := testClient().Do(req)
	if err != nil {
		t.Fatalf("requesting %s: %v", path, err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	}()
	body, rerr := io.ReadAll(resp.Body)
	if rerr != nil {
		t.Fatalf("reading %s: %v", path, rerr)
	}
	return resp.StatusCode, body
}

// closeEngine releases an engine a test opened, tolerating one a test already
// closed itself.
func closeEngine(t *testing.T, e *lifecycle.Engine) {
	t.Helper()
	if err := e.Close(); err != nil {
		t.Errorf("closing: %v", err)
	}
}

// A disconnect releases every pin the connection held.
//
// A subscription pins a directory into the watcher's sticky half, which is the
// one thing the hot set never evicts on its own. The old implementation
// removed the socket and left the pins, so every tab that closed cost a kernel
// watch for the life of the process.
func TestADisconnectReleasesEverySubscription(t *testing.T) {
	base, cookie, _, e := eventsEngine(t)
	defer closeEngine(t, e)
	share, _ := watchedShare(t, base, cookie, "docs")

	before := e.PinnedDirectoriesForTest()

	conn := dialEvents(t, base, cookie)
	send(t, conn, frame{Type: "sub", Paths: []string{share}})
	time.Sleep(300 * time.Millisecond)

	if during := e.PinnedDirectoriesForTest(); during <= before {
		t.Fatalf("subscribing pinned nothing: %d pinned, was %d", during, before)
	}

	if err := conn.Close(); err != nil {
		t.Fatalf("closing the socket: %v", err)
	}
	// The release runs on the connection's own goroutine once the read fails.
	time.Sleep(700 * time.Millisecond)

	if after := e.PinnedDirectoriesForTest(); after != before {
		t.Errorf("a closed connection left %d directories pinned, want %d", after, before)
	}
}

// A change is reported against the share it happened in.
//
// Both shares are subscribed, so both are watched and the only thing telling
// the two events apart is the match. A client told the wrong path re-fetches a
// folder nothing happened in and never re-fetches the one that changed.
//
// Subscribing to both is what makes this discriminate. A share nobody
// subscribed to carries no kernel watch, so it emits nothing at all and the
// match is never reached.
func TestAChangeIsReportedAgainstTheShareItHappenedIn(t *testing.T) {
	base, cookie, _, e := eventsEngine(t)
	defer closeEngine(t, e)

	docs, _ := watchedShare(t, base, cookie, "docs")
	photos, photoDir := watchedShare(t, base, cookie, "photos")

	conn := dialEvents(t, base, cookie)
	send(t, conn, frame{Type: "sub", Paths: []string{docs, photos}})
	time.Sleep(400 * time.Millisecond)

	if err := os.WriteFile(filepath.Join(photoDir, "new.jpg"), []byte("x"), 0o600); err != nil {
		t.Fatalf("writing into the second share: %v", err)
	}

	got, ok := awaitFrame(t, conn, 5*time.Second)
	if !ok {
		t.Fatal("nothing was delivered for a change in a subscribed share")
	}
	if got.Path != photos {
		t.Errorf("a change in %s was reported as %q", photos, got.Path)
	}
}

// Subscribing twice to one path takes one pin, and one unsubscribe releases it.
//
// The watcher refcounts pins, so a second subscribe that pinned again would
// take a reference the single unsubscribe never returns: the directory would
// stay in the half nothing evicts for the life of the process.
func TestSubscribingTwiceTakesOnePin(t *testing.T) {
	base, cookie, _, e := eventsEngine(t)
	defer closeEngine(t, e)
	share, _ := watchedShare(t, base, cookie, "docs")

	before := e.PinnedDirectoriesForTest()

	conn := dialEvents(t, base, cookie)
	send(t, conn, frame{Type: "sub", Paths: []string{share}})
	time.Sleep(250 * time.Millisecond)
	send(t, conn, frame{Type: "sub", Paths: []string{share}})
	time.Sleep(250 * time.Millisecond)

	if during := e.PinnedDirectoriesForTest(); during <= before {
		t.Fatalf("subscribing pinned nothing: %d, was %d", during, before)
	}

	send(t, conn, frame{Type: "unsub", Paths: []string{share}})
	time.Sleep(400 * time.Millisecond)

	if after := e.PinnedDirectoriesForTest(); after != before {
		t.Errorf("one unsubscribe left %d pinned, want %d: a repeated subscribe "+
			"took a reference nothing releases", after, before)
	}
}

// A change is reported against the directory it happened in.
//
// Subscribing pins the whole ancestor chain, so the share root carries a
// kernel watch here too and both directories produce events. Only the match
// separates them: a client told its subdirectory changed when the write landed
// beside it re-fetches the wrong folder and misses nothing it was watching.
func TestAChangeIsReportedAgainstTheDirectoryItHappenedIn(t *testing.T) {
	base, cookie, _, e := eventsEngine(t)
	defer closeEngine(t, e)
	share, hostDir := watchedShare(t, base, cookie, "docs")

	sub := filepath.Join(hostDir, "reports")
	if err := os.MkdirAll(sub, 0o750); err != nil {
		t.Fatalf("making the subdirectory: %v", err)
	}
	// Resolved once through the API, so the path is known before the
	// subscription asks about it.
	if status, body := hostGet(t, base,
		"/api/v1/files/list?path=%2Fdocs%2Freports", cookie); status != http.StatusOK {
		t.Fatalf("listing the subdirectory answered %d: %s", status, body)
	}

	conn := dialEvents(t, base, cookie)
	send(t, conn, frame{Type: "sub", Paths: []string{share + "/reports"}})
	time.Sleep(400 * time.Millisecond)

	// Beside the subscribed directory rather than inside it. The first frame
	// to arrive settles the question: with the match in place nothing is
	// delivered at all, and without it this write is what arrives.
	if err := os.WriteFile(filepath.Join(hostDir, "root.txt"), []byte("x"), 0o600); err != nil {
		t.Fatalf("writing at the share root: %v", err)
	}
	if got, ok := awaitFrame(t, conn, 3*time.Second); ok {
		t.Fatalf("a write beside the subscribed directory was delivered as %q", got.Path)
	}
}

// A change inside the subscribed directory is delivered, naming it.
func TestAChangeInsideTheSubscribedDirectoryIsDelivered(t *testing.T) {
	base, cookie, _, e := eventsEngine(t)
	defer closeEngine(t, e)
	share, hostDir := watchedShare(t, base, cookie, "docs")

	sub := filepath.Join(hostDir, "reports")
	if err := os.MkdirAll(sub, 0o750); err != nil {
		t.Fatalf("making the subdirectory: %v", err)
	}
	if status, body := hostGet(t, base,
		"/api/v1/files/list?path=%2Fdocs%2Freports", cookie); status != http.StatusOK {
		t.Fatalf("listing the subdirectory answered %d: %s", status, body)
	}

	conn := dialEvents(t, base, cookie)
	send(t, conn, frame{Type: "sub", Paths: []string{share + "/reports"}})
	time.Sleep(400 * time.Millisecond)

	if err := os.WriteFile(filepath.Join(sub, "inside.txt"), []byte("x"), 0o600); err != nil {
		t.Fatalf("writing into the subdirectory: %v", err)
	}
	got, ok := awaitFrame(t, conn, 5*time.Second)
	if !ok {
		t.Fatal("a change in the subscribed directory was not delivered")
	}
	if got.Path != share+"/reports" {
		t.Errorf("the change was reported as %q", got.Path)
	}
}
