//go:build linux

// Accounts and groups, as an administrator sees them.
package handler

import (
	"strconv"

	"github.com/heavycaffeiner/stowcloud/go/engine/service/auth"
)

// UserView is one account.
//
// No credential material of any kind: not the password hash, not the sealed
// file-sharing secret, not whether a particular token exists. What an
// operator needs from this screen is who can sign in and how much room they
// are using.
type UserView struct {
	// ID is decimal, like every other id on this API. A JavaScript number
	// loses exactness past 2^53, and an id that round-trips wrong names a
	// different account.
	ID    string `json:"id"`
	Login string `json:"login"`

	Display string `json:"display,omitempty"`

	Admin    bool `json:"admin"`
	Disabled bool `json:"disabled"`

	// TOTP and SMB report which facilities the account has turned on. Whether
	// a second factor is enrolled is not a secret from an administrator, and
	// it is what tells them why somebody is being asked for a code.
	TOTP bool `json:"totp"`
	SMB  bool `json:"smb"`

	CreatedNs string `json:"created_ns"`

	// Quota is absent when the account has no limit, which is different from
	// a limit of zero: one means unrestricted and the other means nothing may
	// be written.
	Quota string `json:"quota_bytes,omitempty"`
	Usage string `json:"usage_bytes"`
}

// UserOf projects one account.
func UserOf(r auth.UserRow) UserView {
	v := UserView{
		ID:        strconv.FormatInt(r.ID, 10),
		Login:     r.Name,
		Display:   r.Display,
		Admin:     r.IsAdmin,
		Disabled:  r.Disabled,
		TOTP:      r.TOTPEnabled,
		SMB:       r.SMBEnabled,
		CreatedNs: strconv.FormatInt(r.CreatedNs, 10),
		Usage:     strconv.FormatUint(r.UsageBytes, 10),
	}
	if r.QuotaBytes != nil {
		v.Quota = strconv.FormatInt(*r.QuotaBytes, 10)
	}
	return v
}

// UsersOf projects a listing.
//
// Never nil: an empty result encodes as an empty array, because a client
// iterating a null gets a runtime error rather than zero rows.
func UsersOf(rows []auth.UserRow) []UserView {
	out := make([]UserView, 0, len(rows))
	for _, r := range rows {
		out = append(out, UserOf(r))
	}
	return out
}

// GroupView is one group and who is in it.
type GroupView struct {
	ID   string `json:"id"`
	Name string `json:"name"`

	// Members are account ids as decimal strings, for the same reason every
	// other id on this API is.
	Members []string `json:"members"`
}

// GroupOf projects one group.
func GroupOf(r auth.GroupRow) GroupView {
	members := make([]string, 0, len(r.Members))
	for _, m := range r.Members {
		members = append(members, strconv.FormatInt(m, 10))
	}
	return GroupView{
		ID:      strconv.FormatInt(r.ID, 10),
		Name:    r.Name,
		Members: members,
	}
}

// GroupsOf projects a listing.
func GroupsOf(rows []auth.GroupRow) []GroupView {
	out := make([]GroupView, 0, len(rows))
	for _, r := range rows {
		out = append(out, GroupOf(r))
	}
	return out
}

// SMBStateView is what an account can do over the file-sharing protocol.
//
// No credential and no hash. What the screen needs is whether the protocol
// works for this account and, when it does not, which of the three reasons
// applies: the person opted out, a second factor blocks it, or no credential
// has been set.
type SMBStateView struct {
	// OptOut is the account's own refusal, which forces Enabled off.
	OptOut  bool `json:"opt_out"`
	Enabled bool `json:"enabled"`

	// Credential is "account" when something works and "none" when nothing
	// does.
	Credential string `json:"credential"`

	// Reason carries a value only where Credential is none. Absent otherwise,
	// because a reason alongside a working credential reads as a warning
	// about something that is fine.
	Reason string `json:"reason,omitempty"`
}

// SMBStateOf projects the state.
func SMBStateOf(s auth.SMBState) SMBStateView {
	return SMBStateView{
		OptOut:     s.OptOut,
		Enabled:    s.Enabled,
		Credential: string(s.Credential),
		Reason:     string(s.Reason),
	}
}

// SMBClearedView answers a credential removal.
type SMBClearedView struct {
	State SMBStateView `json:"state"`

	// Revertible says the account can restore protocol access by setting a
	// credential again. False means clearing it was losing that access for
	// good under the current configuration, which is a different thing to
	// have just done and worth saying.
	Revertible bool `json:"revertible"`
}

// SMBClearedOf projects a removal.
func SMBClearedOf(s auth.SMBState, revertible bool) SMBClearedView {
	return SMBClearedView{State: SMBStateOf(s), Revertible: revertible}
}
