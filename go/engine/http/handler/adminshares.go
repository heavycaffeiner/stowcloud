//go:build linux

// Shares and grants, as an administrator sees them.
package handler

import (
	"strconv"

	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
)

// ShareView is one registered share.
//
// There is no host path here, and adding one would be a mistake rather than a
// feature: where a share lives on the server's disk is configuration, and a
// client that learns it learns the layout of the machine. The absence is the
// enforcement, since a field that does not exist cannot be filled in by
// accident.
type ShareView struct {
	ID   string `json:"id"`
	Name string `json:"name"`

	// No host path appears here, deliberately. Where a share lives on the
	// server's disk is configuration, and a client that learns it learns the
	// layout of the machine: the first thing worth knowing to anyone trying to
	// reach past the shares they were given. An administrator who needs to
	// change it sends a new one rather than editing what was echoed back.

	// Trash says whether a delete in this share is undoable, which is what a
	// confirmation dialogue needs in order to tell the truth.
	Trash bool `json:"trash"`

	// SharedExternally marks a share another service also reads, so a screen
	// can warn that changes here are visible elsewhere.
	SharedExternally bool `json:"shared_externally,omitempty"`

	// Broken carries why the share is unservable, empty when it is fine. A
	// share whose disk never came back stays registered and stays listed:
	// dropping it made an unreachable share look exactly like a deleted one.
	Broken string `json:"broken,omitempty"`
}

// ShareOf projects one share.
func ShareOf(s core.Share) ShareView {
	return ShareView{
		ID:               strconv.FormatInt(int64(s.ID), 10),
		Name:             s.Name,
		Trash:            s.TrashEnabled,
		SharedExternally: s.SharedExternally,
		Broken:           s.BrokenReason,
	}
}

// SharesOf projects a listing.
func SharesOf(shares []core.ShareDef) []ShareView {
	out := make([]ShareView, 0, len(shares))
	for _, s := range shares {
		out = append(out, ShareOf(s))
	}
	return out
}

// GrantView is one permission assignment.
type GrantView struct {
	ID string `json:"id"`

	// Exactly one of these is set, naming who the grant is for.
	User  string `json:"user,omitempty"`
	Group string `json:"group,omitempty"`

	// Principal is the same fact in the shape the admin screen's filters
	// read: a kind beside the id, so the screen can tell a user grant from a
	// group grant without a second lookup.
	Principal *GrantPrincipalView `json:"principal,omitempty"`

	Share   string `json:"share"`
	Subpath string `json:"subpath,omitempty"`

	// Allow and Deny are permission names rather than a bitmask. A number
	// would make every client reimplement the mapping, and a client that got
	// it wrong would display permissions nobody granted.
	Allow []string `json:"allow"`
	Deny  []string `json:"deny"`

	Inherit   bool   `json:"inherit"`
	Label     string `json:"label,omitempty"`
	CreatedNs string `json:"created_ns"`
}

// GrantOf projects one grant.
func GrantOf(g core.Grant) GrantView {
	v := GrantView{
		ID:        strconv.FormatInt(g.ID, 10),
		Share:     strconv.FormatInt(g.Share, 10),
		Subpath:   g.Subpath,
		Allow:     permNames(acl.Perms(g.Allow)),
		Deny:      permNames(acl.Perms(g.Deny)),
		Inherit:   g.Inherit,
		Label:     g.Label,
		CreatedNs: strconv.FormatInt(g.CreatedNs, 10),
	}
	if g.User != nil {
		v.User = strconv.FormatInt(*g.User, 10)
		v.Principal = &GrantPrincipalView{Kind: "user", ID: *g.User}
	}
	if g.Group != nil {
		v.Group = strconv.FormatInt(*g.Group, 10)
		v.Principal = &GrantPrincipalView{Kind: "group", ID: *g.Group}
	}
	return v
}

// GrantsOf projects a listing.
func GrantsOf(rows []core.Grant) []GrantView {
	out := make([]GrantView, 0, len(rows))
	for _, g := range rows {
		out = append(out, GrantOf(g))
	}
	return out
}

// permNames renders a permission set as the names the API accepts back.
//
// Never nil, so an empty set encodes as an empty array: a client iterating a
// null gets a runtime error rather than zero permissions.
func permNames(p acl.Perms) []string {
	named := acl.NamedPerms()
	out := make([]string, 0, len(named))
	for _, np := range named {
		if p.Has(np.Perm) {
			out = append(out, np.Name)
		}
	}
	return out
}

// ShareUsage is one share's disk accounting.
//
// Total and Free are absent when the filesystem could not be measured, which
// is not the same as a full disk. A share that reported zero free would have
// an operator moving data off a device that is fine.
type ShareUsage struct {
	ID    core.ShareID
	Label string

	Total, Free uint64
	Measured    bool
}

// ShareUsageView is one row of the storage screen.
type ShareUsageView struct {
	Share string `json:"share"`
	Label string `json:"label"`

	// Decimal strings, because a modern array is past 2^53 bytes and a
	// JavaScript number would round the figure an operator is deciding on.
	// Absent when the filesystem could not be measured.
	TotalBytes *string `json:"total_bytes,omitempty"`
	FreeBytes  *string `json:"free_bytes,omitempty"`
}

// StorageView is the whole accounting.
type StorageView struct {
	// DBBytes is what the deployment's own database occupies.
	DBBytes string `json:"db_bytes"`

	// Shares is never nil: a deployment with none encodes as an empty array,
	// because a client iterating a null gets a runtime error.
	Shares []ShareUsageView `json:"shares"`
}

// StorageOf projects the accounting.
func StorageOf(dbBytes int64, shares []ShareUsage) StorageView {
	out := StorageView{
		DBBytes: strconv.FormatInt(dbBytes, 10),
		Shares:  make([]ShareUsageView, 0, len(shares)),
	}
	for _, s := range shares {
		v := ShareUsageView{
			Share: strconv.FormatInt(int64(s.ID), 10),
			Label: s.Label,
		}
		if s.Measured {
			total := strconv.FormatUint(s.Total, 10)
			free := strconv.FormatUint(s.Free, 10)
			v.TotalBytes, v.FreeBytes = &total, &free
		}
		out.Shares = append(out.Shares, v)
	}
	return out
}

// GrantPrincipalView names who a grant is for, in the shape the admin
// screen's filters read.
type GrantPrincipalView struct {
	Kind string `json:"kind"`
	ID   int64  `json:"id"`
}
