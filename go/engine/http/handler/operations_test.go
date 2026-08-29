// Linux only, matching the package under test.
//go:build linux

package handler

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
)

// Ids and counters cross as decimal strings. A JavaScript number loses
// exactness past 2^53, so an id round-tripped through a client would come back
// as a different id, pointing at somebody else's job or none at all.
func TestOperationIdsCrossAsStrings(t *testing.T) {
	// One past the exact range of a float64 integer.
	const big = int64(1)<<53 + 1

	v := OperationOf(core.Operation{ID: core.OperationID(big), Progress: big, Total: math.MaxInt64})

	if v.ID != strconv.FormatInt(big, 10) {
		t.Errorf("the id encoded as %q", v.ID)
	}

	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	// Quoted in the JSON, which is what keeps a decoder from making it a
	// number and losing the low bit.
	if !strings.Contains(string(raw), `"id":"`+strconv.FormatInt(big, 10)+`"`) {
		t.Errorf("the id is not a string in the body: %s", raw)
	}
	if !strings.Contains(string(raw), `"total":"9223372036854775807"`) {
		t.Errorf("the total lost exactness: %s", raw)
	}

	// The round trip a client actually performs.
	var back OperationView
	if derr := json.Unmarshal(raw, &back); derr != nil {
		t.Fatalf("decoding: %v", derr)
	}
	got, perr := strconv.ParseInt(back.ID, 10, 64)
	if perr != nil || got != big {
		t.Errorf("the id round-tripped to %d (%v)", got, perr)
	}
}

// A running job carries no results, and a finished one carries them. Nothing
// streams during a run, so a client polls and reads them when the state says
// to.
func TestARunningJobCarriesNoResults(t *testing.T) {
	// The zero OpState is running, which is what a freshly created job has.
	running := OperationOf(core.Operation{})
	if running.State != "running" {
		t.Fatalf("the state encoded as %q", running.State)
	}
	if len(running.Results) != 0 {
		t.Errorf("a running job carried %d results", len(running.Results))
	}
	if terminal, _ := TerminalStateName(running.State); terminal {
		t.Error("running was reported as terminal")
	}
}

// Every terminal state is terminal, and the running one is not. A list written
// twice is how one state is forgotten and a client polls a finished job
// forever.
func TestEveryTerminalStateIsTerminal(t *testing.T) {
	for _, name := range []string{"done", "failed", "cancelled", "interrupted"} {
		terminal, known := TerminalStateName(name)
		if !terminal || !known {
			t.Errorf("%q reported terminal=%v known=%v", name, terminal, known)
		}
	}
	if terminal, known := TerminalStateName("running"); terminal || !known {
		t.Errorf("running reported terminal=%v known=%v", terminal, known)
	}
	// An unknown state counts as finished, and says it is unknown: a client
	// polling forever on a state this build does not have is worse than one
	// that stops, and the second answer is what keeps the list above testable.
	if terminal, known := TerminalStateName("something_new"); !terminal || known {
		t.Errorf("an unknown state reported terminal=%v known=%v", terminal, known)
	}
}

// The attempting and pending lists stay apart. Whether an attempted item
// landed is genuinely unknown, and folding it in with the untouched ones would
// offer a client a re-run that could duplicate work.
func TestAttemptingAndPendingStayApart(t *testing.T) {
	v := OperationOf(core.Operation{
		Attempting: []string{"in/flight.txt"},
		Pending:    []string{"never/reached.txt", "also/not.txt"},
	})

	if len(v.Attempting) != 1 || v.Attempting[0] != "in/flight.txt" {
		t.Errorf("attempting carried %v", v.Attempting)
	}
	if len(v.Pending) != 2 {
		t.Errorf("pending carried %v", v.Pending)
	}
	for _, p := range v.Pending {
		if p == "in/flight.txt" {
			t.Error("the attempted item appears in the pending list")
		}
	}
}

// The projection copies the slices rather than aliasing the service's, so a
// caller that keeps a view cannot see a later mutation of the source.
func TestTheProjectionDoesNotAliasTheService(t *testing.T) {
	src := core.Operation{Pending: []string{"one.txt"}}
	v := OperationOf(src)
	src.Pending[0] = "mutated.txt"

	if v.Pending[0] != "one.txt" {
		t.Errorf("the view aliased the service's slice: %v", v.Pending)
	}
}

// An empty list encodes as [] rather than null, so a client iterating it does
// not have to test for the field first.
func TestAnEmptyOperationListEncodesAsAList(t *testing.T) {
	raw, err := json.Marshal(OperationsOf(nil))
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	if string(raw) != "[]" {
		t.Errorf("an empty list encoded as %s", raw)
	}
}

// The two terminal lists agree, name by name.
//
// core.Operation.Terminal reads the stored number and TerminalStateName reads
// the name, because this tier may not import the tier that owns the numbers.
// Two lists of the same thing is how one drifts, so this checks every name the
// service publishes against this tier's answer.
func TestBothTerminalChecksAgree(t *testing.T) {
	published := core.OperationStateNames()
	if len(published) < 5 {
		t.Fatalf("the service publishes only %d state names: %v", len(published), published)
	}
	for name, terminal := range published {
		got, known := TerminalStateName(name)
		if !known {
			t.Errorf("the state %q is published by the service and unknown to this tier", name)
			continue
		}
		if got != terminal {
			t.Errorf("the state %q: the service says terminal=%v and this tier says %v",
				name, terminal, got)
		}
	}
}
