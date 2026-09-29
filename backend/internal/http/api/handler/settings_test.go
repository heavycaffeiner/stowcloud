// Linux only, matching the package under test.
//go:build linux

package handler

import (
	"encoding/json"
	"strings"
	"testing"
)

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
