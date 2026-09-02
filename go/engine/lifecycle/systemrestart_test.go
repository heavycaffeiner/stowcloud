//go:build linux

package lifecycle_test

import (
	"net/http"
	"testing"
)

// The restart answers before it goes down.
//
// A client that gets a dropped socket cannot tell a restart from a crash, and
// the screen that asked for one has nothing to report but a network error. So
// the answer is written first and the process replaces itself after.
//
// The test process is not replaced because nothing wired a replacement: the
// engine reports that a restart is wanted and the process it is mounted on
// decides how, which is what keeps this testable at all.
func TestARestartIsAnsweredBeforeItHappens(t *testing.T) {
	base, cookie, csrf, _, _ := adminEngine(t)

	status, body := mutate(t, http.MethodPost, base+"/api/v1/admin/system/restart",
		cookie, csrf, nil)
	if status != http.StatusAccepted {
		t.Fatalf("the restart answered %d, want 202: %v", status, body)
	}
	restarting, ok := body["restarting"].(bool)
	if !ok || !restarting {
		t.Errorf("the answer does not say it is restarting: %v", body)
	}

	// The counts are always present. A client that has to tell "nothing
	// running" from "the server did not say" reads the same absent field twice.
	for _, key := range []string{"active_uploads", "active_jobs"} {
		if _, ok := body[key]; !ok {
			t.Errorf("the answer omits %q, so a warning cannot say what it would interrupt", key)
		}
	}
	// Still serving. The engine asks; nothing here performs it.
	after, _ := withCookie(t, http.MethodGet, base+"/api/v1/jobs", cookie)
	if after != http.StatusOK {
		t.Errorf("the server answered %d after a restart with nothing wired to perform it", after)
	}
}

// Taking the server down is not an ordinary account's to do.
//
// Every other admin route checks this and the guard is per handler rather than
// per route category, so a new one that forgot would be a denial of service
// available to anybody who can log in.
func TestARestartNeedsAnAdministrator(t *testing.T) {
	base, _, _, plain, plainCSRF := adminEngine(t)

	status, body := mutate(t, http.MethodPost, base+"/api/v1/admin/system/restart",
		plain, plainCSRF, nil)
	if status != http.StatusForbidden {
		t.Fatalf("an ordinary account restarting answered %d, want 403: %v", status, body)
	}
}

// And not an anonymous caller's either.
func TestARestartRefusesAnonymously(t *testing.T) {
	base, _, _, _, _ := adminEngine(t)

	status, _ := get(t, base+"/api/v1/admin/system/restart")
	if status != http.StatusUnauthorized && status != http.StatusForbidden &&
		status != http.StatusMethodNotAllowed {
		t.Errorf("an anonymous restart answered %d", status)
	}
}

// A restart cannot serve a change the kernel forbids.
//
// Landlock is stackable and narrowing-only and a seccomp filter has no removal
// syscall, and both survive execve. So the image that replaces this one starts
// under the sandbox this one installed, whatever the stored setting now says.
// Answering 202 there would report a change as applied while the old policy
// still governed the process.
//
// Refused rather than exited, so the deployment is still up afterwards: an
// exit would apply the change and leave the server down for as long as it
// takes somebody to notice.
func TestARestartRefusesToLoosenTheSandbox(t *testing.T) {
	base, cookie, csrf, _, _ := adminEngine(t)

	// The test process installed nothing, which is the strictest reading the
	// engine has, so anything weaker than that is a loosening change.
	status, body := mutate(t, http.MethodPatch, base+"/api/v1/admin/settings/security",
		cookie, csrf, map[string]any{"hardening": "off"})
	if status != http.StatusOK {
		t.Fatalf("storing the policy answered %d: %v", status, body)
	}

	status, body = mutate(t, http.MethodPost, base+"/api/v1/admin/system/restart",
		cookie, csrf, nil)
	if status != http.StatusConflict {
		t.Fatalf("a restart that cannot loosen answered %d, want 409: %v", status, body)
	}

	// And it is still serving, which is the whole reason this refuses rather
	// than exiting.
	after, _ := withCookie(t, http.MethodGet, base+"/api/v1/jobs", cookie)
	if after != http.StatusOK {
		t.Errorf("the server answered %d after refusing a restart", after)
	}
}
