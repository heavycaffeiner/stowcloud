// Package agent carries the control channel between the server and the SMB
// agent, and the vocabulary both halves speak.
//
// The server renders configuration and credentials into a shared volume; the
// agent, running beside smbd in its own container, imports them into the
// daemon's world. Without a channel the server writes files and learns
// nothing: a rejected validation, a share path absent where the daemon runs,
// and an import that produced no credential all look identical to success from
// the writing side, and surface only as a client failing to connect.
//
// One request, one answer, one connection. The vocabulary is two words, so the
// encoding is line-delimited JSON rather than a framed binary format that would
// buy nothing an operator with a socket tool cannot already get.
//
// Nothing here is authenticated. The socket sits on a volume shared by exactly
// the two containers that already exchange password hashes through the same
// directory, so anything able to open it can already read those. Filesystem
// permissions are the entire gate.
package agent

import (
	"log/slog"
	"time"
)

// DefaultTimeout bounds how long the server waits for an answer. The agent
// validates a file, copies it and runs an import, so a slow reply indicates
// something stuck rather than something large.
const DefaultTimeout = 10 * time.Second

// DefaultSocket is the socket's location unless the settings name another.
//
// It sits outside the rendered configuration directory. That directory holds
// password hashes and only the server writes it, so the agent mounts it read
// only, and a listener has to create its own socket.
const DefaultSocket = "/run/sc-smb/agent.sock"

// The operations a request may name.
const (
	// OpApply re-reads the rendered files and applies them immediately.
	OpApply = "apply"
	// OpStatus repeats the previous apply's report without performing another.
	OpStatus = "status"
)

// Request is a single message to the agent.
type Request struct {
	Op string `json:"op"`
}

// SmbdAction records what the agent did to the daemon during a pass.
type SmbdAction string

const (
	// ActionUnchanged means nothing changed that the daemon needed to hear
	// about.
	ActionUnchanged SmbdAction = "unchanged"

	// ActionReloaded means the configuration was reread in place, which
	// suffices for shares, users and permissions.
	ActionReloaded SmbdAction = "reloaded"

	// ActionRestarted means the process was replaced. Only a restart moves the
	// listening sockets: the daemon binds them once at startup and a reload
	// never revisits them, so a changed bind line requires this.
	ActionRestarted SmbdAction = "restarted"

	// ActionStarted means the daemon was not running and now is.
	ActionStarted SmbdAction = "started"

	// ActionStopped means the rendered configuration disappeared, which is how
	// the setting being switched off reaches this side.
	ActionStopped SmbdAction = "stopped"

	// ActionFailed means the agent intended to act and could not. The report's
	// error explains why.
	ActionFailed SmbdAction = "failed"
)

// Report describes one apply in the agent's own terms.
//
// Every field holds something the server could not otherwise determine, being
// true of the agent's namespace and filesystem rather than the server's.
type Report struct {
	// OK is false whenever anything below requires an operator's attention.
	// The configuration may still have been promoted, since a share with a
	// missing path does not stop the remaining shares from working.
	OK bool `json:"ok"`

	// Shares lists the section names the daemon now serves.
	Shares []string `json:"shares"`

	// Interfaces and HostsAllow are the expanded scope lines after detection.
	// The server renders these closed and cannot observe what they became,
	// which is exactly the pair a question about why nothing connects needs.
	Interfaces string     `json:"interfaces"`
	HostsAllow string     `json:"hosts_allow"`
	Smbd       SmbdAction `json:"smbd"`

	// MissingPaths lists share paths named in the configuration that do not
	// exist in the agent's filesystem. Validation does not examine this, and
	// before it was reported the symptom was a client being told the network
	// name is invalid while every file looked correct on the server's side.
	MissingPaths []string `json:"missing_paths"`

	// MissingPassdb lists roster accounts the import produced no credential
	// for. They cannot authenticate, and every downstream symptom points at
	// credentials rather than at the import.
	MissingPassdb []string `json:"missing_passdb"`

	// Error explains why this apply failed, or carries a warning where OK is
	// false for a reason that did not prevent promotion.
	Error string `json:"error,omitempty"`
}

// FailedReport describes an apply that did not take place.
func FailedReport(reason string) Report {
	return Report{Smbd: ActionFailed, Error: reason}
}

// LogReport writes one line per apply, whoever requested it.
//
// The source is what used to be absent. An apply arriving on the control socket
// is the interesting one, being the server reacting to an administrator, and it
// logged nothing at all on success, leaving the next poll finding no work as the
// only evidence in the log.
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
