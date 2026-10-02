//go:build linux

package e2e_test

import (
	"bytes"
	"encoding/json"
	"flag"
	"net/http"
	"os"
	"testing"
)

var updateOpenAPI = flag.Bool("update-openapi", false, "rewrite the checked-in OpenAPI document from the server") //nolint:gochecknoglobals // a test flag must exist before flag.Parse runs.

// openAPIGolden is the document the frontend generates its client types from.
const openAPIGolden = "../../../frontend/src/api/generated/openapi.json"

// The checked-in specification is the one the server publishes, so a field the
// server stops sending reaches the client's types, and from there its type check.
func TestTheCheckedInOpenAPIMatchesTheServer(t *testing.T) {
	t.Parallel()
	base, adminCookie, _, _, _ := adminEngine(t)

	status, body := withCookie(t, http.MethodGet, base+"/api/v1/admin/openapi", adminCookie)
	if status != http.StatusOK {
		t.Fatalf("the specification answered %d: %s", status, body)
	}
	var indented bytes.Buffer
	if err := json.Indent(&indented, body, "", "  "); err != nil {
		t.Fatalf("the specification does not parse: %v", err)
	}
	got := append(indented.Bytes(), '\n')

	if *updateOpenAPI {
		if err := os.WriteFile(openAPIGolden, got, 0o644); err != nil {
			t.Fatalf("writing %s: %v", openAPIGolden, err)
		}
		return
	}
	want, err := os.ReadFile(openAPIGolden)
	if err != nil {
		t.Fatalf("reading %s: %v", openAPIGolden, err)
	}
	if !bytes.Equal(want, got) {
		t.Fatalf("%s differs from what the server publishes. Rewrite it with "+
			"`go test ./test/e2e -run TestTheCheckedInOpenAPIMatchesTheServer -update-openapi`, "+
			"then run `pnpm gen:api` in frontend.", openAPIGolden)
	}
}
