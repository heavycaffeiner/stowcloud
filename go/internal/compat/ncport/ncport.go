//go:build compat_nc

// Package ncport is the seam between the compatibility layer and the core.
//
// It is the one package both sides see, which makes it the one place a
// vocabulary leak would be invisible to the import gates: the layer may not
// import the core, and the core may not import the layer, but both may import
// this. So this package carries no vendor vocabulary at all, and a gate greps
// it to make sure.
//
// The types here are aliases, not mirrors. An alias makes strictness cost no
// conversion code; a mirror is code that exists only to drift, and a mirrored
// set of core types falling out of step with the real ones has cost this
// project a round of debugging before.
package ncport

import (
	"context"

	"github.com/heavycaffeiner/stowcloud/go/internal/acl"
	"github.com/heavycaffeiner/stowcloud/go/internal/core"
	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
)

// The core types the layer works in. Aliases, so a value crossing the seam is
// the same value and not a copy of one.
type (
	Entry    = core.Entry
	Resolved = core.Resolved
	UserID   = core.UserID
	ShareID  = core.ShareID
	Page     = core.Page
	Cursor   = core.Cursor
	Perms    = acl.Perms
	Vpath    = vfs.Vpath
	Stat     = vfs.Stat
)

// The permission bits, re-exported so the layer never imports the acl package
// to name one.
const (
	Read     = acl.Read
	Write    = acl.Write
	Create   = acl.Create
	Delete   = acl.Delete
	Rename   = acl.Rename
	Move     = acl.Move
	Share    = acl.Share
	Download = acl.Download
)

// FS is what the layer may do to files.
//
// An interface rather than the core itself, so the set of operations the layer
// can reach is a list somebody wrote down rather than whatever the core
// happens to export.
type FS interface {
	// Resolve turns a client path into a resolution. Path parsing lives in the
	// core and the layer asks for it: doing it here is the bug the two path
	// types exist to prevent.
	Resolve(user UserID, p string, need Perms) (Resolved, error)
	// List is one page of a directory.
	List(ctx context.Context, r Resolved, cur Cursor) (Page, error)
	// EntryAt is the resolved path itself as an entry.
	EntryAt(r Resolved, st Stat) Entry
	// Stat reports on the resolved path.
	Stat(r Resolved) (Stat, error)
}

// StatePort is the durable storage the layer may reach.
//
// It exists because the import gate forbids the layer from importing the store
// at all. The queries behind it live in the wiring package, which is the only
// one that sees both sides, so the layer states what it needs rather than how
// it is stored.
type StatePort interface {
	// InstanceID is the identity this deployment presents. It is durable
	// because a client that saw one identity and then another treats the
	// server as a different server and re-syncs everything.
	InstanceID(ctx context.Context) (string, error)

	// Favorites are the starred paths for a user, keyed by identity so a
	// star follows the file rather than a path.
	Favorites(ctx context.Context, user UserID) ([]Favorite, error)
	SetFavorite(ctx context.Context, user UserID, f Favorite, on bool) error
}

// UserInfo is an account, as the compat surfaces render one.
type UserInfo struct {
	LoginName   string
	DisplayName string
	Enabled     bool
	Email       *string
	Groups      []string
	Language    string
	Locale      string
}

// Quota is what an account may use and has used.
//
// Total is nil for no per-user cap, which is a different fact from a cap of
// zero and is rendered differently by every client. Free is the storage's real
// free space, which a client compares a file's size against before it will
// start an upload.
type Quota struct {
	Used  uint64
	Free  uint64
	Total *uint64
}

// AccountPort is what the layer needs to answer the account surfaces.
type AccountPort interface {
	UserInfo(ctx context.Context, user UserID) (UserInfo, error)
	Quota(ctx context.Context, user UserID) (Quota, error)
	// UserInfoByLogin resolves another account, applying whatever visibility
	// scope the deployment configured. Outside scope and absent are the same
	// answer, so this reports absence rather than a refusal.
	UserInfoByLogin(ctx context.Context, caller UserID, login string) (UserInfo, bool, error)
}

// SearchPort answers the mobile search surfaces.
type SearchPort interface {
	// Search returns entries matching a term, already permission-filtered.
	Search(ctx context.Context, user UserID, term string, limit int) ([]Entry, error)
	// Recent returns entries modified since a bound, newest first.
	Recent(ctx context.Context, user UserID, sinceNs int64, limit int) ([]Entry, error)
}

// PreviewPort mints the signed URLs the thumbnail and player surfaces hand
// out. Every one is short-lived, read-only and on the content origin, so a URL
// that leaks cannot reach an app-origin route.
type PreviewPort interface {
	SignedThumbURL(ctx context.Context, user UserID, path string, w, h int) (string, bool, error)
	SignedDownloadURL(ctx context.Context, user UserID, path string) (string, bool, error)
}

// Favorite is one starred entry, identified the way everything durable in this
// product is: by the file's identity rather than by a name that can move.
type Favorite struct {
	Share ShareID
	Dev   uint64
	Ino   uint64
	Btime *int64
	Path  string
}
