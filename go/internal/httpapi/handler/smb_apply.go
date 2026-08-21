package handler

import (
	"net/http"

	"github.com/heavycaffeiner/stowcloud/go/internal/apierr"
	"github.com/heavycaffeiner/stowcloud/go/internal/smbagent"
)

// The SMB apply surface.
//
// It exists because the settings screen could not otherwise tell an
// administrator anything. This server renders the configuration and cannot
// apply it: the daemon runs in another container, in another network
// namespace. Everything worth knowing about a change is true over there, so
// the answer here is the sidecar's own report rather than this server's guess.

// smbReport is the wire shape, which is the sidecar's report with the fields
// named for the screen that shows them.
type smbReport struct {
	OK         bool     `json:"ok"`
	Shares     []string `json:"shares"`
	Interfaces string   `json:"interfaces"`
	HostsAllow string   `json:"hostsAllow"`
	Action     string   `json:"action"`
	// MissingPaths are share paths that do not exist where the daemon runs.
	// Nothing on this side can see that, and the symptom without it is a
	// client being told the network name is invalid while every file here
	// looks right.
	MissingPaths []string `json:"missingPaths"`
	// MissingCredentials are accounts that cannot authenticate over SMB
	// because the import produced nothing for them.
	MissingCredentials []string `json:"missingCredentials"`
	Message            string   `json:"message,omitempty"`
}

func wireReport(r smbagent.Report) smbReport {
	out := smbReport{
		OK:                 r.OK,
		Shares:             r.Shares,
		Interfaces:         r.Interfaces,
		HostsAllow:         r.HostsAllow,
		Action:             string(r.Smbd),
		MissingPaths:       r.MissingPaths,
		MissingCredentials: r.MissingPassdb,
		Message:            r.Error,
	}
	// Empty rather than null, so the screen renders a list either way.
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

// SMBApply answers POST /api/admin/smb/apply.
func SMBApply(d Deps) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		if _, err := requireAdmin(r, d.Auth); err != nil {
			return err
		}
		if d.PublishSMB == nil {
			return &apierr.RequestError{
				Status: http.StatusServiceUnavailable, Code: apierr.CodeSubsystemUnavail,
				Message: "this deployment has no SMB sidecar configured",
				Key:     "smb.not_configured",
			}
		}

		report, err := d.PublishSMB(r.Context())
		if err != nil {
			// The files may well be written: what failed is getting an answer.
			// Saying so beats reporting a success nobody confirmed.
			d.Log.Warn("the SMB agent did not answer an apply", "error", err)
			return &apierr.RequestError{
				Status: http.StatusBadGateway, Code: apierr.CodeSubsystemUnavail,
				Message: "the SMB configuration is written, but the sidecar did not answer",
				Key:     "smb.agent_unreachable",
			}
		}

		// The report is the answer whether or not it is good news. A share
		// path that does not exist is a warning an operator has to see, and it
		// is not a failure of this request.
		return writeJSON(w, http.StatusOK, wireReport(report))
	})
}
