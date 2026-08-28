//go:build linux

package agent

import (
	"fmt"
	"net"
	"net/netip"
	"os"
	"strings"
)

// Reading this machine's own devices, and the configuration read-backs.

// Devices reports each network device that is up, is not loopback, and has at
// least one configured address.
//
// This is the only function here that asks the operating system anything. What
// it returns is folded by Compute, a pure function, so the decision determining
// who can reach SMB is testable on any machine rather than only on one with the
// right interfaces.
//
// An earlier version shelled out to a command and swallowed the failure. On an
// image lacking that command the scan came back empty, the closed configuration
// reached the promoted file, validation passed it, and SMB served loopback and
// nothing else with no record anywhere. Reading the interfaces directly
// removes the dependency, and an empty answer becomes something the caller must
// report rather than something resembling a decision.
func Devices() ([]Device, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("reading this machine's network devices: %w", err)
	}

	var out []Device
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, aerr := ifc.Addrs()
		if aerr != nil {
			// One unreadable device is no reason to report none: the others are
			// still what this machine is attached to.
			continue
		}
		if parsed := addrsOf(addrs); len(parsed) > 0 {
			out = append(out, Device{Name: ifc.Name, Addrs: parsed, Veth: veth(ifc.Name)})
		}
	}
	return out, nil
}

// addrsOf converts one device's addresses, judging each as the family it
// actually is.
func addrsOf(addrs []net.Addr) []netip.Addr {
	var parsed []netip.Addr
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip, ok := netip.AddrFromSlice(ipnet.IP)
		if !ok {
			continue
		}
		// An address of one family wrapped in the other is treated as the family
		// it truly belongs to, which is the classifier's rule as well.
		parsed = append(parsed, ip.Unmap())
	}
	return parsed
}

// Detect gathers the devices and folds them into a scope.
//
// An error belongs to the caller to report. It means this machine's interfaces
// could not be read, which is no reason to promote a configuration saying
// loopback and call that a scope.
func Detect(allowPublic bool) (Scope, error) {
	devices, err := Devices()
	if err != nil {
		return Scope{}, err
	}
	return Compute(devices, allowPublic), nil
}

// Policy is what the agent needs from the server to decide the scope.
type Policy struct {
	AllowPublicBind bool

	// PinnedInterfaces records that the operator supplied the addresses, making
	// the rendered scope lines final and putting them beyond detection's
	// reach.
	PinnedInterfaces bool
}

// ReadPolicy interprets the flag file the server produced.
//
// A missing or unreadable file reads as both flags off. Neither is assumed
// permissive merely because a file failed to arrive.
func ReadPolicy(path string) Policy {
	body, err := os.ReadFile(path) //nolint:gosec // G304: the path is the agent's own configured directory.
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

// logDirectives send authentication failures somewhere a ban daemon can tail.
//
// That audit class at level three is the only setting under which the daemon
// logs a failed authentication at all; the plain global level never emits one.
// Raising just that class keeps the global level low rather than enabling
// per-request chatter.
//
// One file rather than the per-client split some examples use, because the
// filter shipped beside this names one file and a split log would scatter the
// failures across files it never reads.
const logDirectives = "  log file = /var/log/samba/log.smbd\n  log level = 1 auth_audit:3\n"

// Candidate assembles the configuration that validation and promotion act on.
//
// A nil scope means the operator pinned the addresses, where the rendered lines
// are already the final answer.
//
// The scope lines are substituted and never inserted. The server always renders
// both, so a file missing them is one this should not be widening at all.
func Candidate(src string, scope *Scope) string {
	lines := strings.Split(strings.TrimSuffix(src, "\n"), "\n")
	out := make([]byte, 0, len(src)+len(logDirectives))

	for i, line := range lines {
		if i == 0 {
			// The global section is always the literal first line the server
			// emits, so appending here lands inside it without this code
			// knowing anything further about the layout.
			out = append(out, line...)
			out = append(out, '\n')
			out = append(out, logDirectives...)
			continue
		}
		switch {
		case scope != nil && isDirective(line, "interfaces"):
			out = appendDirective(out, "interfaces", scope.Interfaces)
		case scope != nil && isDirective(line, "hosts allow"):
			out = appendDirective(out, "hosts allow", scope.HostsAllow)
		default:
			out = append(out, line...)
			out = append(out, '\n')
		}
	}
	return string(out)
}

func appendDirective(out []byte, name, value string) []byte {
	out = append(out, "  "...)
	out = append(out, name...)
	out = append(out, " = "...)
	out = append(out, value...)
	return append(out, '\n')
}

// isDirective recognises a name followed by an equals sign, allowing the
// whitespace the daemon allows while refusing a comment that merely mentions
// the word.
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

// Section pairs a share with the path behind it.
type Section struct {
	Name string
	Path string
}

// Sections lists every share in a configuration, excluding the global one.
//
// Derived from the promoted file rather than kept alongside it, so the report
// describes what the daemon is genuinely serving.
func Sections(conf string) []Section {
	var out []Section
	var current *Section

	for _, line := range strings.Split(conf, "\n") {
		if name, ok := sectionHeader(line); ok {
			if current != nil {
				out = append(out, *current)
				current = nil
			}
			if !strings.EqualFold(name, "global") {
				current = &Section{Name: name}
			}
			continue
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

// sectionHeader reports the name a bracketed line opens.
func sectionHeader(line string) (string, bool) {
	t := strings.TrimSpace(line)
	name, ok := strings.CutPrefix(t, "[")
	if !ok {
		return "", false
	}
	return strings.CutSuffix(name, "]")
}

// NetbiosWanted reports whether the configuration calls for the name service.
//
// Derived from the promoted file rather than from the environment, letting a
// settings change take hold at the next apply instead of waiting for a
// restart.
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

// BoundInterfaces is a promoted configuration's bind line: what the running
// daemon bound, which is the one thing a reload cannot change.
func BoundInterfaces(conf string) string {
	out := ""
	for _, line := range strings.Split(conf, "\n") {
		if v, ok := directiveValue(line, "interfaces"); ok {
			out = v
		}
	}
	return out
}

// HostsAllowOf is a promoted configuration's admission line, reported so a
// question about why nothing connects has both halves of the answer.
func HostsAllowOf(conf string) string {
	out := ""
	for _, line := range strings.Split(conf, "\n") {
		if v, ok := directiveValue(line, "hosts allow"); ok {
			out = v
		}
	}
	return out
}
