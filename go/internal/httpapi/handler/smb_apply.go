// Linux only: it depends on packages that are Linux only.
//go:build linux

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

// SMBOutcome is what a republish did, for a response that is not about SMB.
//
// It is a separate shape from smbReport because the question is different. The
// apply endpoint is asked "what does the daemon look like now", and answers
// with the whole report. A share save is asked "did my change land", and the
// answer is one of three words plus the detail behind whichever it is.
type SMBOutcome struct {
	// State is "applied", "unreachable" or "warnings". Empty when this
	// deployment has no sidecar, which is not a state worth reporting: a
	// screen that showed "SMB: not configured" after every folder rename
	// would be noise on most deployments.
	State string `json:"state,omitempty"`
	// Socket names where the agent was expected, present only when it could
	// not be reached. It is the difference between "rendered and nothing
	// applied it" and "the agent answered with a failure", which are different
	// things to go and look at.
	Socket string `json:"socket,omitempty"`
	// Report is the agent's own answer, present when it gave one.
	Report *smbReport `json:"report,omitempty"`
}

// The three states a republish ends in.
const (
	// SMBApplied is the daemon now serving what this server rendered.
	SMBApplied = "applied"
	// SMBUnreachable is the configuration written and nobody having applied
	// it. The change is live here and not over there.
	SMBUnreachable = "unreachable"
	// SMBWarnings is an apply that happened and found something an operator
	// has to fix: a share path missing where the daemon runs, or an account
	// the import produced no credential for.
	SMBWarnings = "warnings"
)

// SMBOutcomeOf pairs a state with the agent's report. Exported because the
// wiring builds the sink and this package owns the shape it produces.
func SMBOutcomeOf(state string, report smbagent.Report) SMBOutcome {
	wire := wireReport(report)
	return SMBOutcome{State: state, Report: &wire}
}

// smbChanged republishes and hands back what happened, or the zero outcome on
// a deployment with no sidecar.
func smbChanged(r *http.Request, d Deps) SMBOutcome {
	if d.SMBChanged == nil {
		return SMBOutcome{}
	}
	return d.SMBChanged(r.Context())
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
