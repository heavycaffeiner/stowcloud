// Package smbpublish renders the SMB configuration and pushes it to the agent.
//
// The server decides what SMB should serve and can do nothing about it: the
// daemon runs in another container, in another network namespace, as a user
// that may edit the system account file. So this renders four files into a
// directory both sides mount, then asks the agent to apply them and reports
// what the agent says.
//
// The asking is the part that used to be missing. Writing the files and hoping
// meant a rejected configuration, a share path that does not exist where the
// daemon runs, or an import that produced no credential all looked identical
// to success from here, and only turned up as a client failing to connect.
package smbpublish

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/heavycaffeiner/stowcloud/go/internal/acl"
	"github.com/heavycaffeiner/stowcloud/go/internal/core"
	"github.com/heavycaffeiner/stowcloud/go/internal/smb"
	"github.com/heavycaffeiner/stowcloud/go/internal/smbagent"
	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
)

// Deps is what publishing needs. Passed in rather than reached for, so the
// caller decides what a publish sees.
type Deps struct {
	Core *core.Core
	// Auth publishes the two credential files, which this package does not
	// render itself: the hashes are sealed and only that package holds the key.
	Auth Accounts
	// Grants reads the stored grants, which become the per-share account
	// lists. Nil means no share gets a list, which renders every share
	// unreachable rather than open.
	Grants func(ctx context.Context) ([]acl.Grant, error)
	// Names resolves an account id to the name the rendered files use. Nil
	// means no grant can be attributed, so no share gets a list.
	Names func(ctx context.Context, id int64) (string, error)
	// ConfigDir is the directory both containers mount.
	ConfigDir string
	// Socket is where the agent listens, or empty for a deployment with no
	// sidecar, which is a legitimate configuration.
	Socket string
}

// Accounts is the credential half, which lives in the auth package because
// only it can open the sealed hashes.
type Accounts interface {
	PublishPasswdEntries(ctx context.Context, path string, gid uint32) error
	PublishPassdb(ctx context.Context) error
}

// serviceGID is the group every rendered account belongs to. It has to exist
// in the agent's container, which the agent checks and refuses over.
const serviceGID = 1000

// Publish renders the files and asks the agent to apply them.
//
// A render failure leaves the previous files alone: half a configuration is
// worse than a stale one, because the agent would validate and promote it.
func Publish(ctx context.Context, d Deps, cfg smb.Config) (smbagent.Report, error) {
	if !cfg.Enabled {
		return disable(ctx, d)
	}

	shares, err := shareDefs(ctx, d)
	if err != nil {
		return smbagent.Report{}, err
	}

	// Rendered before anything is written, so a configuration the renderer
	// refuses never reaches the directory the agent reads.
	conf, rerr := smb.Render(cfg, shares)
	if rerr != nil {
		return smbagent.Report{}, fmt.Errorf("rendering the SMB configuration: %w", rerr)
	}

	if err := os.MkdirAll(d.ConfigDir, 0o750); err != nil {
		return smbagent.Report{}, fmt.Errorf("the SMB configuration directory: %w", err)
	}
	if err := writeFile(filepath.Join(d.ConfigDir, "smb.conf"), conf, 0o640); err != nil {
		return smbagent.Report{}, err
	}
	if err := writeFile(filepath.Join(d.ConfigDir, "network.policy"), policyFile(cfg), 0o640); err != nil {
		return smbagent.Report{}, err
	}

	// The two credential files carry the same account identifiers, because the
	// import tool matches them through those rather than by name. Publishing
	// one without the other is what makes a login fail as an unknown user with
	// nothing logged anywhere.
	if err := d.Auth.PublishPasswdEntries(ctx, filepath.Join(d.ConfigDir, "passwd"), serviceGID); err != nil {
		return smbagent.Report{}, fmt.Errorf("publishing the SMB account file: %w", err)
	}
	if err := d.Auth.PublishPassdb(ctx); err != nil {
		return smbagent.Report{}, fmt.Errorf("publishing the SMB credentials: %w", err)
	}

	return push(ctx, d)
}

// disable removes the rendered files, which is how the setting going off
// reaches the agent.
//
// Removal rather than an empty configuration: the agent reads absence as the
// off switch and tears down the accounts and their credentials with it.
// Leaving a file behind would keep a revoked credential working.
func disable(ctx context.Context, d Deps) (smbagent.Report, error) {
	var errs []error
	for _, name := range []string{"smb.conf", "smbpasswd", "passwd", "network.policy"} {
		if err := os.Remove(filepath.Join(d.ConfigDir, name)); err != nil && !os.IsNotExist(err) {
			errs = append(errs, fmt.Errorf("removing %s: %w", name, err))
		}
	}
	if len(errs) > 0 {
		return smbagent.Report{}, errors.Join(errs...)
	}
	return push(ctx, d)
}

// push asks the agent to apply what was just written.
func push(ctx context.Context, d Deps) (smbagent.Report, error) {
	if d.Socket == "" {
		// No sidecar configured. The files are written and something else
		// applies them, which is what a bare-metal deployment does.
		return smbagent.Report{OK: true, Smbd: smbagent.ActionUnchanged}, nil
	}

	ctx, cancel := context.WithTimeout(ctx, smbagent.DefaultTimeout)
	defer cancel()

	report, err := smbagent.Apply(ctx, d.Socket)
	if err != nil {
		// The files are written either way, so the poll on the other side
		// still applies them. What is lost is the answer, and the caller is
		// told that rather than being shown a success it did not get.
		return smbagent.Report{}, fmt.Errorf("the configuration is written but the SMB agent did not answer: %w", err)
	}
	return report, nil
}

// shareDefs turns this server's shares and grants into share blocks.
//
// A share nobody has a grant on is left out entirely rather than rendered with
// an empty account list. An empty list in that format means every account, so
// rendering one would publish a share this server considers private.
func shareDefs(ctx context.Context, d Deps) ([]smb.ShareDef, error) {
	// Without any one of these there is nothing to render: no grants means no
	// account may reach a share, no way to name an account means every grant
	// is attributed to nobody, and no share registry means there is nothing to
	// attach a list to. Each answers nothing rather than a share with no
	// account list, which in that format is a share open to everyone.
	if d.Grants == nil || d.Names == nil || d.Core == nil {
		return nil, nil
	}
	grants, err := d.Grants(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading the grants: %w", err)
	}

	// Only whole-share grants become account lists. A grant on a subpath
	// cannot be expressed in that format at all: it has no notion of a
	// permission that begins partway down a tree, so rendering one would grant
	// the whole share.
	type lists struct {
		valid, read, write map[string]bool
	}
	byShare := map[int64]*lists{}

	for _, g := range grants {
		if g.Subpath.Len() > 0 || g.User == 0 {
			continue
		}
		// An account whose name cannot be resolved is skipped rather than
		// rendered with an empty one, which is a name the daemon cannot look
		// up and a grant that silently does nothing.
		name, uerr := d.Names(ctx, g.User)
		if uerr != nil || name == "" {
			continue
		}
		l := byShare[g.Share]
		if l == nil {
			l = &lists{valid: map[string]bool{}, read: map[string]bool{}, write: map[string]bool{}}
			byShare[g.Share] = l
		}
		// A denial is not subtracted here: this format has no way to express
		// one that survives the other lists, so an account with any denial is
		// left off entirely and reaches the share through the web interface,
		// where the evaluator is the authority.
		if g.Deny != 0 {
			continue
		}
		if g.Allow&acl.Read == 0 {
			continue
		}
		l.valid[name] = true
		if g.Allow&acl.Write != 0 {
			l.write[name] = true
		} else {
			l.read[name] = true
		}
	}

	var out []smb.ShareDef
	for _, s := range d.Core.Shares() {
		l := byShare[int64(s.ID)]
		if l == nil || len(l.valid) == 0 {
			continue
		}
		out = append(out, smb.ShareDef{
			Name:             s.Name,
			Path:             s.Host,
			ValidUsers:       sortedKeys(l.valid),
			ReadList:         sortedKeys(l.read),
			WriteList:        sortedKeys(l.write),
			ModeFile:         s.Policy.ModeFile,
			ModeDir:          s.Policy.ModeDir,
			SharedExternally: s.SharedExternally,
		})
	}
	// Sorted, so the same state renders the same file and the agent's
	// unchanged case actually happens.
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// policyFile is the two flags the agent reads to decide the network scope.
//
// A pin means the rendered lines are already the final answer and detection
// must not widen them.
func policyFile(cfg smb.Config) []byte {
	out := make([]byte, 0, 64)
	if cfg.AllowPublicBind {
		out = append(out, "allow_public_bind=1\n"...)
	}
	if len(cfg.Interfaces) > 0 {
		out = append(out, "pinned_interfaces=1\n"...)
	}
	return out
}

// writeFile replaces a file so the sidecar never reads half of it.
//
// The sidecar polls this directory on a timer and promotes what it finds, which
// is exactly the window a plain write leaves open: it would validate and
// promote a truncated configuration.
func writeFile(path string, body []byte, mode uint32) error {
	err := vfs.ReplaceFileDurable(path, mode, func(f *os.File) error {
		_, werr := f.Write(body)
		return werr
	})
	if err != nil {
		return fmt.Errorf("replacing %s: %w", filepath.Base(path), err)
	}
	return nil
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
