//go:build linux

package core

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"os"
	"strconv"

	"github.com/heavycaffeiner/stowcloud/go/internal/acl"
	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
)

// Per-user home directories, off by default (principle 5: an operator turns
// them on, nothing else).
//
// A home is not a second resolution mechanism. One share root is opened for
// the whole homes tree and every user's home is a subdirectory under it, so a
// home reaches a caller through the same grant-projected virtual root and the
// same single Resolve every other share uses. A home that resolved by a
// different path would be a second place the existence rule could be got
// wrong.
//
// One thing is not downstream of that: a new home is seeded from
// {homes.root}/.template when it exists, by recursive copy. That is an
// admin-facing feature with no other trace in configuration, and an
// empty-mkdir-only implementation loses it silently. The template directory is
// unreachable as anybody's own home, because homes are named by numeric user
// id and .template is not a number.

const (
	homeLabel    = "Home"
	homeShareID  = 999_999
	templateName = ".template"
)

// EnableHomes opens host as the shared homes root, creating it if missing,
// and registers it under the reserved home share id. Unlike an admin share the
// directory is entirely managed by this process, not a pre-existing location.
func (c *Core) EnableHomes(ctx context.Context, host string) error {
	if _, ok := c.ShareRoot(homeShareID); ok {
		return errors.New("homes are already enabled")
	}
	if err := ensureDir(host); err != nil {
		return err
	}
	return c.RegisterShare(ctx, ShareDef{
		ID:     homeShareID,
		Name:   homeLabel,
		Host:   host,
		Policy: vfs.DefaultSharePolicy(),
	})
}

// ensureHome creates a user's home directory and grant on first access. The
// caller (resolve) runs it eagerly and treats a failure as a warn-and-continue:
// a home hiccup must not break access to the user's other shares.
func (c *Core) ensureHome(ctx context.Context, user UserID) error {
	if _, ok := c.ShareRoot(homeShareID); !ok {
		return nil // homes disabled
	}
	if c.userHasHome(user) {
		return nil
	}
	// The two checks below race; this serializes the once-per-user slow path.
	c.homeOnce.Lock()
	defer c.homeOnce.Unlock()
	if c.userHasHome(user) {
		return nil
	}

	subpath, err := vfs.ParseSafePath(strconv.FormatInt(int64(user), 10))
	if err != nil {
		return err
	}
	root, _ := c.ShareRoot(homeShareID)
	home := Resolved{user: user, share: homeShareID, root: root, path: subpath, perms: homePerms}

	template, terr := vfs.ParseSafePath(templateName)
	if terr != nil {
		return terr
	}
	hasTemplate, pterr := pathExists(root, template)
	if pterr != nil {
		return pterr
	}
	if hasTemplate {
		// copyRecursive creates subpath itself if missing and copies the
		// template tree into it, which is the admin-facing seeding feature.
		st, serr := root.Stat(template)
		if serr != nil {
			return mapVFSErr(serr)
		}
		tmpl := Resolved{user: user, share: homeShareID, root: root, path: template, perms: homePerms}
		if err := c.copyRecursive(ctx, tmpl, home, st); err != nil {
			return err
		}
	} else {
		if err := root.Mkdir(subpath); err != nil {
			if !errors.Is(err, vfs.ErrExists) {
				return mapVFSErr(err)
			}
			// An existing directory is a race won elsewhere, and its existing
			// is all that mattered.
		}
	}

	return c.createHomeGrant(user)
}

// userHasHome is the once-per-user gate: a grant on the homes share means the
// home (and its directory) already exists.
func (c *Core) userHasHome(user UserID) bool {
	for _, r := range c.acl.Roots(int64(user)) {
		if int64(homeShareID) == r.Share {
			return true
		}
	}
	return false
}

// createHomeGrant persists exactly one grant scoping this user to their own
// home subpath with full permissions under the "Home" label. A root grant on
// the whole homes tree would hand whoever received it every other user's home.
func (c *Core) createHomeGrant(user UserID) error {
	g := acl.Grant{
		User:    int64(user),
		Share:   int64(homeShareID),
		Subpath: aclPathFromString(strconv.FormatInt(int64(user), 10)),
		Allow:   homePerms,
		Inherit: true,
		Label:   homeLabel,
	}
	err := c.state.Write(context.Background(), func(tx *sql.Tx) error {
		_, ierr := tx.ExecContext(context.Background(), grantInsertStmt,
			g.User, nil, g.Share, g.Subpath.String(),
			int64(g.Allow), int64(g.Deny), inheritInt(g.Inherit), g.Label, c.clk.Nanos())
		return ierr
	})
	if err != nil {
		return err
	}
	return c.acl.LoadFromState(context.Background(), c.state.SQL())
}

// inheritInt maps the grant's inherit flag to the stored integer.
func inheritInt(v bool) int64 {
	if v {
		return 1
	}
	return 0
}

// grantInsertStmt is the only way a grant row is created in this package.
const grantInsertStmt = `
INSERT INTO "grant"(user, "group", share, subpath, allow, deny, inherit, label, created_ns)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`

// homePerms is the full permission set a home grant carries.
const homePerms = acl.Read | acl.Write | acl.Create | acl.Delete |
	acl.Rename | acl.Move | acl.Share | acl.Download

// aclPathFromString builds the ACL spelling of a share-relative path.
func aclPathFromString(s string) acl.Path {
	return acl.ParsePath(s)
}

// ensureDir creates a host directory the process manages. This package only
// does it for the homes root, which is the one directory it owns outright.
func ensureDir(host string) error {
	return osMkdirAll(host, 0o750)
}

// osMkdirAll exists as a single seam so the gate's stateless check has one
// place to look; nothing in this package forgets the mode.
func osMkdirAll(host string, mode uint32) error {
	// os.MkdirAll through a helper keeps the write out of the domain files.
	return os.MkdirAll(host, fs.FileMode(mode))
}
