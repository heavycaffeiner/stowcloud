//go:build linux

package smbagent

import (
	"fmt"
	"log/slog"

	"github.com/heavycaffeiner/stowcloud/go/internal/clock"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// One apply: read what the server rendered, decide the scope, validate,
// promote, reconcile accounts, and tell the daemon as little as will do.
//
// Every failure keeps the previously promoted configuration running. A rejected
// candidate, a colliding account or an unreadable interface list all leave the
// daemon serving what it was already serving, and turn into a report the caller
// sends back to the server rather than a log line nobody reads.

// Paths is everything this agent reads or writes, so a test can point it at a
// temporary directory and so a bare-metal install can move the two that differ
// there.
type Paths struct {
	// ConfigDir is what the server writes. Mounted read only.
	ConfigDir string
	// StateDir is scratch: the candidate configuration and the validator's
	// output.
	StateDir string
	// SmbConf is what the daemon reads.
	SmbConf string
	Passdb  string
	Passwd  string
	Group   string
}

// DefaultPaths is the container layout.
func DefaultPaths() Paths {
	return Paths{ //nolint:gosec // G101 reads the database path as a literal credential: these are file locations, and the file itself is never in this tree.
		ConfigDir: "/config/smb",
		StateDir:  "/var/lib/sc-smb-agent",
		SmbConf:   "/etc/samba/smb.conf",
		Passdb:    "/var/lib/samba/private/passdb.tdb",
		Passwd:    "/etc/passwd",
		Group:     "/etc/group",
	}
}

func (p Paths) renderedConf() string   { return filepath.Join(p.ConfigDir, "smb.conf") }
func (p Paths) renderedPasswd() string { return filepath.Join(p.ConfigDir, "passwd") }
func (p Paths) smbpasswd() string      { return filepath.Join(p.ConfigDir, "smbpasswd") }
func (p Paths) policy() string         { return filepath.Join(p.ConfigDir, "network.policy") }
func (p Paths) candidate() string      { return filepath.Join(p.StateDir, "smb.conf.candidate") }

// Agent applies what the server renders.
type Agent struct {
	paths Paths
	log   *slog.Logger
	clock clock.Clock

	// One apply at a time. Everything below is reachable from both the poll
	// loop and the control socket.
	mu   sync.Mutex
	smbd *Smbd
	// bound is the bind line the running daemon was started with. Compared,
	// not assumed: it is the one directive a reload cannot apply.
	bound string
	// promoted is the configuration as promoted last time, so an unchanged one
	// costs nothing.
	promoted string
	last     Report
}

// NewAgent makes one.
func NewAgent(paths Paths, mode Mode, log *slog.Logger, clk clock.Clock) *Agent {
	return &Agent{paths: paths, log: log, clock: clk, smbd: NewSmbd(mode, log)}
}

// NeedsRestart reports whether the bind line moved.
//
// The daemon binds its listening sockets at startup and a reload does not
// revisit them, so this is the whole of "a reload will not do".
func NeedsRestart(bound, wanted string) bool { return bound != wanted }

// Last is the previous apply's answer, for a status request.
func (a *Agent) Last() Report {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.last
}

// SmbdRunning reports whether the daemon is up. Only the supervising loop
// asks: nothing else is watching a process this agent owns.
func (a *Agent) SmbdRunning() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.smbd.Running()
}

// StartFail2ban starts brute-force mitigation, on top of the admission list
// and required authentication rather than instead of them.
func (a *Agent) StartFail2ban() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.smbd.StartFail2ban()
}

// Fingerprint is a cheap "has anything changed" for the poll loop: the
// rendered files' sizes and modification times, plus the detected scope, which
// moves when a tunnel or a tagged network comes up without anything on disk
// changing.
func (a *Agent) Fingerprint() string {
	out := make([]byte, 0, 128)
	for _, name := range []string{"smb.conf", "smbpasswd", "passwd", "network.policy"} {
		st, err := os.Stat(filepath.Join(a.paths.ConfigDir, name))
		if err != nil {
			out = append(out, "absent;"...)
			continue
		}
		out = append(out, fmt.Sprintf("%d:%d;", st.Size(), st.ModTime().UnixNano())...)
	}
	policy := ReadPolicy(a.paths.policy())
	if !policy.PinnedInterfaces {
		if s, err := Detect(policy.AllowPublicBind); err == nil {
			out = append(out, s.Interfaces...)
		}
	}
	return string(out)
}

// Apply runs one pass.
func (a *Agent) Apply() Report {
	a.mu.Lock()
	defer a.mu.Unlock()
	report := a.apply()
	a.last = report
	return report
}

func (a *Agent) apply() Report {
	if err := os.MkdirAll(a.paths.StateDir, 0o750); err != nil {
		return FailedReport(fmt.Sprintf("the state directory: %v", err))
	}

	src, err := os.ReadFile(a.paths.renderedConf()) //nolint:gosec // G304 reads the variable: the path is the agent's own configured directory.
	if err != nil {
		if os.IsNotExist(err) {
			// Absence is the off switch: the server removes the rendered files
			// when the setting goes false, and reading that as "not synced
			// yet" would leave SMB serving a revoked configuration.
			return a.teardown()
		}
		return FailedReport(fmt.Sprintf("reading the rendered configuration: %v", err))
	}

	policy := ReadPolicy(a.paths.policy())
	var warnings []string

	var scope *Scope
	if !policy.PinnedInterfaces {
		s, derr := Detect(policy.AllowPublicBind)
		if derr != nil {
			// Not promoted: a scope this agent could not read is not a scope,
			// and the previous configuration is still serving.
			return FailedReport(fmt.Sprintf("reading this machine's interfaces: %v", derr))
		}
		if !s.Detected {
			warnings = append(warnings, "no usable network interface was found, so SMB answers on loopback only; check that this container sees the host's network")
		}
		scope = &s
	}

	candidate := Candidate(string(src), scope)
	if werr := os.WriteFile(a.paths.candidate(), []byte(candidate), 0o600); werr != nil { //nolint:gosec // G703 traces the configured state directory into a write: it is this agent's own scratch path, never caller data.
		return FailedReport(fmt.Sprintf("writing the candidate configuration: %v", werr))
	}
	if verr := testparm(a.paths.candidate()); verr != nil {
		return FailedReport(fmt.Sprintf("%v; keeping the previous configuration", verr))
	}

	// Accounts are checked before anything is promoted, because a refusal here
	// means the rendered roster cannot be applied at all.
	renderedPasswd, _ := os.ReadFile(a.paths.renderedPasswd()) //nolint:errcheck,gosec // an absent roster is an empty one, which is what a deployment with no SMB accounts looks like.
	desired := ParseRendered(string(renderedPasswd))

	currentPasswd, perr := os.ReadFile(a.paths.Passwd) //nolint:gosec // G304 reads the variable: the path is this agent's own configuration.
	if perr != nil {
		return FailedReport(fmt.Sprintf("reading %s: %v", a.paths.Passwd, perr))
	}
	groupFile, _ := os.ReadFile(a.paths.Group) //nolint:errcheck,gosec // an unreadable group file makes every group missing, which the check below reports.

	if c := Collisions(desired, string(currentPasswd)); len(c) > 0 {
		return FailedReport("refusing to sync: " + strings.Join(c, "; "))
	}
	if m := MissingGroups(desired, string(groupFile)); len(m) > 0 {
		return FailedReport("refusing to sync: no group exists for " + strings.Join(m, ", "))
	}

	// Promotion starts here. Everything above could refuse; nothing below can.
	if werr := os.WriteFile(a.paths.SmbConf, []byte(candidate), 0o644); werr != nil { //nolint:gosec // G306 wants stricter: the daemon reads this as its own user, and the file holds no secret.
		return FailedReport(fmt.Sprintf("promoting %s: %v", a.paths.SmbConf, werr))
	}

	if werr := WritePasswd(a.paths.Passwd, Rebuild(string(currentPasswd), desired)); werr != nil {
		warnings = append(warnings, fmt.Sprintf("rebuilding %s: %v", a.paths.Passwd, werr))
	}
	if _, serr := os.Stat(a.paths.smbpasswd()); serr == nil {
		if ierr := Import(a.paths.smbpasswd(), a.paths.Passdb); ierr != nil {
			warnings = append(warnings, ierr.Error())
		}
	}
	if _, perr := Prune(desired); perr != nil {
		warnings = append(warnings, fmt.Sprintf("pruning the credential database: %v", perr))
	}
	missingPassdb, _ := MissingPassdb(desired) //nolint:errcheck // a database that cannot be listed is already reported by the prune above.
	if len(missingPassdb) > 0 {
		warnings = append(warnings, "no credential exists for "+strings.Join(missingPassdb, ", ")+": they cannot authenticate over SMB")
	}

	// What the daemon is now serving.
	sections := Sections(candidate)
	var missingPaths []string
	shares := make([]string, 0, len(sections))
	for _, s := range sections {
		shares = append(shares, s.Name)
		if s.Path == "" {
			continue
		}
		if st, serr := os.Stat(s.Path); serr != nil || !st.IsDir() {
			missingPaths = append(missingPaths, s.Path)
		}
	}
	if len(missingPaths) > 0 {
		warnings = append(warnings, "these share paths do not exist here, so a client is told the network name is invalid: "+
			strings.Join(missingPaths, ", ")+". Mount them into this container at the same paths.")
	}

	wanted := BoundInterfaces(candidate)
	action, aerr := a.settle(candidate, wanted)
	if aerr != nil {
		warnings = append(warnings, "the daemon: "+aerr.Error())
	}
	a.smbd.Nmbd(NetbiosWanted(candidate))

	hostsAllow := hostsAllowOf(candidate)
	if scope != nil {
		hostsAllow = scope.HostsAllow
	}

	report := Report{
		OK:            len(warnings) == 0,
		Shares:        shares,
		Interfaces:    wanted,
		HostsAllow:    hostsAllow,
		Smbd:          action,
		MissingPaths:  missingPaths,
		MissingPassdb: missingPassdb,
	}
	if len(warnings) > 0 {
		report.Error = strings.Join(warnings, " | ")
	}
	return report
}

// settle tells the daemon as little as will do, and no less.
func (a *Agent) settle(candidate, wanted string) (SmbdAction, error) {
	var action SmbdAction
	switch {
	case !a.smbd.Running():
		action = ActionStarted
	case NeedsRestart(a.bound, wanted):
		action = ActionRestarted
	case a.promoted == candidate:
		action = ActionUnchanged
	default:
		action = ActionReloaded
	}

	var err error
	switch action {
	case ActionStarted:
		err = a.smbd.Start()
	case ActionRestarted:
		err = a.smbd.Restart()
	case ActionReloaded:
		err = a.smbd.Reload()
	case ActionUnchanged, ActionStopped, ActionFailed:
	}
	if err != nil {
		return ActionFailed, err
	}

	a.promoted = candidate
	if action == ActionStarted || action == ActionRestarted {
		a.bound = wanted
	}
	return action, nil
}

// teardown runs when the setting went false, or the configuration directory
// was emptied.
//
// Stop serving and take the managed accounts and their credentials with it:
// leaving them behind would keep a revoked credential working.
func (a *Agent) teardown() Report {
	if err := a.smbd.Stop(); err != nil {
		a.log.Warn("the daemon did not stop cleanly", "error", err)
	}
	a.bound = ""
	a.promoted = ""
	if _, err := Prune(nil); err != nil {
		a.log.Warn("the credentials could not be pruned, so a revoked one may still work", "error", err)
	}
	if current, err := os.ReadFile(a.paths.Passwd); err == nil { //nolint:gosec // G304 reads the variable: the path is this agent's own configuration.
		if werr := WritePasswd(a.paths.Passwd, Rebuild(string(current), nil)); werr != nil {
			a.log.Warn("the managed accounts could not be removed", "error", werr)
		}
	}
	return Report{OK: true, Smbd: ActionStopped}
}

// Shutdown stops a daemon this agent owns.
//
// Under supervision that matters: the daemon is this process's child, and
// leaving it running means the next start finds the port taken.
func (a *Agent) Shutdown() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.smbd.Stop()
}

// LogReport writes one line per apply, whoever asked for it.
//
// The source is the half that used to be missing. An apply that arrives on the
// control socket is the interesting one, because it is the server reacting to
// an administrator, and it logged nothing at all when it succeeded, so the only
// evidence in the log was the next poll finding nothing left to do.
func LogReport(log *slog.Logger, r Report, source string) {
	if r.OK {
		log.Info("applied", "source", source, "shares", len(r.Shares),
			"interfaces", r.Interfaces, "daemon", string(r.Smbd))
		return
	}
	reason := r.Error
	if reason == "" {
		reason = "the apply failed"
	}
	log.Error(reason, "source", source, "interfaces", r.Interfaces, "daemon", string(r.Smbd))
}

// testparm validates before the configuration can reach the daemon.
//
// It does not check that a share's path exists, which is why the apply checks
// that separately.
func testparm(candidate string) error {
	out, err := exec.Command("testparm", "-s", filepath.Clean(candidate)).CombinedOutput() //nolint:gosec // G204 flags a variable command: the path is the agent's own state directory.
	if err != nil {
		return fmt.Errorf("the validator rejected the candidate configuration: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

func hostsAllowOf(conf string) string {
	out := ""
	for _, line := range strings.Split(conf, "\n") {
		if v, ok := directiveValue(line, "hosts allow"); ok {
			out = v
		}
	}
	return out
}
