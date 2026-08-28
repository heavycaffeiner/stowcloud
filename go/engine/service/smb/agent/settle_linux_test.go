//go:build linux

package agent

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/clock"
)

// The four-row table, and the reason the order within it matters.
func TestSettleTellsTheDaemonAsLittleAsWillDo(t *testing.T) {
	for _, c := range []struct {
		name string
		in   SettleInput
		want SmbdAction
	}{
		{
			name: "not running",
			in:   SettleInput{Running: false, Bound: "lo", Wanted: "lo", Promoted: "x", Candidate: "x"},
			want: ActionStarted,
		},
		{
			// A reload never revisits the sockets, so a moved bind line needs
			// the process replaced.
			name: "the bind line moved",
			in:   SettleInput{Running: true, Bound: "lo", Wanted: "lo eth0", Promoted: "x", Candidate: "y"},
			want: ActionRestarted,
		},
		{
			name: "nothing changed",
			in:   SettleInput{Running: true, Bound: "lo eth0", Wanted: "lo eth0", Promoted: "x", Candidate: "x"},
			want: ActionUnchanged,
		},
		{
			// Shares, users and permissions are all a reload can carry, and all
			// it needs to.
			name: "the configuration changed",
			in:   SettleInput{Running: true, Bound: "lo eth0", Wanted: "lo eth0", Promoted: "x", Candidate: "y"},
			want: ActionReloaded,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := Settle(c.in); got != c.want {
				t.Errorf("Settle = %q, want %q", got, c.want)
			}
		})
	}
}

// A moved bind line outranks an identical configuration. The file can be byte
// identical while the detected scope moved underneath it, which is what happens
// when a tunnel comes up.
func TestAMovedBindLineOutranksAnUnchangedConfiguration(t *testing.T) {
	got := Settle(SettleInput{
		Running:   true,
		Bound:     "lo",
		Wanted:    "lo eth0",
		Promoted:  "identical",
		Candidate: "identical",
	})
	if got != ActionRestarted {
		t.Errorf("Settle = %q, want a restart: the bind line moved under an unchanged file", got)
	}
}

// A daemon that is not running cannot be reloaded, whatever else changed.
func TestAStoppedDaemonIsStartedRatherThanReloaded(t *testing.T) {
	got := Settle(SettleInput{
		Running: false, Bound: "lo", Wanted: "lo eth0", Promoted: "x", Candidate: "y",
	})
	if got != ActionStarted {
		t.Errorf("Settle = %q, want a start", got)
	}
}

// fakeDaemon records what it was told.
type fakeDaemon struct {
	running bool
	calls   []string
	fail    error
}

func (d *fakeDaemon) Running() bool  { return d.running }
func (d *fakeDaemon) Start() error   { d.calls = append(d.calls, "start"); return d.fail }
func (d *fakeDaemon) Stop() error    { d.calls = append(d.calls, "stop"); return d.fail }
func (d *fakeDaemon) Restart() error { d.calls = append(d.calls, "restart"); return d.fail }
func (d *fakeDaemon) Reload() error  { d.calls = append(d.calls, "reload"); return d.fail }

func TestTellCarriesOutTheDecision(t *testing.T) {
	for _, c := range []struct {
		action SmbdAction
		want   []string
	}{
		{ActionStarted, []string{"start"}},
		{ActionRestarted, []string{"restart"}},
		{ActionReloaded, []string{"reload"}},
		{ActionUnchanged, nil},
	} {
		t.Run(string(c.action), func(t *testing.T) {
			d := &fakeDaemon{}
			got, err := Tell(d, c.action)
			if err != nil {
				t.Fatal(err)
			}
			if got != c.action {
				t.Errorf("Tell reported %q, want %q", got, c.action)
			}
			if len(d.calls) != len(c.want) {
				t.Fatalf("the daemon was told %v, want %v", d.calls, c.want)
			}
			for i := range c.want {
				if d.calls[i] != c.want[i] {
					t.Errorf("the daemon was told %v, want %v", d.calls, c.want)
				}
			}
		})
	}
}

// A daemon that would not do what it was told is a failure, not the action that
// was intended.
func TestTellReportsAFailureRatherThanTheIntendedAction(t *testing.T) {
	d := &fakeDaemon{fail: errors.New("the unit would not start")}
	got, err := Tell(d, ActionStarted)
	if err == nil {
		t.Fatal("a failed start was reported as a success")
	}
	if got != ActionFailed {
		t.Errorf("Tell reported %q, want a failure", got)
	}
}

// The fingerprint is the poll loop's answer to whether anything changed, and it
// has to notice a file that arrived, changed or went away.
func TestTheFingerprintFollowsTheRenderedFiles(t *testing.T) {
	dir := t.TempDir()

	empty := Fingerprint(dir)
	if empty == "" {
		t.Fatal("an empty directory produced no fingerprint at all")
	}

	conf := filepath.Join(dir, "smb.conf")
	if err := os.WriteFile(conf, []byte("[global]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	arrived := Fingerprint(dir)
	if arrived == empty {
		t.Error("a rendered file appearing did not change the fingerprint")
	}

	// A different size is a different fingerprint.
	if err := os.WriteFile(conf, []byte("[global]\n  workgroup = OFFICE\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	grown := Fingerprint(dir)
	if grown == arrived {
		t.Error("a changed file did not change the fingerprint")
	}

	// The same content at a later time is still a change, because a republish
	// that rewrote the same bytes still needs applying.
	later := clock.System().Now().Add(2 * time.Second)
	if err := os.Chtimes(conf, later, later); err != nil {
		t.Fatal(err)
	}
	if touched := Fingerprint(dir); touched == grown {
		t.Error("a rewritten file did not change the fingerprint")
	}

	if err := os.Remove(conf); err != nil {
		t.Fatal(err)
	}
	if gone := Fingerprint(dir); gone != empty {
		t.Error("a removed file did not return the fingerprint to its absent state")
	}
}

// The scope belongs in the fingerprint because it moves with nothing on disk
// changing. This asserts the scope is actually read, by proving a pinned policy
// leaves it out and an unpinned one puts it in.
func TestTheFingerprintIncludesTheDetectedScope(t *testing.T) {
	dir := t.TempDir()
	policy := filepath.Join(dir, "network.policy")

	if err := os.WriteFile(policy, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	unpinned := Fingerprint(dir)

	if err := os.WriteFile(policy, []byte("pinned_interfaces=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pinned := Fingerprint(dir)

	// The two policy files differ in size, so the file half differs too. What
	// this checks is that the unpinned fingerprint carries a bind line and the
	// pinned one does not, which is the scope half being read.
	if !hasScopeSuffix(unpinned) {
		t.Errorf("an unpinned fingerprint carries no detected scope: %q", unpinned)
	}
	if hasScopeSuffix(pinned) {
		t.Errorf("a pinned fingerprint carries a detected scope it must not widen: %q", pinned)
	}
}

// hasScopeSuffix reports whether a fingerprint ends in a bind line rather than
// in the file half's terminator.
func hasScopeSuffix(fp string) bool {
	return len(fp) > 0 && fp[len(fp)-1] != ';'
}
