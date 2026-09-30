//go:build linux

package app

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/heavycaffeiner/stowcloud/backend/internal/files"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/server"
	"github.com/heavycaffeiner/stowcloud/backend/internal/server/apierr"
	"github.com/heavycaffeiner/stowcloud/backend/internal/uploads"
)

// The other half: where the caller already learned the resource exists, a
// denial is reported as one. Without this the fold would make every denial
// unreportable, including on surfaces where saying so is correct.
func TestAKnownDenialIsReportedAsDenied(t *testing.T) {
	status, body := apierr.REST(ErrorClassifier().Classify(files.ErrDenied, apierr.VisibilityKnown))
	if status != http.StatusForbidden {
		t.Errorf("a known denial answered %d, want 403", status)
	}
	if body.Code != "fs.denied" {
		t.Errorf("the code is %q", body.Code)
	}
}

// A missing resource stays 404 under either visibility: the fold is about
// hiding a denial, not about changing what absence means.
func TestAbsenceIsNotFoundUnderEitherVisibility(t *testing.T) {
	for _, v := range []apierr.Visibility{apierr.VisibilityHidden, apierr.VisibilityKnown} {
		status, body := apierr.REST(ErrorClassifier().Classify(files.ErrNotFound, v))
		if status != http.StatusNotFound {
			t.Errorf("absence under %v answered %d", v, status)
		}
		if body.Code != "fs.not_found" {
			t.Errorf("absence under %v answered the code %q", v, body.Code)
		}
	}
}

// A bound that clears on its own and one that does not answer differently.
//
// Both used to be 422, which tells a client the request is wrong and there is
// nothing to wait for. The account's own uploads finishing is exactly the wait
// that clears an exhaustion, so a batch of files that briefly crossed the
// session bound lost its remaining members rather than pausing for them.
func TestATemporaryBoundAsksTheCallerToWaitAndAPermanentOneDoesNot(t *testing.T) {
	for _, c := range []struct {
		name string
		err  error
		want int
	}{
		{"sessions in flight", &uploads.ExhaustedError{Limit: "sessions"}, http.StatusTooManyRequests},
		{"the spool at its budget", &uploads.CacheFullError{RetryAfterSeconds: 3}, http.StatusTooManyRequests},
		{"a session fragmented past its run cap", uploads.ErrFragmented, http.StatusUnprocessableEntity},
	} {
		status, body := apierr.REST(ErrorClassifier().Classify(c.err, apierr.VisibilityHidden))
		if status != c.want {
			t.Errorf("%s answered %d, want %d (%s)", c.name, status, c.want, body.Code)
		}
	}
}

// The setup gate's refusals keep their own classes, so the wizard can tell a
// closed gate from a wrong token.
func TestTheSetupGateRefusalsAreDistinct(t *testing.T) {
	errs := ErrorClassifier()
	for _, c := range []struct {
		err  error
		want apierr.Class
	}{
		{server.ErrSetupClosed, apierr.SetupComplete},
		{server.ErrSetupNotIssued, apierr.SetupExpired},
		{server.ErrSetupToken, apierr.SetupInvalidToken},
	} {
		if got := errs.Classify(c.err, apierr.VisibilityKnown).Class; got != c.want {
			t.Errorf("%v classified as %s, want %s", c.err, got, c.want)
		}
	}
}

// The spool's refusal advertises its delay through the method the chain's
// error step reads, so a client told 429 also learns how long to wait.
func TestTheSpoolRefusalAdvertisesItsDelay(t *testing.T) {
	var wait interface{ RetryAfter() int }
	err := fmt.Errorf("spool: %w", &uploads.CacheFullError{RetryAfterSeconds: 7})
	if !errors.As(err, &wait) || wait.RetryAfter() != 7 {
		t.Errorf("the spool refusal does not carry its delay")
	}
}
