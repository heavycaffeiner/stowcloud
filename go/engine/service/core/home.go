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

// Per-account home directories, disabled unless an operator enables them.
// Nothing else can.
//
// Homes introduce no second resolution mechanism. A single share root covers the
// entire homes tree with each account's home as a subdirectory beneath it, so a
// home reaches callers through the same grant-projected virtual root and the
// same single Resolve every other share uses. Resolving homes by a separate path
// would create a second place to get the existence rule and the permission check
// wrong, and the single gate exists precisely to keep that count at one.

const (
	homeLabel = "Home"
	// homeShareID is reserved; no admin share may take it.
	homeShareID  = 999_999
	templateName = ".template"
)

// homePerms enumerates every permission a home grant confers.
const homePerms = acl.Read | acl.Write | acl.Create | acl.Delete |
	acl.Rename | acl.Move | acl.Share | acl.Download

// homeHostMode keeps other local users out of the tree that holds every home.
const homeHostMode = 0o750

// EnableHomes opens host as the shared homes root, creating it when absent, and
// registers it under the reserved home share id.
//
// An admin share points at a directory the operator already established, whereas
// the homes host is managed entirely by this process. Creating it is the sole
// directory write the core performs outside a share root.
//
// Idempotent, and re-pointable. Boot calls it for a deployment that already had
// homes on, and a settings save calls it again when an operator turns them on
// or moves the root. Re-registering is what the share registry is built for:
// the entry is replaced, and every later Resolve reads the new root. Homes
// created under a previous root keep their grants and stop resolving, which is
// the same thing that happens to an admin share whose host moves.
func (c *Core) EnableHomes(ctx context.Context, host string) error {
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

// DisableHomes withdraws the homes share.
//
// The grants stay. They name a share that no longer resolves, so nobody reaches
// a home through them, and turning homes back on restores exactly what each
// account had rather than handing everybody a fresh empty directory.
func (c *Core) DisableHomes() {
	c.UnregisterShare(homeShareID)
}

// ensureHostDir creates the one host directory this package owns outright.
func ensureHostDir(host string) error {
	return os.MkdirAll(host, fs.FileMode(homeHostMode))
}

// AttachHomeNames supplies the login-name lookup that names home directories.
//
// One-shot, and for the same reason the quota sink is: the names already on
// disk were chosen by whatever was attached when each home was created, so
// swapping the source at runtime would leave a tree half-named by one rule and
// half by another.
func (c *Core) AttachHomeNames(fn func(context.Context, int64) (string, error)) error {
	if c.homeName != nil {
		return errors.New("a home-name source is already attached")
	}
	c.homeName = fn
	return nil
}

// homeDirName is what an account's home is called on disk.
//
// The login name, because an operator administering the tree from a shell or
// over SMB has to be able to tell whose directory is whose, and a numeric id
// tells them nothing. The name rule that guards account creation is already
// stricter than any filesystem needs: lower-case letters, digits, underscore
// and hyphen, never leading with a hyphen or a dot, at most 32 bytes. So the
// name needs no escaping here, and a name that somehow fails to parse as a
// path component falls back to the id rather than failing the home.
func (c *Core) homeDirName(ctx context.Context, user UserID) string {
	fallback := strconv.FormatInt(int64(user), 10)
	if c.homeName == nil {
		return fallback
	}
	name, err := c.homeName(ctx, int64(user))
	if err != nil || name == "" {
		return fallback
	}
	if _, perr := vfs.ParseSafePath(name); perr != nil {
		return fallback
	}
	return name
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

	name := c.homeDirName(ctx, user)
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
		// because an account name cannot begin with a dot.
		st, serr := root.Stat(template)
		if serr != nil {
			return mapVFSErr(serr)
		}
		tmpl := Resolved{user: user, share: homeShareID, root: root, path: template, perms: homePerms}
		// No cancellation gate, since seeding a home is not a job anyone polls.
		// copyRecursive creates the destination and accepts a pre-existing one,
		// which is how it takes over from an attempt that crashed earlier.
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

// createHomeGrant writes a single grant confining this account to its own home
// subpath, then reloads the evaluator so the change applies in the running
// process.
//
// Subpath scoping carries the security property. Since the whole tree forms one
// share, scoping is the only barrier between accounts' homes: a grant scoped to
// the root would give its holder every other account's home.
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
