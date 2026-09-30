// Linux only, matching the package under test.
//go:build linux

package handler

import (
	"testing"

	"github.com/heavycaffeiner/stowcloud/backend/internal/uploads"
)

// The two terminal answers agree, name by name. The service reads the stored
// number and this tier reads the name, so two lists exist and this is what
// keeps them one.
func TestBothUploadTerminalChecksAgree(t *testing.T) {
	published := uploads.StateNames()
	if len(published) < 5 {
		t.Fatalf("the service publishes only %d names: %v", len(published), published)
	}
	for name, terminal := range published {
		got, known := TerminalUploadState(name)
		if !known {
			t.Errorf("the state %q is published and unknown to this tier", name)
			continue
		}
		if got != terminal {
			t.Errorf("the state %q: the service says terminal=%v and this tier says %v",
				name, terminal, got)
		}
	}

	// An unrecognised name is finished and says it is unknown, which is what
	// keeps the list above testable.
	if terminal, known := TerminalUploadState("something_new"); !terminal || known {
		t.Errorf("an unknown state reported terminal=%v known=%v", terminal, known)
	}
}
