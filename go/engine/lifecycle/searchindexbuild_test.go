//go:build linux

package lifecycle_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/clock"
	"github.com/heavycaffeiner/stowcloud/go/engine/lifecycle"
)

// indexEngine serves an engine whose name index is switched on, with an
// administrator signed in and one share holding a file.
//
// The setting is saved before the engine that reads it opens, which is the
// order an operator configures in: the index directory is opened once at
// construction, so a value written afterwards would not reach it.
func indexEngine(t *testing.T) (base, dataDir string, cookie *http.Cookie, csrf string, e *lifecycle.Engine) {
	t.Helper()
	ctx := context.Background()
	dataDir = t.TempDir()

	first, err := lifecycle.Open(ctx, lifecycle.Options{DataDir: dataDir})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	if serr := first.State.SetIndexNameEnabled(ctx, true); serr != nil {
		t.Fatalf("enabling the index: %v", serr)
	}
	if _, cerr := first.Auth.CreateAdmin(ctx, "root", "Root", pwOf(loginPassword)); cerr != nil {
		t.Fatalf("creating the administrator: %v", cerr)
	}
	if cerr := first.Close(); cerr != nil {
		t.Fatalf("closing: %v", cerr)
	}

	opened, oerr := lifecycle.Open(ctx, lifecycle.Options{DataDir: dataDir})
	if oerr != nil {
		t.Fatalf("reopening: %v", oerr)
	}
	e = opened
	t.Cleanup(func() {
		if cerr := e.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	})
	base = serve(t, e)

	signIn := postJSON(t, base+"/api/v1/auth/login",
		map[string]string{"login": "root", "password": loginPassword})
	if signIn.sessionCookie() == nil {
		t.Fatalf("the administrator did not sign in: %d %v", signIn.status, signIn.body)
	}
	return base, dataDir, signIn.sessionCookie(), signIn.field("csrf"), e
}

// awaitJob polls one job until it leaves the running state.
func awaitJob(t *testing.T, base string, cookie *http.Cookie, id string) map[string]any {
	t.Helper()

	clk := clock.System()
	deadline := clk.Now().Add(20 * time.Second)
	for clk.Now().Before(deadline) {
		status, raw := withCookie(t, http.MethodGet, base+"/api/v1/jobs/"+id, cookie)
		if status != http.StatusOK {
			t.Fatalf("reading the job answered %d: %s", status, raw)
		}
		var job map[string]any
		if err := json.Unmarshal(raw, &job); err != nil {
			t.Fatalf("decoding the job: %v", err)
		}
		if state, isString := job["state"].(string); !isString || state != "running" {
			return job
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("the job never left the running state")
	return nil
}

// Building the index runs as a job rather than holding the request open.
//
// A build traverses every share, which is minutes on a real corpus. The client
// is handed a job id and polls it, which is what makes a long build observable
// rather than a request that has yet to answer.
func TestBuildingTheIndexRunsAsAJob(t *testing.T) {
	base, _, cookie, csrf, _ := indexEngine(t)
	share := makeShare(t, base, cookie, csrf, "docs")
	_ = share

	status, body := mutate(t, http.MethodPost, base+"/api/v1/admin/index/build", cookie, csrf, nil)
	if status != http.StatusAccepted {
		t.Fatalf("starting a build answered %d: %v", status, body)
	}

	id := stringField(body, "id")
	if id == "" {
		t.Fatalf("the build names no job: %v", body)
	}
	if kind := stringField(body, "kind"); kind == "" {
		t.Errorf("the job names no kind: %v", body)
	}

	job := awaitJob(t, base, cookie, id)
	if state := stringField(job, "state"); state != "done" {
		t.Errorf("the build finished as %q: %v", state, job)
	}
}

// A deployment with the index switched off refuses rather than reporting a
// build that wrote nothing.
func TestBuildingWithTheIndexOffIsRefused(t *testing.T) {
	base, adminCookie, adminCSRF, _, _ := adminEngine(t)

	status, body := mutate(t, http.MethodPost,
		base+"/api/v1/admin/index/build", adminCookie, adminCSRF, nil)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("building with no index answered %d: %v", status, body)
	}
	if key := reasonKey(body); key != "search.index_disabled" {
		t.Errorf("the refusal carries reason key %q", key)
	}
}

// Building is an administrator's: it walks every share on the deployment.
func TestBuildingTheIndexNeedsAnAdministrator(t *testing.T) {
	base, _, _, plainCookie, plainCSRF := adminEngine(t)

	status, _ := mutate(t, http.MethodPost,
		base+"/api/v1/admin/index/build", plainCookie, plainCSRF, nil)
	if status != http.StatusForbidden {
		t.Errorf("an ordinary account started an index build: %d", status)
	}
}

// The built index survives a restart.
//
// It is written to a directory under the data directory and found again at
// boot, which is what makes a build worth spending: an index that had to be
// rebuilt on every start would cost more than the walk it replaces.
func TestTheBuiltIndexSurvivesARestart(t *testing.T) {
	base, dataDir, cookie, csrf, first := indexEngine(t)

	// A share with a file in it. An empty corpus indexes nothing, so the
	// build would write no segment and this would be measuring an empty
	// directory rather than a surviving index.
	hostDir := t.TempDir()
	if werr := os.WriteFile(filepath.Join(hostDir, "report.txt"), []byte("x"), 0o600); werr != nil {
		t.Fatalf("writing into the share: %v", werr)
	}
	if status, created := mutate(t, http.MethodPost, base+"/api/v1/admin/shares", cookie, csrf,
		map[string]string{"name": "docs", "host": hostDir}); status != http.StatusCreated {
		t.Fatalf("creating the share answered %d: %v", status, created)
	}

	status, body := mutate(t, http.MethodPost, base+"/api/v1/admin/index/build", cookie, csrf, nil)
	if status != http.StatusAccepted {
		t.Fatalf("starting a build answered %d: %v", status, body)
	}
	awaitJob(t, base, cookie, stringField(body, "id"))

	// Something was written where a restart will look for it.
	entries, err := os.ReadDir(filepath.Join(dataDir, "index"))
	if err != nil {
		t.Fatalf("the index directory was not written: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("the index directory is empty after a build")
	}

	if cerr := first.Close(); cerr != nil {
		t.Fatalf("closing: %v", cerr)
	}

	// A second engine over the same directory finds it rather than starting
	// from nothing.
	second, oerr := lifecycle.Open(context.Background(), lifecycle.Options{DataDir: dataDir})
	if oerr != nil {
		t.Fatalf("reopening: %v", oerr)
	}
	t.Cleanup(func() {
		if cerr := second.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	})
	if !second.Search.HasIndex() {
		t.Error("the index was not reopened after a restart")
	}
}

// A deployment that never enabled the index opens none, so an index directory
// left behind does not come back on its own.
func TestTheIndexIsNotOpenedWhenTheSettingIsOff(t *testing.T) {
	e, err := lifecycle.Open(context.Background(), lifecycle.Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	t.Cleanup(func() {
		if cerr := e.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	})
	if e.Search.HasIndex() {
		t.Error("an index was opened on a deployment that did not ask for one")
	}
}

// Turning the setting off through a live save detaches the index rather than
// leaving it running under a setting that now says off.
func TestASaveDetachesTheIndexWhenTheSettingGoesOff(t *testing.T) {
	base, _, cookie, csrf, e := indexEngine(t)

	if !e.Search.HasIndex() {
		t.Fatal("the index was not attached by indexEngine's setup")
	}

	status, body := mutate(t, http.MethodPatch, base+"/api/v1/admin/settings/search",
		cookie, csrf, map[string]any{"name_index_enabled": false})
	if status != http.StatusOK {
		t.Fatalf("turning the index off answered %d: %v", status, body)
	}
	if restart, ok := body["restart_required"].(bool); ok && restart {
		t.Error("the save asked for a restart, which this setting no longer needs")
	}

	if e.Search.HasIndex() {
		t.Error("the index is still attached after a save turned it off")
	}

	// Turning it back on reattaches, so the toggle is not a one-way trip.
	status, body = mutate(t, http.MethodPatch, base+"/api/v1/admin/settings/search",
		cookie, csrf, map[string]any{"name_index_enabled": true})
	if status != http.StatusOK {
		t.Fatalf("turning the index back on answered %d: %v", status, body)
	}
	if !e.Search.HasIndex() {
		t.Error("the index did not reattach after a save turned it back on")
	}
}
