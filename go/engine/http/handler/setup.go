// Linux only, for the same reason as the rest of this package.
//go:build linux

// The first-run surface's projection.
//
// Both shapes here are answered to a caller with no credential, because the
// deployment has no account to hold one yet. That governs what they carry: the
// state answers one boolean, and the completion answers what was created and
// what the checks noticed, with nothing about the machine it runs on.
package handler

import (
	"strconv"

	"github.com/heavycaffeiner/stowcloud/go/engine/service/settings/check"
)

// SetupStateView is what an unauthenticated caller may learn about a server
// that has not been set up.
//
// One field. Whether the deployment is configured is already observable to
// anyone who can reach it, and anything more would describe a server to
// somebody who cannot yet sign in to it.
type SetupStateView struct {
	// Required says a first administrator can still be created.
	Required bool `json:"required"`
}

// SetupStateOf reports whether the gate is open.
func SetupStateOf(required bool) SetupStateView {
	return SetupStateView{Required: required}
}

// SetupAccountView identifies the account the first run created.
type SetupAccountView struct {
	// ID is a decimal string. Ids run past what a double represents exactly,
	// and a client that rounded one would name a different account.
	ID   string `json:"id"`
	Name string `json:"name"`
}

// SetupOutcomeView is what a completed first run answers with.
//
// Deliberately not a session. The account exists, and the client then
// authenticates through the one path that issues a credential, so this
// surface never becomes a second way to be signed in.
type SetupOutcomeView struct {
	User SetupAccountView `json:"user"`

	// Warnings are what the configuration checks noticed and did not refuse
	// over. The list matters most when it says the host list just saved does
	// not contain the address the operator is browsing from: legitimate behind
	// a proxy, and a lockout otherwise.
	Warnings []FindingView `json:"warnings"`

	// Share names the first folder where one was asked for and created. Absent
	// when none was asked for, and when the one asked for was refused: the
	// account exists either way, and the interface offers the same operation on
	// a screen built for it.
	Share *ShareView `json:"share,omitempty"`

	// ShareFailed says the folder was asked for and not registered. Separate
	// from the account's own success because the gate has closed by then: an
	// operator told only "created" would not know to look at it.
	ShareFailed bool `json:"share_failed,omitempty"`
}

// SetupOutcomeOf builds the answer for a completed first run.
//
// The id is rendered decimal rather than numeric because ids run past what a
// double holds exactly, and a client that rounded one would name a different
// account.
func SetupOutcomeOf(id int64, name string, findings []check.Finding) SetupOutcomeView {
	return SetupOutcomeView{
		User:     SetupAccountView{ID: strconv.FormatInt(id, 10), Name: name},
		Warnings: FindingsOf(check.Advisory(findings)),
	}
}
