//go:build linux

package lifecycle_test

import (
	"fmt"
	"net/http"
	"testing"
	"time"
)

// Turning the size guard on stops the databases accepting writes, and turning
// it off lets them accept again.
//
// The setting used to do nothing: the switch and its bounds were stored,
// validated and shown, and nothing anywhere constructed a guard. An operator
// who set a ceiling to protect a full volume got no protection, and no restart
// gave them any, because the sampler that would have applied it was never
// started.
//
// Probed by creating an account, which is certainly a row. A filesystem write
// is the wrong question: mkdir reaches the database only when it has to
// allocate a node id, so it passes under a tripped guard and would have
// reported this working while it did nothing.
func TestTheSizeGuardRefusesWritesPastItsCeiling(t *testing.T) {
	base, cookie, csrf, _, _ := adminEngine(t)

	n := 0
	newAccount := func() bool {
		n++
		status, _ := mutate(t, http.MethodPost, base+"/api/v1/admin/users",
			cookie, csrf, map[string]any{
				"login":    fmt.Sprintf("probe%d", n),
				"display":  "Probe",
				"password": "a-long-enough-password",
			})
		return status == http.StatusCreated || status == http.StatusOK
	}

	// A row lands before the guard exists, so the refusal below is the guard
	// and not a route that never worked.
	if !newAccount() {
		t.Fatal("a write before the guard was refused")
	}

	// A ceiling of one byte, which the databases are already past.
	if status, body := mutate(t, http.MethodPatch, base+"/api/v1/admin/settings/db",
		cookie, csrf, map[string]any{
			"size_guard": true, "max_bytes": 1, "min_free_bytes": 0,
		}); status != http.StatusOK {
		t.Fatalf("setting a ceiling answered %d: %v", status, body)
	}

	// The first sample runs before the first tick, so this waits on the
	// goroutine starting rather than on an interval elapsing.
	if !eventually(t, 5*time.Second, func() bool { return !newAccount() }) {
		t.Fatal("the databases kept accepting writes past the ceiling")
	}

	// Turning the guard off has to release them. A block that outlived its
	// setting would leave an operator who raised the ceiling with no way back,
	// since raising it is itself a write.
	if status, body := mutate(t, http.MethodPatch, base+"/api/v1/admin/settings/db",
		cookie, csrf, map[string]any{
			"size_guard": false, "max_bytes": 0, "min_free_bytes": 0,
		}); status != http.StatusOK {
		t.Fatalf("turning the guard off answered %d: %v", status, body)
	}
	if !eventually(t, 5*time.Second, newAccount) {
		t.Error("the databases stayed blocked after the guard was turned off")
	}
}

// eventually polls until the condition holds or the budget runs out.
//
// A ticker rather than a deadline, because reading the wall clock is reserved
// to the clock packages and a test has no more claim on it than anything else.
func eventually(t *testing.T, budget time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.After(budget)
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		if cond() {
			return true
		}
		select {
		case <-deadline:
			return false
		case <-tick.C:
		}
	}
}
