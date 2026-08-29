//go:build linux

package lifecycle_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

// A section saves and the document reports it back.
func TestSavingASettingsSection(t *testing.T) {
	base, cookie, csrf, _, _ := adminEngine(t)

	status, body := mutate(t, http.MethodPatch, base+"/api/v1/admin/settings/rate",
		cookie, csrf, map[string]any{"per_sec": 30, "burst": 90})
	if status != http.StatusOK {
		t.Fatalf("saving answered %d: %v", status, body)
	}
	if !boolField(body, "stored") {
		t.Errorf("the save does not report itself stored: %v", body)
	}

	// Read back through the document, which is what a screen reloads.
	code, raw := withCookie(t, http.MethodGet, base+"/api/v1/admin/settings", cookie)
	if code != http.StatusOK {
		t.Fatalf("reading answered %d: %s", code, raw)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decoding %s: %v", raw, err)
	}
	section, ok := doc["rate"].(map[string]any)
	if !ok {
		t.Fatalf("the saved section is not in the document: %s", raw)
	}
	if fmt.Sprint(section["per_sec"]) != "30" {
		t.Errorf("the stored rate is %v, want 30", section["per_sec"])
	}
}

// A live section says it is applied; a pinned one says a restart is needed.
//
// Those are different answers to "did my change take effect", and folding
// them together is how an administrator spends an afternoon on a setting that
// was stored and never running.
func TestASaveSaysWhetherItIsLive(t *testing.T) {
	base, cookie, csrf, _, _ := adminEngine(t)

	live, liveBody := mutate(t, http.MethodPatch, base+"/api/v1/admin/settings/rate",
		cookie, csrf, map[string]any{"per_sec": 30, "burst": 90})
	if live != http.StatusOK {
		t.Fatalf("the live section answered %d: %v", live, liveBody)
	}
	if !boolField(liveBody, "applied") {
		t.Error("a section the chain reads per request is not reported as applied")
	}
	if boolField(liveBody, "restart_required") {
		t.Error("a live section asks for a restart")
	}

	// The watcher takes its bounds when it starts, so this one is pinned.
	pinned, pinnedBody := mutate(t, http.MethodPatch, base+"/api/v1/admin/settings/watch",
		cookie, csrf, map[string]any{"hot_set_max": 4096})
	if pinned != http.StatusOK {
		t.Fatalf("the pinned section answered %d: %v", pinned, pinnedBody)
	}
	if !boolField(pinnedBody, "stored") {
		t.Error("a pinned section was not stored")
	}
	if !boolField(pinnedBody, "restart_required") {
		t.Error("a section decided at startup does not ask for a restart")
	}
	if boolField(pinnedBody, "applied") {
		t.Error("a change needing a restart reports itself applied, which is a clean application of something not running")
	}
}

// A live save reaches the running server, not just the database.
//
// This is the claim the "applied" field makes, so it is checked by observing
// the behaviour rather than by reading the field back.
func TestALiveSaveTakesEffectImmediately(t *testing.T) {
	base, cookie, csrf, _, _ := adminEngine(t)

	// A burst small enough that a short loop crosses it. The engine boots at
	// the settings default of 100, so a throttle here is this save landing.
	status, body := mutate(t, http.MethodPatch, base+"/api/v1/admin/settings/rate",
		cookie, csrf, map[string]any{"per_sec": 1, "burst": 3})
	if status != http.StatusOK {
		t.Fatalf("saving answered %d: %v", status, body)
	}

	var throttled int
	for i := 0; i < 25; i++ {
		if hostRequest(t, base, "") == http.StatusTooManyRequests {
			throttled++
		}
	}
	if throttled == 0 {
		t.Error("nothing was throttled after saving a burst of 3, so the save did not reach the running limiter")
	}
}

// A section this build does not have is refused rather than stored.
//
// Storing it would leave a document holding a section nothing reads, and the
// client would report a change that never happens.
func TestAnUnknownSectionIsRefused(t *testing.T) {
	base, cookie, csrf, _, _ := adminEngine(t)

	for _, section := range []string{"nosuch", "", "rate/../db", "RATE"} {
		status, _ := mutate(t, http.MethodPatch, base+"/api/v1/admin/settings/"+section,
			cookie, csrf, map[string]any{"per_sec": 30})
		if status == http.StatusOK {
			t.Errorf("the section %q was accepted", section)
		}
	}
}

// A blocking finding refuses the save, and stores nothing.
//
// The lockout probe is the one that matters here: a host list that excluded
// the administrator would take effect before any correction could be sent,
// and the correction is what would then be rejected.
func TestALockoutIsRefusedAndStoresNothing(t *testing.T) {
	base, cookie, csrf, _, _ := adminEngine(t)

	status, body := mutate(t, http.MethodPatch, base+"/api/v1/admin/settings/network",
		cookie, csrf, map[string]any{"app_hosts": []any{"somewhere.else"}})
	if status == http.StatusOK {
		t.Fatalf("a host list excluding the caller was accepted: %v", body)
	}
	if boolField(body, "stored") {
		t.Error("a refused save reports itself stored")
	}
	if boolField(body, "applied") {
		t.Error("a refused save reports itself applied")
	}

	// Nothing was written, so the administrator can still reach the server
	// and correct it.
	code, raw := withCookie(t, http.MethodGet, base+"/api/v1/admin/settings", cookie)
	if code != http.StatusOK {
		t.Fatalf("the administrator can no longer read the settings: %d", code)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if network, present := doc["network"].(map[string]any); present {
		if hosts, named := network["app_hosts"]; named {
			t.Errorf("the refused host list was stored anyway: %v", hosts)
		}
	}
}

// An ordinary account cannot read or write the settings.
func TestTheSettingsNeedAnAdministrator(t *testing.T) {
	base, _, _, plainCookie, plainCSRF := adminEngine(t)

	read, _ := withCookie(t, http.MethodGet, base+"/api/v1/admin/settings", plainCookie)
	if read != http.StatusForbidden {
		t.Errorf("an ordinary account read the settings: %d", read)
	}

	write, _ := mutate(t, http.MethodPatch, base+"/api/v1/admin/settings/rate",
		plainCookie, plainCSRF, map[string]any{"per_sec": 1})
	if write != http.StatusForbidden {
		t.Errorf("an ordinary account wrote the settings: %d", write)
	}
}

// The findings list is present whether or not anything was found.
//
// A client that had to test the field before iterating it is a client that
// will forget once.
func TestTheOutcomeAlwaysCarriesFindings(t *testing.T) {
	base, cookie, csrf, _, _ := adminEngine(t)

	_, body := mutate(t, http.MethodPatch, base+"/api/v1/admin/settings/rate",
		cookie, csrf, map[string]any{"per_sec": 30, "burst": 90})

	raw, present := body["findings"]
	if !present {
		t.Fatalf("no findings field: %v", body)
	}
	if raw == nil {
		t.Error("the findings are null rather than an empty list")
	}
}
