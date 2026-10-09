//go:build linux

package agent

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Telling the daemon as little as will do.
//
// A reload cannot rebind sockets or revoke an authenticated tree connection.
// Changed interfaces, credentials, or share permissions therefore restart the
// daemon. Repeated publication of identical authority leaves sessions intact.

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
	// Existing sessions retain authentication and tree connections after a
	// reload. Authority changes must terminate those connections immediately.
	RevokeConnections bool
}

// Settle decides what the daemon has to be told.
//
// A pure function over its inputs, so the table below is testable without
// a daemon to drive:
//
//	not running                  -> start
//	the bind line moved          -> restart
//	authority changed            -> restart
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
	case in.Bound != in.Wanted || in.RevokeConnections:
		return ActionRestarted
	case in.Promoted == in.Candidate:
		return ActionUnchanged
	default:
		return ActionReloaded
	}
}

// Publication timestamps are not authority. Repeated publication of unchanged
// hashes leaves sessions alone; credentials or share changes disconnect them.
func authorityFingerprint(conf, credentials string) [sha256.Size]byte {
	stable := append([]byte(conf), 0)
	for _, line := range strings.Split(credentials, "\n") {
		fields := strings.Split(line, ":")
		if len(fields) >= 5 {
			stable = append(stable, strings.Join(fields[:5], ":")...)
		} else {
			stable = append(stable, line...)
		}
		stable = append(stable, '\n')
	}
	return sha256.Sum256(stable)
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
