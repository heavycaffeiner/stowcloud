package smbagent

import (
	"os"
	"strings"
)

// Turning what the server rendered into what the daemon runs.
//
// Three edits and two questions. The edits: a log target the daemon's own
// configuration has no reason to carry, and the two scope lines. The
// questions: which shares are in there, and does this configuration want a
// name service, both of which decide what happens to processes afterwards.

// Policy is what the sidecar needs from the server to decide the scope.
type Policy struct {
	AllowPublicBind bool
	// PinnedInterfaces means the operator named the addresses themselves, so
	// the rendered scope lines are final and detection must not widen them.
	PinnedInterfaces bool
}

// ReadPolicy reads the flags the server wrote.
//
// A missing or unreadable file is the closed reading of both: neither is
// something to assume in the permissive direction because a file did not
// arrive.
func ReadPolicy(path string) Policy {
	body, err := os.ReadFile(path) //nolint:gosec // G304 reads the variable: the path is the agent's own configured directory.
	if err != nil {
		return Policy{}
	}
	var p Policy
	for _, line := range strings.Split(string(body), "\n") {
		switch strings.TrimSpace(line) {
		case "allow_public_bind=1":
			p.AllowPublicBind = true
		case "pinned_interfaces=1":
			p.PinnedInterfaces = true
		}
	}
	return p
}

// logDirectives is where authentication failures go, so a ban daemon has
// something to tail.
//
// That audit class at level three is the only setting at which the daemon logs
// a failed authentication at all: the plain global level never emits one,
// confirmed against a real bad-password connection. Raising just that class
// keeps the global level low rather than turning on per-request chatter.
//
// A single file, not the per-client split some examples use: the filter
// shipped beside this names one file, and a split log would scatter the
// failures across files it never looks at.
const logDirectives = "  log file = /var/log/samba/log.smbd\n  log level = 1 auth_audit:3\n"

// Candidate builds what gets validated and promoted.
//
// A nil scope means the operator pinned the addresses, where the rendered
// lines are already the final answer.
//
// The scope lines are substituted, never inserted: the server always renders
// both, and a file missing them is one this should not be widening anyway.
func Candidate(src string, scope *Scope) string {
	lines := strings.Split(strings.TrimSuffix(src, "\n"), "\n")
	out := make([]byte, 0, len(src)+len(logDirectives))

	for i, line := range lines {
		if i == 0 {
			// The server always emits the global section as the literal first
			// line, so this lands inside it without this file needing to know
			// anything else about the shape.
			out = append(out, line...)
			out = append(out, '\n')
			out = append(out, logDirectives...)
			continue
		}
		switch {
		case scope != nil && isDirective(line, "interfaces"):
			out = append(out, "  interfaces = "...)
			out = append(out, scope.Interfaces...)
			out = append(out, '\n')
		case scope != nil && isDirective(line, "hosts allow"):
			out = append(out, "  hosts allow = "...)
			out = append(out, scope.HostsAllow...)
			out = append(out, '\n')
		default:
			out = append(out, line...)
			out = append(out, '\n')
		}
	}
	return string(out)
}

// isDirective matches a name followed by an equals sign, with the daemon's
// tolerance for whitespace but not for a comment that happens to contain the
// word.
func isDirective(line, name string) bool {
	t := strings.TrimLeft(line, " \t")
	rest, ok := strings.CutPrefix(t, name)
	if !ok {
		return false
	}
	return strings.HasPrefix(strings.TrimLeft(rest, " \t"), "=")
}

func directiveValue(line, name string) (string, bool) {
	if !isDirective(line, name) {
		return "", false
	}
	_, v, ok := strings.Cut(line, "=")
	if !ok {
		return "", false
	}
	return strings.TrimSpace(v), true
}

// Section is one share and the path it serves.
type Section struct {
	Name string
	Path string
}

// Sections lists every share in a configuration, the global one excluded.
//
// Read back from the promoted file rather than tracked alongside it, so what
// gets reported is what the daemon is actually serving.
func Sections(conf string) []Section {
	var out []Section
	var current *Section

	for _, line := range strings.Split(conf, "\n") {
		t := strings.TrimSpace(line)
		if name, ok := strings.CutPrefix(t, "["); ok {
			if name, ok = strings.CutSuffix(name, "]"); ok {
				if current != nil {
					out = append(out, *current)
					current = nil
				}
				if !strings.EqualFold(name, "global") {
					current = &Section{Name: name}
				}
				continue
			}
		}
		if current != nil {
			if v, ok := directiveValue(line, "path"); ok {
				current.Path = v
			}
		}
	}
	if current != nil {
		out = append(out, *current)
	}
	return out
}

// NetbiosWanted reports whether this configuration wants the name service
// running.
//
// Read back from the promoted file rather than from the environment, which is
// what lets a settings change take effect on the next apply instead of on the
// next restart.
func NetbiosWanted(conf string) bool {
	found := false
	disabled := false
	for _, line := range strings.Split(conf, "\n") {
		if v, ok := directiveValue(line, "disable netbios"); ok {
			found = true
			switch strings.ToLower(v) {
			case "yes", "true", "1":
				disabled = true
			default:
				disabled = false
			}
		}
	}
	return found && !disabled
}

// BoundInterfaces is the bind line of a promoted configuration: what the
// running daemon bound, which is the one thing a reload cannot change.
func BoundInterfaces(conf string) string {
	out := ""
	for _, line := range strings.Split(conf, "\n") {
		if v, ok := directiveValue(line, "interfaces"); ok {
			out = v
		}
	}
	return out
}
