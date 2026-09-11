//go:build linux

package lifecycle_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	emergencyHTTP "github.com/heavycaffeiner/stowcloud/go/engine/http/emergency"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/secret"
	"github.com/heavycaffeiner/stowcloud/go/engine/lifecycle"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/auth"
)

// The repair door answers, and answers outside the middleware chain.
//
// That placement is the whole point: it is what an operator reaches when the
// chain's own configuration is what is broken. A door behind the boundary
// check it exists to repair is a door nobody can open on the day it matters.
func TestTheRepairDoorIsReachable(t *testing.T) {
	t.Parallel()
	base := boot(t)

	status, body := doorRequest(t, http.MethodGet, base+"/emergency/api/state", nil, "")
	if status != http.StatusOK {
		t.Fatalf("the door answered %d: %s", status, body)
	}

	var state map[string]any
	if err := json.Unmarshal(body, &state); err != nil {
		t.Fatalf("decoding %s: %v", body, err)
	}

	if len(state) == 0 {
		t.Error("the door reports nothing about the deployment")
	}
}

// The repair door accepts the same one-use recovery factor as ordinary
// sign-in, while still requiring the administrator password first. A second
// attempt with the code is refused because consuming it is part of the
// authentication operation.
func TestEmergencyLoginAcceptsRecoveryCodeOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e, err := lifecycle.Open(ctx, lifecycle.Options{DataDir: t.TempDir(), PasswordParams: fastPasswordParams()})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	t.Cleanup(func() {
		if cerr := e.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	})

	id, err := e.Auth.CreateAdmin(ctx, "root", "Root", secret.New([]byte("a-long-enough-password")))
	if err != nil {
		t.Fatalf("creating the administrator: %v", err)
	}
	enrol(t, e, id)
	codes, err := e.Auth.GenerateRecoveryCodes(ctx, id, 1)
	if err != nil || len(codes) != 1 {
		t.Fatalf("generating a recovery code: %v", err)
	}
	base := serve(t, e)
	login := func() (int, []byte) {
		return doorRequest(t, http.MethodPost, base+"/emergency/api/login",
			[]byte(`{"username":"root","password":"a-long-enough-password","factor":"`+codes[0]+`"}`), "")
	}

	status, body := login()
	if status != http.StatusOK {
		t.Fatalf("a valid recovery code was refused: %d %s", status, body)
	}
	var result map[string]any
	if uerr := json.Unmarshal(body, &result); uerr != nil {
		t.Fatalf("decoding the login result: %v", uerr)
	}
	if result["status"] != "ok" {
		t.Fatalf("the recovery login answered %v", result["status"])
	}

	rows, _, err := e.Auth.AuditPage(ctx, auth.AuditFilter{})
	if err != nil {
		t.Fatalf("reading the audit log: %v", err)
	}
	if len(rows) != 1 || rows[0].Event != emergencyHTTP.EventLogin || !rows[0].OK {
		t.Fatalf("the recovery login produced the wrong audit flow: %+v", rows)
	}
	if status, _ := login(); status == http.StatusOK {
		t.Fatal("the recovery code was accepted twice")
	}
}

// A valid TOTP completes the emergency login without also recording an
// ordinary login attempt. The door should produce one authentication event.
func TestEmergencyLoginAcceptsTOTPOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e, err := lifecycle.Open(ctx, lifecycle.Options{DataDir: t.TempDir(), PasswordParams: fastPasswordParams()})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	t.Cleanup(func() {
		if cerr := e.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	})

	id, err := e.Auth.CreateAdmin(ctx, "root", "Root", secret.New([]byte("a-long-enough-password")))
	if err != nil {
		t.Fatalf("creating the administrator: %v", err)
	}
	secretB32 := enrol(t, e, id)
	base := serve(t, e)

	status, body := doorRequest(t, http.MethodPost, base+"/emergency/api/login",
		[]byte(`{"username":"root","password":"a-long-enough-password","factor":"`+
			referenceCode(t, secretB32, nowStep())+`"}`), "")
	if status != http.StatusOK {
		t.Fatalf("a valid TOTP was refused: %d %s", status, body)
	}
	var result map[string]any
	if uerr := json.Unmarshal(body, &result); uerr != nil {
		t.Fatalf("decoding the login result: %v", uerr)
	}
	if result["status"] != "ok" {
		t.Fatalf("the TOTP login answered %v", result["status"])
	}

	rows, _, err := e.Auth.AuditPage(ctx, auth.AuditFilter{})
	if err != nil {
		t.Fatalf("reading the audit log: %v", err)
	}
	if len(rows) != 1 || rows[0].Event != emergencyHTTP.EventLogin || !rows[0].OK {
		t.Fatalf("the TOTP login produced the wrong audit flow: %+v", rows)
	}
}

// A recovery login has only one password attempt. Nine preceding password
// checks leave exactly one service attempt, so a second Login call would
// refuse the valid recovery code as rate limited.
func TestEmergencyRecoveryDoesNotSpendASecondLoginAttempt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e, err := lifecycle.Open(ctx, lifecycle.Options{DataDir: t.TempDir(), PasswordParams: fastPasswordParams()})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	t.Cleanup(func() {
		if cerr := e.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	})

	id, err := e.Auth.CreateAdmin(ctx, "root", "Root", secret.New([]byte("a-long-enough-password")))
	if err != nil {
		t.Fatalf("creating the administrator: %v", err)
	}
	enrol(t, e, id)
	codes, err := e.Auth.GenerateRecoveryCodes(ctx, id, 1)
	if err != nil || len(codes) != 1 {
		t.Fatalf("generating a recovery code: %v", err)
	}

	for i := range 9 {
		_, loginErr := e.Auth.Login(ctx, auth.LoginRequest{
			Name: "root", Password: secret.New([]byte("a-long-enough-password")),
			IP: "127.0.0.1",
		}, 0)
		if !errors.Is(loginErr, auth.ErrSecondFactor) {
			t.Fatalf("password attempt %d returned %v", i+1, loginErr)
		}
	}

	base := serve(t, e)
	status, body := doorRequest(t, http.MethodPost, base+"/emergency/api/login",
		[]byte(`{"username":"root","password":"a-long-enough-password","factor":"`+codes[0]+`"}`), "")
	if status != http.StatusOK {
		t.Fatalf("the valid recovery code was rate limited: %d %s", status, body)
	}
}

// A recovery code never replaces the password check. A refused password also
// leaves the code available for the account holder.
func TestEmergencyRecoveryDoesNotBypassPassword(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e, err := lifecycle.Open(ctx, lifecycle.Options{DataDir: t.TempDir(), PasswordParams: fastPasswordParams()})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	t.Cleanup(func() {
		if cerr := e.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	})

	id, err := e.Auth.CreateAdmin(ctx, "root", "Root", secret.New([]byte("a-long-enough-password")))
	if err != nil {
		t.Fatalf("creating the administrator: %v", err)
	}
	enrol(t, e, id)
	codes, err := e.Auth.GenerateRecoveryCodes(ctx, id, 1)
	if err != nil || len(codes) != 1 {
		t.Fatalf("generating a recovery code: %v", err)
	}
	base := serve(t, e)
	login := func(password string) (int, []byte) {
		return doorRequest(t, http.MethodPost, base+"/emergency/api/login",
			[]byte(`{"username":"root","password":"`+password+`","factor":"`+codes[0]+`"}`), "")
	}

	if status, body := login("wrong-password-value"); status == http.StatusOK {
		t.Fatalf("wrong password bypassed authentication: %s", body)
	}
	if status, body := login("a-long-enough-password"); status != http.StatusOK {
		t.Fatalf("the recovery code was consumed by the refused password: %d %s", status, body)
	}
}

// doorRequest sends a request with an optional forwarded header, which is the
// thing the guard has to be unwilling to trust.
func doorRequest(t *testing.T, method, url string, body []byte, forwarded string) (int, []byte) {
	t.Helper()

	var reader *strings.Reader
	if body == nil {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(string(body))
	}

	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatalf("building: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if forwarded != "" {
		req.Header.Set("X-Forwarded-For", forwarded)
	}

	resp, err := testClient().Do(req)
	if err != nil {
		t.Fatalf("requesting %s: %v", url, err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	}()

	out := make([]byte, 0, 1024)
	buf := make([]byte, 1024)
	for {
		n, rerr := resp.Body.Read(buf)
		out = append(out, buf[:n]...)
		if rerr != nil {
			break
		}
	}
	return resp.StatusCode, out
}

// A forwarded header from a peer that is not on the internet moves the caller.
//
// The door admits private addresses only. With no proxy list configured a
// private peer is trusted, which is what a tunnel daemon or a sidecar proxy
// looks like: the visitor behind it is the address in the header, and a public
// one has to be refused. Reading the peer instead would put the repair door in
// front of the whole internet on every such deployment.
func TestTheDoorRefusesAnInternetVisitorBehindALocalProxy(t *testing.T) {
	t.Parallel()
	base := boot(t)

	plain, _ := doorRequest(t, http.MethodGet, base+"/emergency/api/state", nil, "")
	if plain != http.StatusOK {
		t.Fatalf("the loopback caller answered %d", plain)
	}

	public, _ := doorRequest(t, http.MethodGet, base+"/emergency/api/state", nil, "203.0.113.7")
	if public == http.StatusOK {
		t.Error("an internet visitor forwarded by a local proxy reached the door")
	}

	// A private visitor behind that same proxy is still on the local network,
	// so the door stays open to them.
	private, _ := doorRequest(t, http.MethodGet, base+"/emergency/api/state", nil, "192.168.0.7")
	if private != http.StatusOK {
		t.Errorf("a private visitor behind a local proxy answered %d", private)
	}
}

// The door is not a way past the ordinary authorization.
//
// It serves login, the settings and a restart. Anything else has to be absent
// from it, or an unauthenticated caller on the loopback would have a second
// entrance into the product with none of the checks.
func TestTheDoorServesNothingElse(t *testing.T) {
	t.Parallel()
	base := boot(t)

	for _, path := range []string{
		"/emergency/api/users",
		"/emergency/api/files",
		"/emergency/api/v1/admin/users",
		"/emergency/api/shares",
		"/emergency/../api/v1/admin/users",
	} {
		status, body := doorRequest(t, http.MethodGet, base+path, nil, "")
		if status == http.StatusOK {
			t.Errorf("%s answered 200 through the repair door: %s", path, body)
		}
	}
}

// The settings behind the door need a session, like everywhere else.
//
// The door lowers the network guard, not the credential one: reaching the
// screen is not the same as being allowed to read or change the deployment.
//
// The paths are the door's own, `GET /api/settings` and
// `PATCH /api/settings/{section}`. An earlier version of this test posted to
// `/api/settings`, which the door does not route, and passed on the 405: it
// proved the method was wrong rather than that anything was guarded.
func TestTheDoorStillRequiresACredentialForSettings(t *testing.T) {
	t.Parallel()
	base := boot(t)

	read, readBody := doorRequest(t, http.MethodGet, base+"/emergency/api/settings", nil, "")
	if read != http.StatusUnauthorized {
		t.Errorf("reading the settings answered %d, want 401: %s", read, readBody)
	}

	write, writeBody := doorRequest(t, http.MethodPatch,
		base+"/emergency/api/settings/server", []byte(`{"listen":"127.0.0.1:1"}`), "")
	if write != http.StatusUnauthorized {
		t.Errorf("writing a section answered %d, want 401: %s", write, writeBody)
	}

	// A restart is the most destructive thing behind this door, so it is
	// checked separately rather than assumed to share the gate.
	restart, restartBody := doorRequest(t, http.MethodPost, base+"/emergency/api/restart", nil, "")
	if restart == http.StatusOK || restart == http.StatusNoContent {
		t.Errorf("an unauthenticated restart answered %d: %s", restart, restartBody)
	}
}

// The ordinary API is unaffected by the door being mounted in front of it.
//
// The door claims one prefix. If it claimed more, or fell through wrongly,
// every other route would stop working, and mounting it before the chain is
// exactly the kind of change that could do that.
func TestMountingTheDoorLeavesTheAPIAlone(t *testing.T) {
	t.Parallel()
	base, _, sess := bootWithUser(t)

	status, body := authed(t, http.MethodGet, base+"/api/v1/files/list?path=/", sess)
	if status != http.StatusOK {
		t.Fatalf("the ordinary API answered %d behind the door: %s", status, body)
	}

	// The chain still runs on those routes, which is what the door must not
	// have displaced.
	resp, err := testClient().Get(base + "/api/v1/system/health")
	if err != nil {
		t.Fatalf("requesting: %v", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	}()
	if resp.Header.Get("Content-Security-Policy") == "" {
		t.Error("the security headers are gone, so the chain no longer runs")
	}
}
