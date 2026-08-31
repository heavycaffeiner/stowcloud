// Linux only, matching the package under test.
//go:build linux

package handler

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/service/settings/check"
)

func blocking() check.Finding {
	return check.Finding{Section: "network", Field: "app_hosts", ReasonKey: "host.overlap", Blocking: true}
}

func warning() check.Finding {
	return check.Finding{Section: "smb", ReasonKey: "smb.agent_unreachable"}
}

// A save that reached the database and did not take effect says both. An
// operator told only "saved" would believe the change is live and act on that.
func TestAFailedApplyIsStoredAndNotApplied(t *testing.T) {
	got := ApplyOutcomeOf(true, false, false, nil)

	if !got.Stored {
		t.Error("a stored change reported stored=false")
	}
	if got.Applied {
		t.Error("a failed apply reported applied=true")
	}

	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	if !strings.Contains(string(raw), `"stored":true`) || !strings.Contains(string(raw), `"applied":false`) {
		t.Errorf("the body does not carry both facts: %s", raw)
	}
}

// A blocking finding stores nothing and applies nothing, whatever the caller
// passed. A refused save reporting itself stored is the worst of the three.
func TestABlockingFindingStoresNothing(t *testing.T) {
	got := ApplyOutcomeOf(true, true, true, []check.Finding{warning(), blocking()})

	if got.Stored || got.Applied || got.RestartRequired {
		t.Errorf("a refused save reported %+v", got)
	}
	if len(got.Findings) != 2 {
		t.Fatalf("the refusal carried %d findings", len(got.Findings))
	}
	// The warning survives alongside the refusal: an observation worth
	// surfacing is not cancelled by an objection beside it.
	if got.Findings[0].Blocking || !got.Findings[1].Blocking {
		t.Errorf("the findings lost which one refused: %+v", got.Findings)
	}
}

// A warning saves. An observation worth surfacing is not automatically an
// objection.
func TestAWarningDoesNotRefuseTheSave(t *testing.T) {
	got := ApplyOutcomeOf(true, true, false, []check.Finding{warning()})

	if !got.Stored || !got.Applied {
		t.Errorf("a warning refused the save: %+v", got)
	}
	if len(got.Findings) != 1 || got.Findings[0].Blocking {
		t.Errorf("the warning is reported as %+v", got.Findings)
	}
	if Blocking([]check.Finding{warning()}) {
		t.Error("a warning was reported as blocking")
	}
}

// A restart-required change is not applied. Saying both would report a clean
// application of something that is not running.
func TestARestartRequiredChangeIsNotApplied(t *testing.T) {
	got := ApplyOutcomeOf(true, true, true, nil)

	if !got.Stored {
		t.Error("a restart-required change reported stored=false")
	}
	if got.Applied {
		t.Error("a restart-required change reported applied=true")
	}
	if !got.RestartRequired {
		t.Error("the restart requirement was lost")
	}
}

// Active work is reported so the operator decides, and only where a restart is
// actually required.
func TestActiveWorkIsReportedOnlyForARestart(t *testing.T) {
	restart := ApplyOutcomeOf(true, false, true, nil).WithActiveWork(3, 1)
	if restart.ActiveUploads == nil || *restart.ActiveUploads != 3 {
		t.Errorf("the upload count is %v", restart.ActiveUploads)
	}
	if restart.ActiveJobs == nil || *restart.ActiveJobs != 1 {
		t.Errorf("the job count is %v", restart.ActiveJobs)
	}

	// Zero active uploads is a real answer and is reported, not omitted: "no
	// uploads are running" is what makes a restart safe to press.
	quiet := ApplyOutcomeOf(true, false, true, nil).WithActiveWork(0, 0)
	raw, err := json.Marshal(quiet)
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	if !strings.Contains(string(raw), `"active_uploads":0`) {
		t.Errorf("a quiet server omitted its zero count: %s", raw)
	}

	// No restart, no counts.
	plain := ApplyOutcomeOf(true, true, false, nil).WithActiveWork(3, 1)
	if plain.ActiveUploads != nil || plain.ActiveJobs != nil {
		t.Errorf("a plain save carried active work: %+v", plain)
	}
}

// The finding carries a key and its arguments, not a rendered sentence: the
// client owns the wording and the language.
func TestAFindingCarriesItsKeyAndArguments(t *testing.T) {
	got := FindingsOf([]check.Finding{{
		Section:   "network",
		Field:     "app_hosts",
		ReasonKey: "host.overlap",
		Args:      []string{"host", "files.example.test", "role", "content"},
		Blocking:  true,
	}})

	if len(got) != 1 {
		t.Fatalf("the projection produced %d findings", len(got))
	}
	f := got[0]
	if f.Reason != "host.overlap" {
		t.Errorf("the reason key is %q", f.Reason)
	}
	if f.Args["host"] != "files.example.test" || f.Args["role"] != "content" {
		t.Errorf("the arguments are %v", f.Args)
	}
}

// An odd trailing argument is dropped rather than paired with an empty string,
// which would render as a substitution nobody wrote.
func TestAnOddArgumentIsDropped(t *testing.T) {
	got := FindingsOf([]check.Finding{{ReasonKey: "x", Args: []string{"host", "a", "orphan"}}})
	if len(got[0].Args) != 1 {
		t.Errorf("the arguments are %v", got[0].Args)
	}
	if _, ok := got[0].Args["orphan"]; ok {
		t.Error("the odd argument was paired with an empty value")
	}
}

// An empty findings list encodes as [] rather than null.
func TestAnEmptyFindingListEncodesAsAList(t *testing.T) {
	raw, err := json.Marshal(ApplyOutcomeOf(true, true, false, nil))
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	if !strings.Contains(string(raw), `"findings":[]`) {
		t.Errorf("an empty list encoded as %s", raw)
	}
}
