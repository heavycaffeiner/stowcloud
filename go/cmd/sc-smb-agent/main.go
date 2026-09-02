//go:build linux

// Command sc-smb-agent applies what the server renders for the SMB daemon.
//
// It runs beside that daemon, in the network namespace that can see the host's
// devices, as the user that may edit the system account file and the credential
// database. The server cannot do any of that from where it runs, which is the
// whole reason this exists as a separate process.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/clock"
	smbagent "github.com/heavycaffeiner/stowcloud/go/engine/service/smb/agent"
)

// pollInterval is long enough not to spin, short enough that a scope change
// nobody announced is noticed while the operator is still looking at the
// screen. Not the main path: the server pushes.
const pollInterval = 2 * time.Second

func main() {
	os.Exit(run())
}

func run() int {
	configDir := flagEnv("config-dir", "SC_SMB_CONFIG_DIR", "/config/smb", "where the server writes the rendered files")
	socket := flagEnv("socket", "SC_SMB_SOCKET", smbagent.DefaultSocket, "where the server connects to ask for an apply")
	stateDir := flagEnv("state-dir", "SC_SMB_STATE_DIR", "/var/lib/sc-smb-agent", "scratch space for the candidate configuration")
	smbConf := flagEnv("smb-conf", "SC_SMB_CONF", "/etc/samba/smb.conf", "what the daemon reads")
	passdb := flagEnv("passdb", "SC_SMB_PASSDB", "/var/lib/samba/private/passdb.tdb", "the credential database")
	once := flag.Bool("once", false,
		"apply once and exit, printing the report: the operator's apply-now")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel()}))

	// It edits the system account file and the credential database, and binds
	// the SMB port through the daemon. Saying so here beats a permission error
	// three layers down.
	if os.Geteuid() != 0 {
		log.Error("this must run as root: it edits the system account file and the credential database")
		return 1
	}

	paths := smbagent.DefaultPaths()
	paths.ConfigDir, paths.StateDir, paths.SmbConf, paths.Passdb = *configDir, *stateDir, *smbConf, *passdb

	if parent := filepath.Dir(paths.Passdb); parent != "" {
		if err := os.MkdirAll(parent, 0o700); err != nil {
			log.Warn("the credential database directory could not be created", "error", err)
		}
	}
	if err := os.MkdirAll("/var/log/samba", 0o755); err != nil { //nolint:gosec // G301 wants stricter: the ban daemon reads this directory as its own user.
		log.Warn("the log directory could not be created", "error", err)
	}

	agent := smbagent.NewAgent(paths, log, clock.System())

	if *once {
		// A one-shot run has no signal loop to inherit from, and the pass
		// below is the whole process. Background is the honest context for
		// it: nothing outside is in a position to cancel.
		report := agent.Apply(context.Background())
		// The daemon is this process's child, so exiting without stopping it
		// leaves an orphan holding the port the next start cannot then bind.
		if serr := agent.Shutdown(); serr != nil {
			log.Warn("the daemon did not stop", "error", serr)
		}
		body, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			log.Error("the report could not be encoded", "error", err)
			return 1
		}
		if _, err := fmt.Fprintln(os.Stdout, string(body)); err != nil {
			return 1
		}
		if !report.OK {
			return 1
		}
		return 0
	}

	log.Info("starting", "config_dir", paths.ConfigDir)

	// A signal has to leave the loop rather than end the process, or a
	// supervised daemon outlives the agent that owns it and the next start
	// finds the port taken.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	// The first apply is what starts the daemon, so it happens before the
	// socket: an apply-now that arrives during startup would otherwise race it.
	smbagent.LogReport(log, agent.Apply(ctx), "startup")

	// After the first apply, because the log path is the one the candidate
	// pointed the daemon at, and the ban daemon treats a missing log file
	// as a hard configuration error for the whole of itself.
	if f, err := os.OpenFile("/var/log/samba/log.smbd", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640); err == nil { //nolint:gosec // G302 wants stricter: the ban daemon tails this as its own user, and a file it cannot read is a jail that never starts.
		if cerr := f.Close(); cerr != nil {
			log.Warn("the daemon log file could not be closed", "error", cerr)
		}
	}
	agent.StartFail2ban()

	smbagent.ServeInBackground(ctx, *socket, agent, paths.ConfigDir, clock.System(), log)

	poll(ctx, agent, log)
	log.Info("stopping")
	if err := agent.Shutdown(); err != nil {
		log.Warn("the daemon did not stop cleanly", "error", err)
	}
	return 0
}

// poll watches for a change nobody announced.
func poll(ctx context.Context, agent *smbagent.Agent, log *slog.Logger) {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	last := agent.Fingerprint()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		if now := agent.Fingerprint(); now != last {
			last = now
			smbagent.LogReport(log, agent.Apply(ctx), "poll")
			continue
		}

		// A daemon that died takes the whole service with it, and nothing else
		// is watching: this agent is its parent.
		//
		// Guarded on the last apply having wanted it up. Without that, a
		// deployment with SMB turned off (no rendered configuration, so the
		// daemon is deliberately stopped) reads as "it died" on every pass and
		// tears down again every couple of seconds, an import and an account
		// file rewrite included.
		wantedUp := agent.Last().Smbd != smbagent.ActionStopped
		if wantedUp && !agent.SmbdRunning() {
			log.Warn("the daemon is not running; starting it again")
			smbagent.LogReport(log, agent.Apply(ctx), "poll")
		}
	}
}

// flagEnv is a flag whose default comes from the environment, which is how a
// container configures this without a command line.
func flagEnv(name, env, fallback, usage string) *string {
	if v := os.Getenv(env); v != "" {
		fallback = v
	}
	return flag.String(name, fallback, usage)
}

func logLevel() slog.Level {
	switch os.Getenv("SC_SMB_LOG") {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
