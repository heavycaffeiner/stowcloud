//go:build linux

package handler

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/heavycaffeiner/stowcloud/go/internal/feature/uploads"
)

// A refusal that knows how long to wait says so on the wire.
//
// The spool carries a delay because what the caller waits for is a disk write
// already under way. Without the header a client guesses its own interval, and
// a batch of uploads that all guess the same short one comes back together and
// is refused together.
func TestASpoolRefusalCarriesItsOwnDelay(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	app := gin.New()
	app.GET("/full", func(c *gin.Context) {
		Fail(c, &upload.CacheFullError{RetryAfterSeconds: 7})
	})
	app.GET("/other", func(c *gin.Context) {
		Fail(c, errors.New("something else"))
	})

	full := httptest.NewRecorder()
	app.ServeHTTP(full, httpGet(t, "/full"))
	if got := full.Header().Get("Retry-After"); got != "7" {
		t.Errorf("Retry-After is %q, want the delay the error carried", got)
	}
	if full.Code != http.StatusTooManyRequests {
		t.Errorf("a momentary exhaustion answered %d, want 429", full.Code)
	}

	other := httptest.NewRecorder()
	app.ServeHTTP(other, httpGet(t, "/other"))
	if got := other.Header().Get("Retry-After"); got != "" {
		t.Errorf("an unrelated refusal carried Retry-After %q", got)
	}
}

func httpGet(t *testing.T, path string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "http://localhost"+path, nil)
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	return req
}
