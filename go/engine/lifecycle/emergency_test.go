//go:build linux

package lifecycle_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// The repair door answers, and answers outside the middleware chain.
//
// That placement is the whole point: it is what an operator reaches when the
// chain's own configuration is what is broken. A door behind the boundary
// check it exists to repair is a door nobody can open on the day it matters.
func TestTheRepairDoorIsReachable(t *testing.T) {
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

// A forwarded header cannot move the caller off the loopback.
//
// The door admits private addresses. A header claiming a public one has to be
// ignored, because the peer is not a trusted proxy: honouring it would let
// anyone reaching this port change what address the guard sees. The test
// asserts the direction that is safe to assert from a loopback client, which
// is that the answer does not change.
func TestTheDoorIgnoresAnUntrustedForwardedHeader(t *testing.T) {
	base := boot(t)

	plain, plainBody := doorRequest(t, http.MethodGet, base+"/emergency/api/state", nil, "")
	forged, forgedBody := doorRequest(t, http.MethodGet, base+"/emergency/api/state", nil, "203.0.113.7")

	if plain != forged {
		t.Errorf("a forwarded header changed the answer from %d to %d, so the header is being trusted",
			plain, forged)
	}
	if string(plainBody) != string(forgedBody) {
		t.Error("a forwarded header changed the response body")
	}
}

// The door is not a way past the ordinary authorization.
//
// It serves login, the settings and a restart. Anything else has to be absent
// from it, or an unauthenticated caller on the loopback would have a second
// entrance into the product with none of the checks.
func TestTheDoorServesNothingElse(t *testing.T) {
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
	base, token, _ := bootWithUser(t)

	status, body := authed(t, http.MethodGet, base+"/api/v1/files/list?path=/", token)
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
