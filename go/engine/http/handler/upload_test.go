// Linux only, matching the package under test.
//go:build linux

package handler

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/service/upload"
)

// Offset and received are different numbers and both travel. A random-access
// client that wrote past a hole has more received than its offset; showing one
// where the other belongs either loses the progress bar or breaks the resume.
func TestOffsetAndReceivedAreBothReported(t *testing.T) {
	v := UploadSessionOf(upload.Session{
		Offset:   1000,
		Received: 5000,
		RunCount: 3,
	})

	if v.Offset != "1000" {
		t.Errorf("the offset is %q", v.Offset)
	}
	if v.Received != "5000" {
		t.Errorf("the received count is %q", v.Received)
	}
	// The gap count is what tells a client that resuming from the offset will
	// re-send bytes that already landed.
	if v.Gaps != 3 {
		t.Errorf("the gap count is %d", v.Gaps)
	}

	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	if !strings.Contains(string(raw), `"offset":"1000"`) ||
		!strings.Contains(string(raw), `"received":"5000"`) {
		t.Errorf("the body collapsed the two counts: %s", raw)
	}
}

// A deferred-length upload has no declared size, and zero would be a real one.
func TestADeferredLengthUploadDeclaresNoSize(t *testing.T) {
	deferred := UploadSessionOf(upload.Session{})
	if deferred.TotalLength != nil {
		t.Errorf("a deferred upload reports %v", deferred.TotalLength)
	}
	raw, err := json.Marshal(deferred)
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	if strings.Contains(string(raw), "total_length") {
		t.Errorf("a deferred upload encoded a length: %s", raw)
	}

	// A declared zero-length upload is a real thing and reports zero.
	var zero uint64
	empty := UploadSessionOf(upload.Session{TotalLen: &zero})
	if empty.TotalLength == nil || *empty.TotalLength != "0" {
		t.Errorf("a declared empty upload reports %v", empty.TotalLength)
	}
}

// Sizes cross as strings: an upload can exceed a JavaScript number, and an
// offset that comes back wrong resumes in the wrong place.
func TestUploadCountsCrossAsStrings(t *testing.T) {
	const big = uint64(1)<<53 + 1
	raw, err := json.Marshal(UploadSessionOf(upload.Session{Offset: big, Received: big}))
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	if !strings.Contains(string(raw), `"offset":"9007199254740993"`) {
		t.Errorf("the offset lost exactness: %s", raw)
	}
}

// Every state has a name, and the two live ones are not terminal.
func TestEveryUploadStateIsNamed(t *testing.T) {
	for _, c := range []struct {
		state    upload.SessionState
		name     string
		terminal bool
	}{
		{upload.StateReceiving, "receiving", false},
		{upload.StateFinalizing, "finalizing", false},
		{upload.StateDone, "done", true},
		{upload.StateAborted, "aborted", true},
		{upload.StateExpired, "expired", true},
	} {
		v := UploadSessionOf(upload.Session{State: c.state})
		if v.State != c.name {
			t.Errorf("the state %d is named %q, want %q", c.state, v.State, c.name)
		}
		if v.Terminal != c.terminal {
			t.Errorf("the state %q reports terminal=%v", c.name, v.Terminal)
		}
	}

	// Finalizing is not terminal. An assembly that takes minutes is not an
	// abandoned session, and a client that gave up on it would abandon a file
	// that is still being written.
	if UploadSessionOf(upload.Session{State: upload.StateFinalizing}).Terminal {
		t.Error("finalizing was reported as terminal")
	}
}

// The two terminal answers agree, name by name. The service reads the stored
// number and this tier reads the name, so two lists exist and this is what
// keeps them one.
func TestBothUploadTerminalChecksAgree(t *testing.T) {
	published := upload.StateNames()
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

// Both spool modes are named, so a client knows which addressing to use.
func TestBothSpoolModesAreNamed(t *testing.T) {
	if got := UploadSessionOf(upload.Session{Mode: upload.SpoolOffsetAddressed}).Mode; got != "offset" {
		t.Errorf("the offset mode is named %q", got)
	}
	if got := UploadSessionOf(upload.Session{Mode: upload.SpoolNameOrdered}).Mode; got != "named" {
		t.Errorf("the named mode is named %q", got)
	}
	if got := upload.SpoolMode(99).ModeName(); got != "unknown" {
		t.Errorf("an unnamed mode is called %q", got)
	}
}

// An empty listing encodes as a list.
func TestAnEmptyUploadListingEncodesAsAList(t *testing.T) {
	raw, err := json.Marshal(UploadSessionsOf(nil))
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	if string(raw) != "[]" {
		t.Errorf("an empty listing encoded as %s", raw)
	}
}
