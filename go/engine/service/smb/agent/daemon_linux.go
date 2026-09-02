//go:build linux

package agent

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// Process lifecycle control for the daemon: the Daemon interface's
// implementation.
//
// This agent runs as the daemon's parent and manages it directly: spawned as a
// child, signalled here, replaced here. There is no service manager to delegate
// to, because the only deployment is the container this agent ships in.

func which(bin string) bool {
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" {
			continue
		}
		if st, err := os.Stat(filepath.Join(dir, bin)); err == nil && !st.IsDir() { //nolint:gosec // G703 reports an unchecked error from a path search: this implements PATH lookup, bin is always a literal in every caller.
			return true
		}
	}
	return false
}

// Smbd manages the daemon and optionally the NetBIOS name service when
// configuration requests it.
type Smbd struct {
	log *slog.Logger
	// The daemon this agent spawned, and the name service beside it.
	child *exec.Cmd
	nmbd  *exec.Cmd
}

// NewSmbd constructs a controller.
func NewSmbd(log *slog.Logger) *Smbd {
	return &Smbd{log: log}
}

// alive checks whether a subprocess remains active, harvesting it if
// terminated.
//
// Harvesting prevents zombies: a crashed daemon would otherwise persist as
// undead until this process terminates, and in containers this agent is
// init, so the zombie would remain for the container's entire lifetime.
func alive(cmd *exec.Cmd) bool {
	if cmd == nil || cmd.Process == nil {
		return false
	}
	var status syscall.WaitStatus
	pid, err := syscall.Wait4(cmd.Process.Pid, &status, syscall.WNOHANG, nil)
	if err != nil {
		return false
	}
	// Pid zero indicates the child remains active, no harvest occurred.
	return pid == 0
}

// Running reports whether the daemon this agent spawned is still alive.
func (s *Smbd) Running() bool {
	return alive(s.child)
}

// Start launches the daemon.
func (s *Smbd) Start() error {
	cmd := exec.Command("smbd", "--foreground", "--no-process-group")
	// Output passes through intentionally: daemon diagnostics appear in
	// container logs.
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	// A dedicated process group ensures that stop signals propagate to
	// per-connection children. Signaling just the parent leaves children alive,
	// and restarting then fails because a child still holds the listening port.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting the daemon: %w", err)
	}
	s.child = cmd
	return nil
}

// Reload refreshes configuration from disk. Fast, but does not rebind listening
// sockets.
func (s *Smbd) Reload() error {
	out, err := exec.Command("smbcontrol", "all", "reload-config").CombinedOutput()
	if err == nil {
		return nil
	}
	return fmt.Errorf("reloading the configuration: %w: %s", err, strings.TrimSpace(string(out)))
}

// Restart replaces the process, which rebinds all listening sockets.
func (s *Smbd) Restart() error {
	if err := s.Stop(); err != nil {
		return err
	}
	return s.Start()
}

// Stop terminates both the daemon and name service.
func (s *Smbd) Stop() error {
	s.Nmbd(false)
	if s.child != nil {
		kill(s.child)
		s.child = nil
	}
	return nil
}

// kill terminates a subprocess and all descendants, then harvests to prevent
// zombie processes.
func kill(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	// Target the entire process group: the daemon spawns a child per active
	// connection, and these children hold the listening socket. A negative pid
	// targets the group instead of a single process.
	//
	// Signaling a terminated process fails benignly; harvest below handles both
	// cases.
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) //nolint:errcheck // already-terminated groups are expected here, harvest below covers both outcomes.
	_ = cmd.Process.Kill()                              //nolint:errcheck // same rationale, for cases where setpgid never executed.
	var status syscall.WaitStatus
	_, _ = syscall.Wait4(cmd.Process.Pid, &status, 0, nil) //nolint:errcheck // harvest failures have no remedy, and the enclosing process is shutting down.
}

// Nmbd controls the NetBIOS name service, active only when promoted
// configuration specifies a name.
//
// Broadcast packets do not traverse container bridges, so on bridged
// networks the service runs but clients cannot hear it. Startup failure
// logs but never aborts, since direct address access still functions.
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

// StartFail2ban launches the intrusion prevention daemon if capabilities
// permit.
//
// Banning requires writing firewall rules, which needs CAP_NET_ADMIN. The
// reference container deployment omits this capability on host networking:
// granting it would expose the host firewall to a process parsing untrusted
// SMB traffic. Pre-checking replaces a confusing jail startup error with a
// single informative log line.
func (s *Smbd) StartFail2ban() {
	if !which("fail2ban-server") {
		return
	}
	if !hasNetAdmin() {
		s.log.Info("no permission to change the firewall, so the ban daemon is not started and repeated bad passwords are never banned; what limits an attacker is the admission list plus the required authentication")
		return
	}
	// Log to a regular file instead of syslog: containers lack a log daemon, and
	// pointing at syslog causes the ban daemon to report target-change failure
	// and skip loading jails.
	cmd := exec.Command("fail2ban-server", "-b", "--logtarget=/var/log/fail2ban.log")
	if err := cmd.Run(); err != nil {
		s.log.Warn("the ban daemon did not start; continuing without it", "error", err)
	}
}

// Testparm validates candidate configuration before promotion.
//
// Path existence is not verified; separate checks handle that during apply.
func Testparm(ctx context.Context, candidate string) error {
	out, err := exec.CommandContext(ctx, "testparm", "-s", filepath.Clean(candidate)).CombinedOutput() //nolint:gosec // G204 objects to a variable path: candidate resides in the agent's controlled state directory.
	if err != nil {
		return fmt.Errorf("the validator rejected the candidate configuration: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// hasNetAdmin checks whether this process holds firewall modification
// capability.
//
// Reads capability bits from process status instead of assuming root grants
// this, since containers commonly run as root with selective capability drops.
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
