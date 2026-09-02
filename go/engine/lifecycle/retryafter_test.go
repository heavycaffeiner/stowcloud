//go:build linux

package lifecycle

import (
	"errors"
	"net/http"
	"testing"

	"github.com/gofiber/fiber/v2"

	"github.com/heavycaffeiner/stowcloud/go/engine/service/upload"
)

// A refusal that knows how long to wait says so on the wire.
//
// The spool carries a delay because what the caller waits for is a disk write
// already under way. Without the header a client guesses its own interval, and
// a batch of uploads that all guess the same short one comes back together and
// is refused together.
func TestASpoolRefusalCarriesItsOwnDelay(t *testing.T) {
	app := fiber.New()
	app.Get("/full", func(c *fiber.Ctx) error {
		return fail(c, &upload.CacheFullError{RetryAfterSeconds: 7})
	})
	app.Get("/other", func(c *fiber.Ctx) error {
		return fail(c, errors.New("something else"))
	})

	full, err := app.Test(httpGet(t, "/full"))
	if err != nil {
		t.Fatalf("the spool request failed: %v", err)
	}
	defer func() {
		if cerr := full.Body.Close(); cerr != nil {
			t.Errorf("closing the body: %v", cerr)
		}
	}()
	if got := full.Header.Get(fiber.HeaderRetryAfter); got != "7" {
		t.Errorf("Retry-After is %q, want the delay the error carried", got)
	}
	if full.StatusCode != http.StatusTooManyRequests {
		t.Errorf("a momentary exhaustion answered %d, want 429: a client told 422 gives up on a bound that clears by itself",
			full.StatusCode)
	}

	// Only where a delay is known. A header on every refusal would tell a
	// client to retry things that will be refused identically forever.
	other, err := app.Test(httpGet(t, "/other"))
	if err != nil {
		t.Fatalf("the other request failed: %v", err)
	}
	defer func() {
		if cerr := other.Body.Close(); cerr != nil {
			t.Errorf("closing the body: %v", cerr)
		}
	}()
	if got := other.Header.Get(fiber.HeaderRetryAfter); got != "" {
		t.Errorf("an unrelated refusal carried Retry-After %q", got)
	}
}

func httpGet(t *testing.T, path string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, path, nil)
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	return req
}
