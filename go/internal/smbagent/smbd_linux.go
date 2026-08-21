//go:build linux

package smbagent

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// Starting, reloading and replacing the daemon, and the one distinction that
// matters: it binds its listening sockets once, at startup.
//
// Telling it to reload rereads shares, users and permissions in place, and
// does not revisit those sockets. A changed bind line therefore needs the
// process replaced, not reloaded. Before this agent existed the sidecar
// reloaded either way, so a container that came up before its network did
// stayed bound to loopback for as long as it ran, with a promoted
// configuration on disk that said otherwise.

// ModeKind is who owns the daemon process.
type ModeKind int

const (
	// ModeSupervise means this agent does: it spawns the daemon as its own
	// child and replaces it. The container case, where there is no service
	// manager to ask.
	ModeSupervise ModeKind = iota
	// ModeService means a service manager does, under a unit name. The
	// bare-metal case.
	ModeService
)

// Mode is how the daemon is controlled.
type Mode struct {
	Kind ModeKind
	Unit string
}

// DetectMode picks the control method.
//
// Two unit names because the distributions disagree, then the other init
// system's, then supervision when there is neither.
func DetectMode() Mode {
	if which("systemctl") {
		for _, unit := range []string{"smb", "smbd"} {
			cmd := exec.Command("systemctl", "cat", unit+".service") //nolint:gosec // G204 flags a variable command: the unit name is one of the two literals above.
			cmd.Stdout, cmd.Stderr = nil, nil
			if err := cmd.Run(); err == nil {
				return Mode{Kind: ModeService, Unit: unit}
			}
		}
		return Mode{Kind: ModeService, Unit: "smbd"}
	}
	if which("rc-service") {
		return Mode{Kind: ModeService, Unit: "samba"}
	}
	return Mode{Kind: ModeSupervise}
}

func which(bin string) bool {
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" {
			continue
		}
		if st, err := os.Stat(filepath.Join(dir, bin)); err == nil && !st.IsDir() { //nolint:gosec // G703 traces the search path to a stat: this is what looking up a program on the path is, and the name is a literal at every call.
			return true
		}
	}
	return false
}

// Smbd controls the daemon and, when the configuration asks for it, the name
// service beside it.
type Smbd struct {
	mode Mode
	log  *slog.Logger
	// Only ever set under supervision.
	child *exec.Cmd
	nmbd  *exec.Cmd
}

// NewSmbd makes a controller.
func NewSmbd(mode Mode, log *slog.Logger) *Smbd {
	return &Smbd{mode: mode, log: log}
}

// alive reports whether a spawned child is still running, reaping it if it is
// not.
//
// Reaping matters: without it a daemon that crashed lingers as a zombie until
// this process exits, and this agent is the init process of its container, so
// that is for as long as the container runs.
func alive(cmd *exec.Cmd) bool {
	if cmd == nil || cmd.Process == nil {
		return false
	}
	var status syscall.WaitStatus
	pid, err := syscall.Wait4(cmd.Process.Pid, &status, syscall.WNOHANG, nil)
	if err != nil {
		return false
	}
	// Zero means it is still running and nothing was reaped.
	return pid == 0
}

// Running reports whether the daemon is up as far as this agent can tell.
// Under supervision that is exact; under a service manager it is what the
// manager says.
func (s *Smbd) Running() bool {
	if s.mode.Kind == ModeSupervise {
		return alive(s.child)
	}
	ok, err := service(s.mode.Unit, "is-active")
	return err == nil && ok
}

// Start brings the daemon up.
func (s *Smbd) Start() error {
	if s.mode.Kind == ModeService {
		return require(s.mode.Unit, "start")
	}
	cmd := exec.Command("smbd", "--foreground", "--no-process-group")
	// Inherited output on purpose: the daemon's own diagnostics are what the
	// container's logs show.
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	// Its own process group, so stopping it reaches the per-connection
	// children it forks. Signalling only the parent leaves those running, and
	// a restart then finds the port still held by a child of the daemon that
	// was just replaced.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting the daemon: %w", err)
	}
	s.child = cmd
	return nil
}

// Reload rereads the configuration. Cheap, and wrong for anything that moves a
// socket.
func (s *Smbd) Reload() error {
	out, err := exec.Command("smbcontrol", "all", "reload-config").CombinedOutput()
	if err == nil {
		return nil
	}
	// A service manager can reload a daemon whose control socket this agent
	// cannot reach. Supervision has no second way to ask.
	if s.mode.Kind == ModeService {
		return require(s.mode.Unit, "reload")
	}
	return fmt.Errorf("reloading the configuration: %w: %s", err, strings.TrimSpace(string(out)))
}

// Restart is the only thing that moves the listening sockets.
func (s *Smbd) Restart() error {
	if s.mode.Kind == ModeService {
		return require(s.mode.Unit, "restart")
	}
	if err := s.Stop(); err != nil {
		return err
	}
	return s.Start()
}

// Stop takes the daemon and the name service down.
func (s *Smbd) Stop() error {
	s.Nmbd(false)
	if s.mode.Kind == ModeService {
		return require(s.mode.Unit, "stop")
	}
	if s.child != nil {
		kill(s.child)
		s.child = nil
	}
	return nil
}

// kill ends a child and everything it forked, then reaps it so it does not
// linger as a zombie.
func kill(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	// The whole group, because the daemon forks a child per connection and
	// those hold the listening socket open. The negative number is what makes
	// this the group rather than the one process.
	//
	// A process that already exited cannot be signalled, and that is not a
	// failure: the reap below is what this is for either way.
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) //nolint:errcheck // an already-exited group is the expected case, and the reap below handles it.
	_ = cmd.Process.Kill()                              //nolint:errcheck // the same, for the case where the group was never created.
	var status syscall.WaitStatus
	_, _ = syscall.Wait4(cmd.Process.Pid, &status, 0, nil) //nolint:errcheck // there is nothing to do about a child that cannot be reaped, and the process is going away.
}

// Nmbd runs the name service, but only while the promoted configuration asks
// for a name.
//
// Name service is broadcast and does not cross a container bridge, so on a
// bridged network it starts and nobody hears it. Failure is a log line, never
// fatal: the address still mounts.
func (s *Smbd) Nmbd(want bool) {
	up := alive(s.nmbd)
	if want == up {
		return
	}
	if !want {
		kill(s.nmbd)
		s.nmbd = nil
		return
	}
	cmd := exec.Command("nmbd", "--foreground")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		s.log.Warn("the name service did not start, so a client cannot resolve the server by name; the address still works", "error", err)
		return
	}
	s.nmbd = cmd
}

// StartFail2ban starts the ban daemon, when it can do anything.
//
// Its ban action writes firewall rules, which needs a capability the reference
// deployment does not grant once this runs on the host's network: there those
// rules are the host's firewall handed to the process that parses SMB off the
// wire. Checking first turns that into one line saying what is off, instead of
// a jail failing to start with an error that reads like a bug.
func (s *Smbd) StartFail2ban() {
	if !which("fail2ban-server") {
		return
	}
	if !hasNetAdmin() {
		s.log.Info("no permission to change the firewall, so the ban daemon is not started and repeated bad passwords are never banned; what limits an attacker is the admission list plus the required authentication")
		return
	}
	// A plain file rather than the system log, because a container has no log
	// daemon: pointed at one, the ban daemon reports that it could not change
	// its log target and comes up with no jails loaded.
	cmd := exec.Command("fail2ban-server", "-b", "--logtarget=/var/log/fail2ban.log")
	if err := cmd.Run(); err != nil {
		s.log.Warn("the ban daemon did not start; continuing without it", "error", err)
	}
}

func service(unit, action string) (bool, error) {
	var cmd *exec.Cmd
	switch {
	case which("systemctl"):
		cmd = exec.Command("systemctl", action, unit) //nolint:gosec // G204 flags a variable command: both come from this package's own detection, never from a request.
	default:
		cmd = exec.Command("rc-service", unit, action) //nolint:gosec // G204 flags a variable command: the same.
	}
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			// A non-zero exit is an answer, not a failure to ask.
			return false, nil
		}
		return false, fmt.Errorf("asking the service manager to %s %s: %w", action, unit, err)
	}
	return true, nil
}

func require(unit, action string) error {
	ok, err := service(unit, action)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("the service manager could not %s the %s service", action, unit)
	}
	return nil
}

// hasNetAdmin reports whether this process may change the firewall.
//
// Read from the process's own status rather than assumed from being root,
// because a container routinely runs as root with that permission dropped.
func hasNetAdmin() bool {
	const netAdminBit = 12

	body, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(body), "\n") {
		v, ok := strings.CutPrefix(line, "CapEff:")
		if !ok {
			continue
		}
		caps, perr := strconv.ParseUint(strings.TrimSpace(v), 16, 64)
		if perr != nil {
			return false
		}
		return caps&(1<<netAdminBit) != 0
	}
	return false
}
