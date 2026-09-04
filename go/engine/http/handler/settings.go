// Linux only, for the same reason as the rest of this package.
//go:build linux

// The settings family's projection.
//
// One rule shapes the whole thing: stored and applied are separate facts. A
// save that reached the database and could not take effect on the running
// process has to say both, because reporting a clean application leaves an
// operator believing a change is live when it is not, and the next thing they
// do is act on that belief.
package handler

import (
	"github.com/heavycaffeiner/stowcloud/go/engine/service/settings/catalogue"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/settings/check"
)

// FindingView is one check result.
//
// The key and its arguments rather than a rendered sentence: the client owns
// the wording and the language, and a server-rendered message would be one
// neither the client nor a translator can change.
type FindingView struct {
	Section  string            `json:"section"`
	Field    string            `json:"field,omitempty"`
	Reason   string            `json:"reason"`
	Args     map[string]string `json:"args,omitempty"`
	Blocking bool              `json:"blocking"`
}

// HopView describes how one request reached the server.
//
// Not a setting: it is what an operator needs in front of the trusted-proxy
// field to know what to put there. Behind a published container port every
// request arrives from the bridge gateway, so an untrusted peer means one
// address for every visitor and nothing on the screen says why.
type HopView struct {
	// Peer is the address the transport reported, which is what the trust
	// decision is made against. Empty when the transport gave none.
	Peer string `json:"peer,omitempty"`

	// PeerTrusted reports whether the peer falls inside the trusted list.
	// False here with ForwardedSeen true is the misconfiguration: the headers
	// arrived and were ignored.
	PeerTrusted bool `json:"peer_trusted"`

	// Client is what the chain resolved. Equal to Peer when no forwarding was
	// believed.
	Client string `json:"client"`

	// ForwardedSeen reports whether the request carried a forwarding header at
	// all, believed or not.
	ForwardedSeen bool `json:"forwarded_seen"`
}

// SettingsView is the settings resource: every settable field, plus what the
// server observed about the request that asked for them.
type SettingsView struct {
	Fields []catalogue.Field `json:"fields"`
	Hop    HopView           `json:"hop"`
}

// SettingsOf projects the resource.
func SettingsOf(snap catalogue.Snapshot, hop HopView) SettingsView {
	fields := snap.Fields
	if fields == nil {
		// Never null: the screen iterates this, and a null is a third state it
		// would have to test for before drawing an empty form.
		fields = []catalogue.Field{}
	}
	return SettingsView{Fields: fields, Hop: hop}
}

// ApplyOutcomeView is what a save answers with.
type ApplyOutcomeView struct {
	// Stored says the change reached the database. Applied says the running
	// process took it. They are separate because a save can do the first and
	// fail the second, and an operator told only "saved" would believe the
	// change is live.
	Stored  bool `json:"stored"`
	Applied bool `json:"applied"`

	// RestartRequired says the change needs a restart to take effect. It is
	// not an error and not a failure to apply: it is the third outcome, and
	// folding it into either of the others loses it.
	RestartRequired bool `json:"restart_required"`

	// ActiveUploads and ActiveJobs are what a restart would interrupt,
	// reported so the operator decides rather than the server deciding for
	// them. Present only when a restart is required.
	ActiveUploads *int `json:"active_uploads,omitempty"`
	ActiveJobs    *int `json:"active_jobs,omitempty"`

	Findings []FindingView `json:"findings"`
}

// FindingsOf projects the check results.
//
// An empty list rather than null, so a client iterating the findings does not
// have to test the field first.
func FindingsOf(findings []check.Finding) []FindingView {
	out := make([]FindingView, 0, len(findings))
	for _, f := range findings {
		v := FindingView{
			Section:  f.Section,
			Field:    f.Field,
			Reason:   f.ReasonKey,
			Blocking: f.Blocking,
		}
		if len(f.Args) > 0 {
			v.Args = make(map[string]string, len(f.Args)/2)
			// Pairs of name and value. An odd trailing element is a caller
			// bug and is dropped rather than paired with an empty string,
			// which would render as a substitution nobody wrote.
			for i := 0; i+1 < len(f.Args); i += 2 {
				v.Args[f.Args[i]] = f.Args[i+1]
			}
		}
		out = append(out, v)
	}
	return out
}

// Blocking reports whether any finding refuses the save.
//
// One place decides, because "was this refused" answered in two places is how
// a save is refused by one and reported as stored by the other.
func Blocking(findings []check.Finding) bool {
	for _, f := range findings {
		if f.Blocking {
			return true
		}
	}
	return false
}

// ApplyOutcomeOf builds the response for a save.
//
// A blocking finding means nothing was stored and nothing was applied,
// whatever the caller passed for those: a refused save that reported itself
// stored would be the worst of the three failures this projection exists to
// prevent.
func ApplyOutcomeOf(stored, applied, restartRequired bool, findings []check.Finding) ApplyOutcomeView {
	if Blocking(findings) {
		stored, applied, restartRequired = false, false, false
	}
	// A change that needs a restart has not been applied, by definition. Saying
	// both would report a clean application of something that is not running.
	if restartRequired {
		applied = false
	}
	return ApplyOutcomeView{
		Stored:          stored,
		Applied:         applied,
		RestartRequired: restartRequired,
		Findings:        FindingsOf(findings),
	}
}

// WithActiveWork records what a restart would interrupt.
//
// Reported rather than acted on: whether losing three uploads is acceptable is
// the operator's decision, and a server that decided for them would either
// restart over their objection or refuse one they wanted.
func (v ApplyOutcomeView) WithActiveWork(uploads, jobs int) ApplyOutcomeView {
	if !v.RestartRequired {
		return v
	}
	u, j := uploads, jobs
	v.ActiveUploads = &u
	v.ActiveJobs = &j
	return v
}
