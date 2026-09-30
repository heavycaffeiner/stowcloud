//go:build linux

package agent

import (
	"fmt"
	"os"
	"path/filepath"
)

// Telling the daemon as little as will do.
//
// Everything hangs on one distinction: the daemon binds its sockets once, at
// startup. A reload rereads shares and users in place and never revisits the
// sockets, so a changed bind line needs the process replaced rather than
// reloaded. The sidecar that preceded this agent reloaded either way and stayed
// bound to loopback for as long as it ran, which is the history this decision
// exists to prevent repeating.

// Daemon is the process this agent controls. An interface so the decision above
// can be tested without one.
type Daemon interface {
	Running() bool
	Start() error
	Stop() error
	Restart() error
	Reload() error
}

// SettleInput is what the decision reads.
type SettleInput struct {
	// Running reports whether the daemon is up.
	Running bool
	// Bound is the bind line the running process actually bound, which a reload
	// cannot change.
	Bound string
	// Wanted is the bind line the promoted configuration asks for.
	Wanted string
	// Promoted is the configuration as promoted last time, so an identical one
	// can be recognised.
	Promoted string
	// Candidate is what was just promoted.
	Candidate string
}

// Settle decides what the daemon has to be told.
//
// A pure function over the four inputs, so the table below is testable without
// a daemon to drive:
//
//	not running                  -> start
//	the bind line moved          -> restart
//	the configuration is the same -> nothing
//	anything else                -> reload
//
// The order matters. A daemon that is not running cannot be reloaded, and a
// moved bind line has to outrank an unchanged configuration, because the
// configuration can be byte identical while the detected scope moved underneath
// it: a tunnel coming up changes what should be bound without changing the file
// the daemon was started from.
func Settle(in SettleInput) SmbdAction {
	switch {
	case !in.Running:
		return ActionStarted
	case in.Bound != in.Wanted:
		return ActionRestarted
	case in.Promoted == in.Candidate:
		return ActionUnchanged
	default:
		return ActionReloaded
	}
}

// Tell carries out what Settle decided.
//
// Split from the decision so the table above stays a pure function, and so a
// failure to act is reported as a failure rather than as the action that was
// intended.
func Tell(d Daemon, action SmbdAction) (SmbdAction, error) {
	var err error
	switch action {
	case ActionStarted:
		err = d.Start()
	case ActionRestarted:
		err = d.Restart()
	case ActionReloaded:
		err = d.Reload()
	case ActionUnchanged, ActionStopped, ActionFailed:
		// Nothing to do. Unchanged is the common case, and the other two are
		// not this function's to bring about.
	}
	if err != nil {
		return ActionFailed, err
	}
	return action, nil
}

// renderedFiles names the set once, for the fingerprint that reads them and the
// teardown that watches for the first one's absence.
//
// Written as a function rather than a package-level slice, which leaves nothing
// for an importer to reassign.
func renderedFiles() []string {
	return []string{"smb.conf", "smbpasswd", "passwd", "network.policy"}
}

// Fingerprint is the poll loop's answer to whether anything changed.
//
// It covers the rendered files' sizes and modification times plus the detected
// scope. The scope belongs in it because it moves without anything on disk
// changing: a tunnel or a tagged network coming up alters what should be bound
// while every rendered file stays byte identical, and a fingerprint over the
// files alone would never notice.
func Fingerprint(configDir string) string {
	out := make([]byte, 0, 128)
	for _, name := range renderedFiles() {
		st, err := os.Stat(filepath.Join(configDir, name))
		if err != nil {
			out = append(out, "absent;"...)
			continue
		}
		out = append(out, fmt.Sprintf("%d:%d;", st.Size(), st.ModTime().UnixNano())...)
	}

	policy := ReadPolicy(filepath.Join(configDir, "network.policy"))
	if !policy.PinnedInterfaces {
		// A pinned configuration's lines are final, so detection cannot move
		// them and reading it would only add noise.
		if s, err := Detect(policy.AllowPublicBind); err == nil {
			out = append(out, s.Interfaces...)
		}
	}
	return string(out)
}
