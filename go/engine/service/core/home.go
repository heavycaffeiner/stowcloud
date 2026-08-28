//go:build linux

package core

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"strconv"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/state"
)

// Per-user home directories, off by default: an operator turns them on and
// nothing else does.
//
// A home is not a second resolution mechanism. One share root is opened for
// the whole homes tree and every user's home is a subdirectory under it, so a
// home reaches a caller through the same grant-projected virtual root and the
// same single Resolve every other share uses. A home that resolved by a
// different path would be a second place the existence rule and the
// permission check could be got wrong, and the single gate exists precisely
// so there is one such place.

const (
	homeLabel = "Home"
	// homeShareID is reserved; no admin share may take it.
	homeShareID  = 999_999
	templateName = ".template"
)

// homePerms is the full permission set a home grant carries.
const homePerms = acl.Read | acl.Write | acl.Create | acl.Delete |
	acl.Rename | acl.Move | acl.Share | acl.Download

// homeHostMode keeps other local users out of the tree that holds every home.
const homeHostMode = 0o750

// EnableHomes opens host as the shared homes root, creating it if missing,
// and registers it under the reserved home share id.
//
// Unlike an admin share, whose directory is a pre-existing location the
// operator points at, the homes host is entirely managed by this process.
// Creating it is the one directory write the core does outside a share root.
func (c *Core) EnableHomes(ctx context.Context, host string) error {
	if _, ok := c.ShareRoot(homeShareID); ok {
		return errors.New("homes are already enabled")
	}
	if err := ensureHostDir(host); err != nil {
		return err
	}
	return c.RegisterShare(ctx, ShareDef{
		ID:     homeShareID,
		Name:   homeLabel,
		Host:   host,
		Policy: vfs.DefaultSharePolicy(),
	})
}

// ensureHostDir creates the one host directory this package owns outright.
func ensureHostDir(host string) error {
	return os.MkdirAll(host, fs.FileMode(homeHostMode))
}

// ensureHome creates a user's home directory and grant on first access.
//
// Resolve runs it eagerly, because the projected root is built from grants
// and without the hook a home appears only after an access that cannot happen
// until the home is in the root. The call site treats a failure as
// warn-and-continue: the failure domain of home creation (a full disk, a
// broken template) must not take down every other share the user can reach.
func (c *Core) ensureHome(ctx context.Context, user UserID) error {
	root, ok := c.ShareRoot(homeShareID)
	if !ok {
		return nil // homes are disabled
	}
	// The grant is the existence marker: it is already durable, already
	// loaded per user, and already the thing that makes the home visible, so
	// there is no second record to drift from the first.
	if c.userHasHome(user) {
		return nil
	}

	// The check above and the work below race, so the slow path serializes
	// and re-checks under the lock. Only once-per-user creation is
	// serialized, never the steady state.
	c.homeOnce.Lock()
	defer c.homeOnce.Unlock()
	if c.userHasHome(user) {
		return nil
	}

	name := strconv.FormatInt(int64(user), 10)
	subpath, err := vfs.ParseSafePath(name)
	if err != nil {
		return err
	}
	home := Resolved{user: user, share: homeShareID, root: root, path: subpath, perms: homePerms}

	template, err := vfs.ParseSafePath(templateName)
	if err != nil {
		return err
	}
	seeded, err := pathExists(root, template)
	if err != nil {
		return err
	}

	if seeded {
		// An admin-facing feature with no other trace in configuration: an
		// operator drops files into .template and every later first login
		// receives them. The template is unreachable as anybody's own home,
		// because homes are named by numeric user id.
		st, serr := root.Stat(template)
		if serr != nil {
			return mapVFSErr(serr)
		}
		tmpl := Resolved{user: user, share: homeShareID, root: root, path: template, perms: homePerms}
		// No cancellation gate: seeding a home is not a job anybody polls.
		// copyRecursive creates the destination itself and tolerates one that
		// already exists, which is what adopts a crashed earlier attempt.
		if cerr := c.copyRecursive(ctx, tmpl, home, st, nil); cerr != nil {
			return cerr
		}
	} else if merr := root.Mkdir(subpath); merr != nil && !errors.Is(merr, vfs.ErrExists) {
		return mapVFSErr(merr)
	}

	// The directory is created before the grant, so a crash between the two
	// leaves a directory with no grant. The next call finds no grant, re-runs
	// this path, tolerates the existing directory, and persists the grant. A
	// grant is never left pointing at a home that was not created.
	return c.createHomeGrant(ctx, user, name)
}

// userHasHome reports the once-per-user gate: a grant on the homes share
// means the home and its directory already exist.
func (c *Core) userHasHome(user UserID) bool {
	for _, r := range c.acl.Roots(int64(user)) {
		if r.Share == int64(homeShareID) {
			return true
		}
	}
	return false
}

// createHomeGrant persists exactly one grant scoping this user to their own
// home subpath, then reloads the evaluator so it takes effect in the running
// process.
//
// The subpath scoping is the security property. The whole tree is one share,
// so scoping is the only wall between users' homes: a root-scoped grant would
// hand whoever received it every other user's home.
func (c *Core) createHomeGrant(ctx context.Context, user UserID, subpath string) error {
	holder := int64(user)
	if _, err := c.state.PersistGrant(ctx, state.GrantRow{
		User:    &holder,
		Share:   int64(homeShareID),
		Subpath: subpath,
		Allow:   uint16(homePerms),
		Inherit: true,
		Label:   homeLabel,
	}, c.clk.Nanos()); err != nil {
		return err
	}
	return c.ReloadGrants(ctx)
}
