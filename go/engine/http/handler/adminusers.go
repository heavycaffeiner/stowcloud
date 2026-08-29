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
