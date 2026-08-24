// Linux only, because what it tests is.
//go:build linux

package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// "Degraded" with no reason is a status an operator cannot act on, so the
// reasons are the whole value of the endpoint.

func readHealth(t *testing.T, state *HealthState) map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	Health(state)(rec, httptest.NewRequest(http.MethodGet, "/api/health", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("the body is not JSON: %v\n%s", err, rec.Body)
	}
	return body
}

func TestAnUndegradedServerReportsOk(t *testing.T) {
	body := readHealth(t, NewHealthState())
	if body["status"] != HealthOK {
		t.Fatalf("status = %v, want ok", body["status"])
	}
	// The list is present and empty rather than absent, so a client does not
	// have to handle two shapes for nothing being wrong.
	reasons, ok := body["reasons"].([]any)
	if !ok {
		t.Fatalf("reasons is %T, want a list", body["reasons"])
	}
	if len(reasons) != 0 {
		t.Fatalf("reasons = %v, want empty", reasons)
	}
}

// Every degradation this server can report is reachable and names itself.
func TestEveryDegradationIsReportable(t *testing.T) {
	kinds := []string{
		ReasonShareRejected,
		ReasonSMBBindFailed,
		ReasonDatabaseSizeGuard,
		ReasonPreviewPoolUnavailable,
		// New in this port. Without it a server running with a sandbox layer
		// missing looks exactly like one running with all of them.
		ReasonHardening,
		ReasonSearchIndexDisabled,
	}

	state := NewHealthState()
	for i, kind := range kinds {
		state.Degrade(kind, "detail_"+string(rune('a'+i)))
	}

	body := readHealth(t, state)
	if body["status"] != HealthDegraded {
		t.Fatalf("status = %v, want degraded", body["status"])
	}
	raw, err := json.Marshal(body["reasons"])
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}
	var reasons []HealthReason
	if uerr := json.Unmarshal(raw, &reasons); uerr != nil {
		t.Fatalf("the reasons do not decode: %v", uerr)
	}
	if len(reasons) != len(kinds) {
		t.Fatalf("got %d reasons, want %d", len(reasons), len(kinds))
	}

	seen := map[string]string{}
	for _, r := range reasons {
		seen[r.Kind] = r.Detail
	}
	for _, kind := range kinds {
		detail, ok := seen[kind]
		if !ok {
			t.Errorf("%s is not reported", kind)
			continue
		}
		// The kind alone does not tell an operator where to look.
		if detail == "" {
			t.Errorf("%s is reported with no detail", kind)
		}
	}
}

// A check that runs on a timer must not grow the list every time it runs.
func TestTheSameDegradationTwiceIsOneReason(t *testing.T) {
	state := NewHealthState()
	for range 5 {
		state.Degrade(ReasonPreviewPoolUnavailable, "no_worker_started")
	}
	if got := len(state.Reasons()); got != 1 {
		t.Fatalf("got %d reasons, want 1", got)
	}
	// A different detail under the same kind is a different problem.
	state.Degrade(ReasonPreviewPoolUnavailable, "every_worker_died")
	if got := len(state.Reasons()); got != 2 {
		t.Fatalf("got %d reasons, want 2", got)
	}
}

// A status that only ever gets worse stops being read.
func TestAFixedDegradationClears(t *testing.T) {
	state := NewHealthState()
	state.Degrade(ReasonPreviewPoolUnavailable, "no_worker_started")
	if state.Status() != HealthDegraded {
		t.Fatal("the state did not degrade")
	}
	state.Resolve(ReasonPreviewPoolUnavailable, "no_worker_started")
	if state.Status() != HealthOK {
		t.Fatalf("status = %s after the fix, want ok", state.Status())
	}
}

// Two reads of an unchanged state are the same answer, so a client polling it
// does not see the order shuffle.
func TestTheReasonsAreStablyOrdered(t *testing.T) {
	state := NewHealthState()
	state.Degrade(ReasonSMBBindFailed, "b")
	state.Degrade(ReasonShareRejected, "a")
	state.Degrade(ReasonShareRejected, "z")

	first := state.Reasons()
	for range 8 {
		got := state.Reasons()
		for i := range got {
			if got[i] != first[i] {
				t.Fatalf("the order changed between reads: %v then %v", first, got)
			}
		}
	}
	if first[0].Kind != ReasonShareRejected || first[0].Detail != "a" {
		t.Fatalf("the first reason is %+v", first[0])
	}
}

// The endpoint is reachable without a credential, because one that needs a
// credential is one the container runtime cannot use. It therefore says what is
// degraded and nothing else about the deployment.
func TestTheHealthBodyCarriesNothingButTheStatusAndReasons(t *testing.T) {
	state := NewHealthState()
	state.Degrade(ReasonShareRejected, "photos:overlayfs")

	body := readHealth(t, state)
	if len(body) != 2 {
		t.Fatalf("the body has %d fields, want the status and the reasons: %v", len(body), body)
	}
	for _, field := range []string{"status", "reasons"} {
		if _, ok := body[field]; !ok {
			t.Errorf("the body has no %s", field)
		}
	}
	// Nothing that would tell a stranger about the deployment.
	rendered := strings.ToLower(func() string {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshalling: %v", err)
		}
		return string(b)
	}())
	for _, leak := range []string{"version", "kernel", "hostname", "/srv", "listen"} {
		if strings.Contains(rendered, leak) {
			t.Errorf("the body carries %q: %s", leak, rendered)
		}
	}
}

// The reasons carry kinds and tokens rather than sentences, for the same
// reason every other refusal in this tree does: a sentence is a thing to
// translate, and a thing whose wording a caller starts matching on.
func TestTheReasonsAreTokensRatherThanSentences(t *testing.T) {
	state := NewHealthState()
	state.Degrade(ReasonHardening, "landlock_unavailable")
	state.Degrade(ReasonShareRejected, "photos:overlayfs")

	for _, r := range state.Reasons() {
		for _, field := range []string{r.Kind, r.Detail} {
			if strings.Contains(field, " ") {
				t.Errorf("%q reads as a sentence rather than a token", field)
			}
			// A trailing stop is the other tell.
			if strings.HasSuffix(field, ".") {
				t.Errorf("%q ends in a full stop", field)
			}
		}
	}
}
