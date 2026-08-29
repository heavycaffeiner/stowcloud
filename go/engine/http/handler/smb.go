// Linux only, for the same reason as the rest of this package.
//go:build linux

// The file-sharing apply surface's projection.
//
// The answer is the sidecar's own report rather than this server's guess.
// Everything worth knowing about an applied configuration is true in the other
// container: which shares the daemon now serves, what the bind lines expanded
// to, and which accounts the credential import produced nothing for. This
// server rendered files and cannot observe any of it.
package handler

import (
	"github.com/heavycaffeiner/stowcloud/go/engine/service/smb/agent"
)

// SMBReportView is one apply, named for the screen that shows it.
type SMBReportView struct {
	// OK is false whenever something below wants an operator. The
	// configuration may still have been promoted: one share with a missing
	// path does not stop the others from working.
	OK bool `json:"ok"`

	// Shares are the network names the daemon serves now.
	Shares []string `json:"shares"`

	// Interfaces and HostsAllow are the scope lines after the sidecar expanded
	// them. This server renders them closed and cannot see what they became,
	// which is the pair a question about why nothing connects starts from.
	Interfaces string `json:"interfaces"`
	HostsAllow string `json:"hosts_allow"`

	// Action is what happened to the daemon: unchanged, reloaded, restarted,
	// started, stopped or failed.
	Action string `json:"action"`

	// MissingPaths are share paths absent in the daemon's filesystem. Nothing
	// on this side can see that, and without it the symptom is a client told
	// the network name is invalid while every file here looks right.
	MissingPaths []string `json:"missing_paths"`

	// MissingCredentials are accounts the import produced nothing for. They
	// cannot authenticate, and every downstream symptom points at the password
	// rather than at the import.
	MissingCredentials []string `json:"missing_credentials"`

	// Message carries the agent's own explanation, present when it gave one.
	Message string `json:"message,omitempty"`
}

// SMBReportOf projects one report.
//
// The three lists are empty rather than null, so a client renders a list in
// every case instead of testing each field before iterating it.
func SMBReportOf(r agent.Report) SMBReportView {
	out := SMBReportView{
		OK:                 r.OK,
		Shares:             r.Shares,
		Interfaces:         r.Interfaces,
		HostsAllow:         r.HostsAllow,
		Action:             string(r.Smbd),
		MissingPaths:       r.MissingPaths,
		MissingCredentials: r.MissingPassdb,
		Message:            r.Error,
	}
	if out.Shares == nil {
		out.Shares = []string{}
	}
	if out.MissingPaths == nil {
		out.MissingPaths = []string{}
	}
	if out.MissingCredentials == nil {
		out.MissingCredentials = []string{}
	}
	return out
}
