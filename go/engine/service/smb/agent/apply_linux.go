//go:build linux

package agent

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/clock"
)

// Single apply operation: parse server output, determine network scope, check
// validity, install the configuration, sync user accounts, and signal the
// daemon only when required.
//
// Failures preserve the active configuration. When a proposed configuration is
// invalid, when an account name conflicts with the system, or when interfaces
// cannot be enumerated, the daemon continues serving its existing state. The
// error becomes a report sent to the server, not a line buried in logs.

// Paths holds every filesystem location the agent touches, allowing tests to
// use temporary directories and allowing production deployments to override
// the two that vary between environments.
type Paths struct {
	// ConfigDir holds server output. Read only mount.
	ConfigDir string
	// StateDir holds temporary files: proposed configuration and validation
	// results.
	StateDir string
	// SmbConf is the daemon's configuration file.
	SmbConf string
	Passdb  string
	Passwd  string
	Group   string
}

// DefaultPaths returns the standard container filesystem layout.
func DefaultPaths() Paths {
	return Paths{ //nolint:gosec // G101 misreads file paths as embedded credentials: these point to on-disk locations, none of which appear in this source tree.
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

// Agent executes the configuration rendered by the server.
type Agent struct {
	paths Paths
	log   *slog.Logger
	clock clock.Clock

	// Serializes apply operations. Fields below are accessed from the polling
	// thread and from the control socket handler.
	mu   sync.Mutex
	smbd *Smbd
	// bound holds the interface binding that the active daemon process uses.
	// Checked by comparison: reloading the daemon cannot change this setting.
	bound string
	// promoted holds the previously installed configuration text. When content
	// matches, no work is needed.
	promoted string
	last     Report
}

// NewAgent makes one.
func NewAgent(paths Paths, mode Mode, log *slog.Logger, clk clock.Clock) *Agent {
	return &Agent{paths: paths, log: log, clock: clk, smbd: NewSmbd(mode, log)}
}

// Last returns the most recent apply result, used by status queries.
func (a *Agent) Last() Report {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.last
}

// SmbdRunning returns true when the daemon process is alive. Called only by
// the supervisor: no other code tracks this agent's child process.
func (a *Agent) SmbdRunning() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.smbd.Running()
}

// StartFail2ban enables brute-force defense, layered with the host allowlist
// and mandatory authentication.
func (a *Agent) StartFail2ban() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.smbd.StartFail2ban()
}

// Fingerprint is a cheap "has anything changed" for the poll loop.
func (a *Agent) Fingerprint() string { return Fingerprint(a.paths.ConfigDir) }

// Apply runs one pass.
func (a *Agent) Apply(ctx context.Context) Report {
	a.mu.Lock()
	defer a.mu.Unlock()
	report := a.apply(ctx)
	a.last = report
	return report
}

func (a *Agent) apply(ctx context.Context) Report {
	if err := os.MkdirAll(a.paths.StateDir, 0o750); err != nil {
		return FailedReport(fmt.Sprintf("the state directory: %v", err))
	}

	src, err := os.ReadFile(a.paths.renderedConf()) //nolint:gosec // G304 flags variable path: the input is this agent's configured directory.
	if err != nil {
		if os.IsNotExist(err) {
			// Missing configuration means disabled: the server deletes rendered
			// output when the feature is turned off. Treating absence as "not
			// ready yet" would allow a disabled service to keep serving.
			return a.teardown(ctx)
		}
		return FailedReport(fmt.Sprintf("reading the rendered configuration: %v", err))
	}

	policy := ReadPolicy(a.paths.policy())
	var warnings []string

	var scope *Scope
	if !policy.PinnedInterfaces {
		s, derr := Detect(policy.AllowPublicBind)
		if derr != nil {
			// Cannot proceed: interface detection failed, so the agent has no
			// usable scope. Existing configuration remains active.
			return FailedReport(fmt.Sprintf("reading this machine's interfaces: %v", derr))
		}
		if !s.Detected {
			warnings = append(warnings, "no usable network interface was found, so SMB answers on loopback only; check that this container sees the host's network")
		}
		scope = &s
	}

	candidate := Candidate(string(src), scope)
	if werr := os.WriteFile(a.paths.candidate(), []byte(candidate), 0o600); werr != nil { //nolint:gosec // G703 flags tracing the state directory into a file write: it is this agent's scratch space, not user input.
		return FailedReport(fmt.Sprintf("writing the candidate configuration: %v", werr))
	}
	if verr := Testparm(ctx, a.paths.candidate()); verr != nil {
		return FailedReport(fmt.Sprintf("%v; keeping the previous configuration", verr))
	}

	// Account validation happens before installing anything. A failure at this
	// stage means the account roster is unusable in its current form.
	renderedPasswd, _ := os.ReadFile(a.paths.renderedPasswd()) //nolint:errcheck,gosec // an absent roster is an empty one, which is what a deployment with no SMB accounts looks like.
	desired := ParseRendered(string(renderedPasswd))

	currentPasswd, perr := os.ReadFile(a.paths.Passwd) //nolint:gosec // G304 flags variable path: the input is this agent's configured file.
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

	// Installation phase begins. Steps above could reject, steps below commit.
	if werr := Promote(a.paths.SmbConf, candidate); werr != nil {
		return FailedReport(fmt.Sprintf("promoting %s: %v", a.paths.SmbConf, werr))
	}

	if werr := WritePasswd(a.paths.Passwd, Rebuild(string(currentPasswd), desired)); werr != nil {
		warnings = append(warnings, fmt.Sprintf("rebuilding %s: %v", a.paths.Passwd, werr))
	}
	if _, serr := os.Stat(a.paths.smbpasswd()); serr == nil {
		if ierr := Import(ctx, a.paths.smbpasswd(), a.paths.Passdb); ierr != nil {
			warnings = append(warnings, ierr.Error())
		}
	}
	if _, perr := Prune(ctx, desired); perr != nil {
		warnings = append(warnings, fmt.Sprintf("pruning the credential database: %v", perr))
	}
	missingPassdb, _ := MissingPassdb(ctx, desired) //nolint:errcheck // a database that cannot be listed is already reported by the prune above.
	if len(missingPassdb) > 0 {
		warnings = append(warnings, "no credential exists for "+strings.Join(missingPassdb, ", ")+": they cannot authenticate over SMB")
	}

	// Current active shares served by the daemon.
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

	hostsAllow := HostsAllowOf(candidate)
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

// settle signals the daemon with the minimum necessary action.
//
// Settle chooses the action and Tell executes it. This method contributes
// state known only to the agent: the interface binding used by the running
// daemon and the content of the last installed configuration.
func (a *Agent) settle(candidate, wanted string) (SmbdAction, error) {
	action := Settle(SettleInput{
		Running:   a.smbd.Running(),
		Bound:     a.bound,
		Wanted:    wanted,
		Promoted:  a.promoted,
		Candidate: candidate,
	})

	done, err := Tell(a.smbd, action)
	if err != nil {
		return done, err
	}

	a.promoted = candidate
	if action == ActionStarted || action == ActionRestarted {
		a.bound = wanted
	}
	return done, nil
}

// teardown handles disablement: when the feature is toggled off or the server
// clears the configuration directory.
//
// Halts the service and removes managed accounts along with their passwords.
// Leaving credentials behind would allow authentication after revocation.
func (a *Agent) teardown(ctx context.Context) Report {
	if err := a.smbd.Stop(); err != nil {
		a.log.Warn("the daemon did not stop cleanly", "error", err)
	}
	a.bound = ""
	a.promoted = ""
	if _, err := Prune(ctx, nil); err != nil {
		a.log.Warn("the credentials could not be pruned, so a revoked one may still work", "error", err)
	}
	if current, err := os.ReadFile(a.paths.Passwd); err == nil { //nolint:gosec // G304 flags variable path: the input is this agent's configured file.
		if werr := WritePasswd(a.paths.Passwd, Rebuild(string(current), nil)); werr != nil {
			a.log.Warn("the managed accounts could not be removed", "error", werr)
		}
	}
	return Report{OK: true, Smbd: ActionStopped}
}

// Shutdown halts the daemon process owned by this agent.
//
// Required under process supervision: the daemon is a child of this process.
// Leaving it alive causes the next launch to encounter a bound port.
func (a *Agent) Shutdown() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.smbd.Stop()
}
