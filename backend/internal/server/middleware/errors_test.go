// Linux only, matching the package under test.
//go:build linux

package middleware

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/heavycaffeiner/stowcloud/backend/internal/server/apierr"
)

type waitError struct{ seconds int }

func (e *waitError) Error() string   { return "wait" }
func (e *waitError) RetryAfter() int { return e.seconds }

func errorApp(t *testing.T, handler gin.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	app := gin.New()
	app.Use(stepHandler(StepErrorMapper, Deps{Errors: apierr.NewClassifier(nil)}))
	app.GET("/x", handler)
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://localhost/x", nil))
	return rec
}

// A recorded error is rendered once the handler returns, through the classifier.
func TestAFailedHandlerIsAnsweredWithItsClass(t *testing.T) {
	t.Parallel()
	rec := errorApp(t, func(c *gin.Context) {
		Fail(c, fmt.Errorf("wrapped: %w", apierr.AsClassified(apierr.Conflict, "fs.conflict")))
	})
	if rec.Code != http.StatusConflict {
		t.Errorf("answered %d, want 409", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"code":"fs.conflict"`) {
		t.Errorf("the body is %s", rec.Body.String())
	}
}

// A refusal that knows how long to wait says so on the wire. Without the
// header a batch of clients guesses the same short interval and is refused
// together again.
func TestARefusalThatKnowsItsDelayCarriesIt(t *testing.T) {
	t.Parallel()
	waiting := errorApp(t, func(c *gin.Context) { Fail(c, fmt.Errorf("spool: %w", &waitError{seconds: 7})) })
	if got := waiting.Header().Get("Retry-After"); got != "7" {
		t.Errorf("Retry-After is %q, want the delay the error carried", got)
	}
	other := errorApp(t, func(c *gin.Context) { Fail(c, errors.New("something else")) })
	if got := other.Header().Get("Retry-After"); got != "" {
		t.Errorf("an unrelated refusal carried Retry-After %q", got)
	}
}

// A handler that already answered keeps its answer: an error recorded after
// the body started is for the log, not a second response.
func TestAnAnsweredRequestIsNotRenderedAgain(t *testing.T) {
	t.Parallel()
	rec := errorApp(t, func(c *gin.Context) {
		c.String(http.StatusOK, "partial")
		Fail(c, errors.New("late"))
	})
	if rec.Code != http.StatusOK || rec.Body.String() != "partial" {
		t.Errorf("answered %d %q", rec.Code, rec.Body.String())
	}
}
