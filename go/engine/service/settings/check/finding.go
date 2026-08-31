// Builds only on Linux, which is where the sandbox policy names and the SMB
// renderer this package calls into exist.
//go:build linux

// Package check probes a proposed settings change by trying it, rather than by
// describing it.
//
// Declared ranges answer only what a number may be. They cannot answer whether
// a directory can be written, whether the renderer will accept a workgroup, or
// whether a new host list still admits the administrator sending it. Each of
// those needs the value tried against the running system, which is what the
// probes here do.
//
// Separated into a package because the settings screen, the first-run form and
// the emergency editor all save the same document. Sharing one implementation
// is what stops them accepting different things.
//
// Nothing here chooses a transport. A finding carries a reason key and its
// arguments, the presentation layer maps the refusal to a status exactly once
// in its own error table, and this package imports nothing presentation.
package check

import (
	"errors"
	"fmt"
	"strings"
)

// Finding is a single observation a probe made about a proposed value.
type Finding struct {
	// Section and Field name where to put the message. Field is empty when the
	// finding is about the section as a whole.
	Section string
	Field   string

	// ReasonKey is the i18n key the client renders, and Args are its
	// substitutions in pairs of name and value. The rendering contract is the
	// client's; this package only says which key and with what.
	ReasonKey string
	Args      []string

	// Blocking refuses the save. Non-blocking findings are stored and shown:
	// an observation worth surfacing is not automatically an objection.
	Blocking bool
}

// Arg reads one argument by name, which is what a renderer and a test both
// want rather than counting positions.
func (f Finding) Arg(name string) (string, bool) {
	for i := 0; i+1 < len(f.Args); i += 2 {
		if f.Args[i] == name {
			return f.Args[i+1], true
		}
	}
	return "", false
}

// ErrRefused is a settings change at least one blocking finding refused.
//
// The presentation layer maps this to its own status exactly once, and renders
// the findings from the error's payload. No status appears in this package.
var ErrRefused = errors.New("settings: the change was refused")

// RefusedError carries the findings that refused a save.
type RefusedError struct {
	Findings []Finding
}

func (e *RefusedError) Error() string {
	blocking := Blocking(e.Findings)
	if len(blocking) == 0 {
		return ErrRefused.Error()
	}
	keys := make([]string, 0, len(blocking))
	for _, f := range blocking {
		keys = append(keys, f.ReasonKey)
	}
	return fmt.Sprintf("%s: %s", ErrRefused.Error(), strings.Join(keys, ", "))
}

func (e *RefusedError) Is(target error) bool { return target == ErrRefused }

// Refused is the error a save answers with, or nil when nothing blocks.
func Refused(findings []Finding) error {
	if !Blocked(findings) {
		return nil
	}
	return &RefusedError{Findings: findings}
}

// Blocked reports whether any finding in the list is blocking.
func Blocked(findings []Finding) bool {
	for _, f := range findings {
		if f.Blocking {
			return true
		}
	}
	return false
}

// Blocking is the subset that refuses.
func Blocking(findings []Finding) []Finding {
	out := make([]Finding, 0, len(findings))
	for _, f := range findings {
		if f.Blocking {
			out = append(out, f)
		}
	}
	return out
}

// Advisory is the non-blocking subset, which is what a screen displays
// alongside a save that succeeded.
func Advisory(findings []Finding) []Finding {
	out := make([]Finding, 0, len(findings))
	for _, f := range findings {
		if !f.Blocking {
			out = append(out, f)
		}
	}
	return out
}

// Lockout selects how a host list that drops the caller's own host is treated.
//
// The correct answer differs per surface, so the caller supplies it instead of
// this package assuming one.
type Lockout int

const (
	// LockoutBlocks refuses the save, which is what the settings screen wants.
	// There the guard is already live, so the change would take hold before any
	// correction could be submitted, and the correction is what gets rejected.
	LockoutBlocks Lockout = iota
	// LockoutWarns stores the change and surfaces the observation. It suits the
	// surfaces the guard does not cover. On the first-run form, reaching the
	// server by IP while entering the eventual DNS name is ordinary setup, and
	// is indistinguishable from an error. In the emergency editor the whole
	// purpose may be to repair a list that already excluded the operator, so a
	// refusal keyed on the current host would block the repair itself.
	LockoutWarns
)

// Input carries a proposed change together with the context the probes need.
type Input struct {
	Section string
	Body    map[string]any

	// SelfHost is the portless host the request arrived on, used by the lockout
	// probe to test the proposed list. Leave empty to skip that probe.
	SelfHost string

	// DataDir supplies the base for the default homes root when none is given.
	DataDir string
	// SMBConfigDir is the sidecar's mounted directory. Writability is probed
	// only while enabling SMB. Leave empty to skip that probe.
	SMBConfigDir string

	Lockout Lockout
}

// blocking and advisory build findings, so a caller reads the intent rather
// than a bool at the end of an argument list.
func blocking(section, field, key string, args ...string) Finding {
	return Finding{Section: section, Field: field, ReasonKey: key, Args: args, Blocking: true}
}

func advisory(section, field, key string, args ...string) Finding {
	return Finding{Section: section, Field: field, ReasonKey: key, Args: args}
}

// Sections lists the section names a settings document may contain.
//
// An allow-list rather than a pass-through, because the store keeps whatever
// name it is given: a client asking for a section that does not exist would
// create one, and the screen would then display a setting no code reads.
//
// Two of these are here because the settings snapshot offers a control for
// them. A field the interface presents as editable and this list omits is a
// save that answers "no such section" with nothing on screen explaining why:
// "rate" carries the request bounds, and "security" carries the sandbox
// policy, which has no configuration file to fall back on.
func Sections() []string {
	return []string{
		"network", "db", "symlink-policy", "homes", "smb",
		"search", "archive", "watch", "paths", "oidc",
		"rate", "security",
	}
}

// Known reports whether section is one this build accepts.
func Known(section string) bool {
	for _, s := range Sections() {
		if s == section {
			return true
		}
	}
	return false
}
