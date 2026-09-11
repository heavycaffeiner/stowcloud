//go:build linux

package lifecycle_test

import (
	"encoding/json"
	"net/http"
	"testing"
)

// sessionRootLabels reads the session's roots, in the order the wire carries
// them.
func sessionRootLabels(t *testing.T, base string, cookie *http.Cookie) []string {
	t.Helper()

	code, raw := withCookie(t, http.MethodGet, base+"/api/v1/auth/session", cookie)
	if code != http.StatusOK {
		t.Fatalf("the session answered %d: %s", code, raw)
	}
	var session struct {
		Roots []struct {
			Label string `json:"label"`
		} `json:"roots"`
	}
	if err := json.Unmarshal(raw, &session); err != nil {
		t.Fatal(err)
	}
	out := make([]string, len(session.Roots))
	for i, r := range session.Roots {
		out[i] = r.Label
	}
	return out
}

// A saved order changes the order the session lists roots in.
func TestASavedRootOrderChangesTheSessionsRootOrder(t *testing.T) {
	t.Parallel()
	base, cookie, csrf, plainCookie, plainCSRF := adminEngine(t)

	docs := makeShare(t, base, cookie, csrf, "docs")
	photos := makeShare(t, base, cookie, csrf, "photos")
	user := accountID(t, base, cookie, loginName)

	for _, g := range []struct {
		share, label string
	}{{docs, "Docs"}, {photos, "Photos"}} {
		if status, body := mutate(t, http.MethodPost, base+"/api/v1/admin/grants", cookie, csrf,
			map[string]any{
				"user": user, "share": g.share, "subpath": "",
				"allow": []string{"read", "download"}, "inherit": true, "label": g.label,
			}); status != http.StatusCreated {
			t.Fatalf("granting %s answered %d: %v", g.label, status, body)
		}
	}

	// The evaluator's own discovery order before any preference is saved.
	if got := sessionRootLabels(t, base, plainCookie); len(got) != 2 || got[0] != "Docs" || got[1] != "Photos" {
		t.Fatalf("the default order is %v, want [Docs Photos]", got)
	}

	if status, body := mutate(t, http.MethodPost, base+"/api/v1/account/roots/order", plainCookie, plainCSRF,
		map[string]any{"order": []string{"Photos", "Docs"}}); status != http.StatusNoContent {
		t.Fatalf("saving the order answered %d: %v", status, body)
	}

	if got := sessionRootLabels(t, base, plainCookie); len(got) != 2 || got[0] != "Photos" || got[1] != "Docs" {
		t.Fatalf("the saved order is %v, want [Photos Docs]", got)
	}
}

// A label named in the saved order that the account holds no root under is
// ignored rather than turning into an invented root.
func TestARootOrderNamingAnUnknownLabelInventsNothing(t *testing.T) {
	t.Parallel()
	base, cookie, csrf, plainCookie, plainCSRF := adminEngine(t)

	share := makeShare(t, base, cookie, csrf, "docs")
	user := accountID(t, base, cookie, loginName)

	if status, body := mutate(t, http.MethodPost, base+"/api/v1/admin/grants", cookie, csrf,
		map[string]any{
			"user": user, "share": share, "subpath": "",
			"allow": []string{"read", "download"}, "inherit": true, "label": "Docs",
		}); status != http.StatusCreated {
		t.Fatalf("granting answered %d: %v", status, body)
	}

	if status, body := mutate(t, http.MethodPost, base+"/api/v1/account/roots/order", plainCookie, plainCSRF,
		map[string]any{"order": []string{"Ghost", "Docs"}}); status != http.StatusNoContent {
		t.Fatalf("saving the order answered %d: %v", status, body)
	}

	got := sessionRootLabels(t, base, plainCookie)
	if len(got) != 1 || got[0] != "Docs" {
		t.Fatalf("the session lists %v, want exactly [Docs]", got)
	}
}

// A refused order (too long, an oversized label, or a duplicate) is refused
// entirely: nothing is stored and the account's roots stay in whatever order
// they already had.
func TestAnInvalidRootOrderIsRefusedAndStoresNothing(t *testing.T) {
	t.Parallel()
	base, cookie, csrf, plainCookie, plainCSRF := adminEngine(t)

	share := makeShare(t, base, cookie, csrf, "docs")
	user := accountID(t, base, cookie, loginName)

	if status, body := mutate(t, http.MethodPost, base+"/api/v1/admin/grants", cookie, csrf,
		map[string]any{
			"user": user, "share": share, "subpath": "",
			"allow": []string{"read", "download"}, "inherit": true, "label": "Docs",
		}); status != http.StatusCreated {
		t.Fatalf("granting answered %d: %v", status, body)
	}

	if status, body := mutate(t, http.MethodPost, base+"/api/v1/account/roots/order", plainCookie, plainCSRF,
		map[string]any{"order": []string{"Docs", "Docs"}}); status == http.StatusNoContent {
		t.Fatalf("a duplicate label was accepted: %v", body)
	}

	if got := sessionRootLabels(t, base, plainCookie); len(got) != 1 || got[0] != "Docs" {
		t.Fatalf("the refused order changed the listing: %v", got)
	}
}
