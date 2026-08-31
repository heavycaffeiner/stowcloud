//go:build linux

package agent

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The defect this file exists for.
//
// The promoted configuration is what the daemon parses at every start and every
// reload. A torn one does not degrade smbd, it stops it: the parse fails and no
// share is served at all.
//
// The property that separates a durable replace from a plain write is what a
// reader already holding the file sees. An atomic rename leaves the previous
// inode intact, so a reader that opened before the promotion still reads the
// whole previous configuration. A write that truncates in place pulls the
// content out from under that same reader, which is exactly the window where
// the daemon reads a fragment.
func TestThePromotionIsNeverTorn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "smb.conf")

	const first = "[global]\n  workgroup = OFFICE\n"
	if err := Promote(path, first); err != nil {
		t.Fatal(err)
	}

	// A reader that opened the configuration before the promotion, which is the
	// daemon parsing it while a pass runs.
	reader, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer closeQuietly(reader)

	// A body far larger than one page, so an in-place write cannot land whole
	// by accident.
	second := "[global]\n  workgroup = LATER\n" + strings.Repeat("  # padding\n", 20000)
	if perr := Promote(path, second); perr != nil {
		t.Fatal(perr)
	}

	// The reader still sees the whole previous configuration. An in-place write
	// would have truncated this descriptor's file underneath it.
	during, rerr := io.ReadAll(reader)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if string(during) != first {
		t.Errorf("a reader open across the promotion saw %d bytes, want the previous configuration whole (%d bytes)",
			len(during), len(first))
	}

	// And a reader opening afterwards sees the new one, whole.
	after, aerr := os.ReadFile(path)
	if aerr != nil {
		t.Fatal(aerr)
	}
	if string(after) != second {
		t.Errorf("the promotion did not replace the configuration: %d bytes", len(after))
	}
}

// The file the daemon reads has to be one it can read. Every name lookup and
// every share depends on it, so a mode the daemon's own user cannot open breaks
// the whole service.
func TestThePromotedFileIsReadableByTheDaemon(t *testing.T) {
	path := filepath.Join(t.TempDir(), "smb.conf")
	if err := Promote(path, "[global]\n"); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != promotedMode {
		t.Errorf("the promoted file is mode %v, want %v", st.Mode().Perm(), os.FileMode(promotedMode))
	}
}

// The candidate is deliberately not durable, and the assertion is only that a
// pass regenerates it. Asserting durability here would erase the distinction
// this package is built around.
func TestTheCandidateIsRegeneratedRatherThanRepaired(t *testing.T) {
	path := filepath.Join(t.TempDir(), "smb.conf.candidate")

	// A torn candidate, as a crash mid-write would leave.
	if err := os.WriteFile(path, []byte("[glo"), 0o600); err != nil {
		t.Fatal(err)
	}

	const whole = "[global]\n  workgroup = OFFICE\n"
	if err := WriteCandidate(path, whole); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != whole {
		t.Errorf("the pass did not regenerate the candidate: %q", got)
	}
}

// The candidate is this agent's scratch and holds nothing anyone else needs.
func TestTheCandidateIsPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "smb.conf.candidate")
	if err := WriteCandidate(path, "[global]\n"); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != candidateMode {
		t.Errorf("the candidate is mode %v, want %v", st.Mode().Perm(), os.FileMode(candidateMode))
	}
}

// A promotion into a directory that does not exist reports rather than leaving
// the caller believing the daemon has a new configuration.
func TestAPromotionThatCannotLandReports(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent", "smb.conf")
	if err := Promote(path, "[global]\n"); err == nil {
		t.Error("promoting into a missing directory reported success")
	}
}
