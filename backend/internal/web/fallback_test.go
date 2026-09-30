// Linux only, matching the file under test.
//go:build linux

package web

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func shellApp(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	app := gin.New()
	app.GET("/api/v1/files/list", func(c *gin.Context) { c.String(200, "files.list") })
	if err := InstallFallback(app, func(c *gin.Context) {
		c.Header("Content-Type", "text/html")
		c.String(200, "<!doctype html><title>shell</title>")
	}); err != nil {
		t.Fatalf("InstallFallback: %v", err)
	}
	return app
}

func ask(t *testing.T, app *gin.Engine, path string) (int, string) {
	t.Helper()
	req := httptest.NewRequest("GET", "http://app.test"+path, nil)
	res := httptest.NewRecorder()
	app.ServeHTTP(res, req)
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return res.Code, string(body)
}

func TestTheShellAnswersAnUnmatchedPath(t *testing.T) {
	app := shellApp(t)
	for _, path := range []string{"/", "/files", "/settings/network", "/anything/at/all"} {
		status, body := ask(t, app, path)
		if status != 200 {
			t.Errorf("%s answered %d", path, status)
		}
		if !strings.Contains(body, "shell") {
			t.Errorf("%s answered %q", path, body)
		}
	}
}

func TestAMistypedReservedPathIsNotTheShell(t *testing.T) {
	app := shellApp(t)
	for _, path := range []string{"/api/v1/nosuchthing", "/api", "/dav/files/alice/gone.txt", "/remote.php/dav/files", "/ocs/v2.php/anything", "/s/sometoken", "/c/someclaim", "/emergency", "/emergency/settings"} {
		status, body := ask(t, app, path)
		if status == 200 {
			t.Errorf("%s answered 200 with %q", path, body)
		}
		if strings.Contains(body, "shell") {
			t.Errorf("%s answered with shell", path)
		}
	}
}

func TestReservationIsComponentWise(t *testing.T) {
	for _, c := range []struct {
		path     string
		reserved bool
	}{{"/api", true}, {"/api/", true}, {"/api/v1/files", true}, {"/apidocs", false}, {"/apiary/thing", false}, {"/dav", true}, {"/dav2", false}, {"/s", true}, {"/s/token", true}, {"/settings", false}, {"/c/claim", true}, {"/contacts", false}, {"/", false}, {"/files", false}} {
		if got := IsReserved(c.path); got != c.reserved {
			t.Errorf("IsReserved(%q) = %v, want %v", c.path, got, c.reserved)
		}
	}
}

func TestAFallbackNeedsAHandler(t *testing.T) {
	if err := InstallFallback(gin.New(), nil); err == nil {
		t.Error("fallback with no handler installed")
	}
}
