package smbagent

import (
	"time"
)

// The control channel between the server and this agent.
//
// One request, one response, one connection, over a socket on a directory both
// sides mount. Line-delimited JSON, because the whole vocabulary is "apply what
// you just read, and tell me what happened", and a framed binary format would
// buy nothing an operator cannot already get from a socket tool.
//
// A socket rather than a second watched file: the point of the channel is that
// the answer comes back. Before it existed the server wrote four files and
// learned nothing. A rejected validation, a share path that does not exist
// where the daemon runs, or an import that produced no credential all looked
// identical to success from that side, and only turned up as a client failing
// to connect.
//
// Not authenticated, deliberately. The socket lives on a volume shared by
// exactly the two containers that already exchange password hashes through the
// same directory; anything that can open it can already read those. Filesystem
// permissions are the whole gate.

// DefaultTimeout is how long the server waits for an answer. The agent's work
// is a validation, a file copy and an import, so a slow answer means something
// is stuck rather than something is large.
const DefaultTimeout = 10 * time.Second

// DefaultSocket is where the socket lives unless the settings say otherwise.
//
// Not under the rendered configuration directory: the agent mounts that read
// only, because it holds password hashes and only the server writes it, and a
// listener has to create its own socket.
const DefaultSocket = "/run/sc-smb/agent.sock"

// Request operations.
const (
	// OpApply re-reads the rendered files and applies them now.
	OpApply = "apply"
	// OpStatus repeats the last apply's report without doing another one.
	OpStatus = "status"
)

// Request is one message to the agent.
type Request struct {
	Op string `json:"op"`
}

// SmbdAction is what the agent did to the daemon on this pass.
type SmbdAction string

const (
	// ActionUnchanged means nothing changed that the daemon had to be told
	// about.
	ActionUnchanged SmbdAction = "unchanged"
	// ActionReloaded means the configuration was reread in place. Enough for
	// shares, users and permissions.
	ActionReloaded SmbdAction = "reloaded"
	// ActionRestarted means the process was replaced. The only thing that
	// moves the listening sockets: the daemon binds them once at startup and a
	// reload does not revisit them, so a changed bind line needs this.
	ActionRestarted SmbdAction = "restarted"
	ActionStarted   SmbdAction = "started"
	// ActionStopped means the rendered configuration went away, which is how
	// the setting being turned off reaches this side.
	ActionStopped SmbdAction = "stopped"
	// ActionFailed means the agent wanted to act and could not. The report's
	// error says why.
	ActionFailed SmbdAction = "failed"
)

// Report is one apply, in the agent's own words.
//
// Everything here is something the server could not otherwise know, because it
// is true of the agent's namespace and filesystem rather than of the server's.
type Report struct {
	// OK is false if anything below is a problem an operator has to act on.
	// The configuration may still have been promoted: a share whose path is
	// missing does not stop the other shares from working.
	OK bool `json:"ok"`
	// Shares are the section names the daemon is now serving.
	Shares []string `json:"shares"`
	// Interfaces and HostsAllow are the expanded scope lines, after detection.
	// The server renders these closed and cannot see what they became, which
	// is exactly the pair of values a "why can nothing connect" question
	// needs.
	Interfaces string     `json:"interfaces"`
	HostsAllow string     `json:"hosts_allow"`
	Smbd       SmbdAction `json:"smbd"`
	// MissingPaths are share paths named in the configuration that do not
	// exist in the agent's own filesystem. Validation does not check this, so
	// before it was reported here the symptom was a client being told the
	// network name is invalid while every file on the server's side looked
	// right.
	MissingPaths []string `json:"missing_paths"`
	// MissingPassdb are accounts listed in the roster that the import did not
	// produce a credential for. They cannot authenticate, and every downstream
	// symptom points at credentials instead of at the import.
	MissingPassdb []string `json:"missing_passdb"`
	// Error is why this apply failed, or a warning when OK is false for a
	// reason that did not stop the promotion.
	Error string `json:"error,omitempty"`
}

// FailedReport is an apply that did not happen.
func FailedReport(reason string) Report {
	return Report{Smbd: ActionFailed, Error: reason}
}
