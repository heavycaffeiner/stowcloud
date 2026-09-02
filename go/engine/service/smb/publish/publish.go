// Builds only on Linux, where the core types it reads are openat2 handles
// beneath.
//go:build linux

// Package publish renders the SMB configuration and pushes it to the agent.
//
// The server decides what SMB ought to serve and can enforce none of it
// directly: the daemon runs in another container, in another network namespace,
// as a user permitted to edit the system account file. So this renders files
// into a directory both sides mount, then asks the agent to apply them and
// reports whatever the agent says.
//
// Asking is the part that used to be absent. Writing files and hoping made a
// rejected configuration, a share path missing where the daemon runs, and an
// import that yielded no credential all indistinguishable from success on this
// side, surfacing only as a client that could not connect.
package publish

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"

	"github.com/heavycaffeiner/stowcloud/go/engine/service/smb"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/smb/agent"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/fsatomic"
)

// The rendered file names. They are listed once because publishing writes them
// and disabling removes them, and a name spelled twice is a file one half
// forgets.
const (
	fileConf   = "smb.conf"
	filePolicy = "network.policy"
	filePasswd = "passwd"
	filePassdb = "smbpasswd"
)

// Share is one registered share, in the terms publishing needs.
//
// Declared here rather than imported from the core, for the same reason the
// grant below is: this package sits beside those packages in the tier and must
// not import sideways into them. The wiring adapts the core's own type, so a
// field the renderer never reads cannot arrive here at all.
type Share struct {
	ID               int64
	Name             string
	Path             string
	ModeFile         uint32
	ModeDir          uint32
	SharedExternally bool
}

// Grant is one stored grant, in the terms publishing needs.
//
// Permissions travel as the caller's own bits, and this package tests only two
// questions of them: whether a grant admits reading, and whether it admits
// writing. Anything finer belongs to the evaluator, which is exactly the
// argument the deny rule below rests on.
type Grant struct {
	// User is the account the grant is for. Zero means it is not a user grant,
	// which SMB cannot express.
	User int64
	// Share is the share the grant covers.
	Share int64
	// WholeShare is false for a grant that begins partway down a tree. Such a
	// grant cannot be expressed in this format at all, which has no notion of
	// a permission starting below the root.
	WholeShare bool
	// AllowRead and AllowWrite are what the grant admits.
	AllowRead  bool
	AllowWrite bool
	// Denies reports that the grant carries any deny bit, whatever it covers.
	Denies bool
}

// Deps is what publishing requires, supplied by the caller rather than reached
// for, so the caller determines what a publish can see.
type Deps struct {
	// Shares lists this server's registered shares. A function rather than the
	// core itself, so this package depends on the one answer it needs instead
	// of on everything the core can do.
	Shares func() []Share

	// Accounts publishes the two credential files, which this package does not
	// render: the hashes are sealed and only that package holds the key.
	Accounts Accounts

	// Grants reads the stored grants, which become the per-share account lists.
	// Nil means no share receives a list, rendering every share unreachable
	// rather than open.
	Grants func(ctx context.Context) ([]Grant, error)

	// Names maps an account id onto the name the rendered files carry. Nil
	// leaves every grant unattributable, so no share receives a list.
	Names func(ctx context.Context, id int64) (string, error)

	// ConfigDir names the directory mounted by both containers.
	ConfigDir string

	// Socket locates the agent's listener. An empty value describes a
	// deployment running no sidecar at all, which is supported.
	Socket string

	// Log reports what a successful publish had to leave out. Nil silences
	// that, which is what a test that is not asserting on it wants.
	Log *slog.Logger

	// ServiceGID is the group every rendered account joins. It must exist in
	// the agent's container, which the agent verifies and refuses over.
	//
	// Zero takes this package's default rather than root's group, so a value
	// nobody set cannot become the one group no service account may join.
	ServiceGID uint32
}

// Accounts is the credential half, which lives in the auth package because only
// it can open the sealed hashes.
type Accounts interface {
	PublishPasswdEntries(ctx context.Context, path string, gid uint32) error
	PublishPassdb(ctx context.Context) error
}

// defaultServiceGID is what an unset ServiceGID means. The settings layer holds
// the operator-facing default under its own name; this is the fallback for a
// caller that set nothing, so zero never reaches a rendered file as root's
// group.
const defaultServiceGID = 1000

func (d Deps) gid() uint32 {
	if d.ServiceGID == 0 {
		return defaultServiceGID
	}
	return d.ServiceGID
}

// Publish writes the rendered set and asks the agent to apply it.
//
// Failing to render leaves the previous files in place. A partial configuration
// is worse than an outdated one, since the agent would validate and promote
// it.
func Publish(ctx context.Context, d Deps, cfg smb.Config) (agent.Report, error) {
	if !cfg.Enabled {
		return Disable(ctx, d)
	}

	shares, err := shareDefs(ctx, d)
	if err != nil {
		return agent.Report{}, err
	}

	// Rendering happens ahead of any write, keeping a configuration the renderer
	// rejects out of the directory the agent reads.
	conf, res, rerr := smb.Render(cfg, shares)
	if rerr != nil {
		return agent.Report{}, fmt.Errorf("rendering the SMB configuration: %w", rerr)
	}
	// A name the renderer dropped costs one account its access to one share,
	// which is the per-entry degradation the renderer exists to prefer over
	// refusing everyone. It is reported rather than discarded: silently, the
	// symptom is one person unable to reach a share nobody changed.
	logDropped(d, res)

	if err := os.MkdirAll(d.ConfigDir, 0o750); err != nil {
		return agent.Report{}, fmt.Errorf("the SMB configuration directory: %w", err)
	}
	if err := writeFile(filepath.Join(d.ConfigDir, fileConf), conf, 0o640); err != nil {
		return agent.Report{}, err
	}
	if err := writeFile(filepath.Join(d.ConfigDir, filePolicy), policyFile(cfg), 0o640); err != nil {
		return agent.Report{}, err
	}

	// The two credential files share account identifiers, since the import tool
	// pairs them by identifier rather than by name. Writing one without the
	// other produces a login rejected as an unknown user with no trace in any
	// log.
	if err := d.Accounts.PublishPasswdEntries(ctx, filepath.Join(d.ConfigDir, filePasswd), d.gid()); err != nil {
		return agent.Report{}, fmt.Errorf("publishing the SMB account file: %w", err)
	}
	if err := d.Accounts.PublishPassdb(ctx); err != nil {
		return agent.Report{}, fmt.Errorf("publishing the SMB credentials: %w", err)
	}

	return push(ctx, d)
}

// logDropped reports the names the renderer would not write.
//
// A dropped name is not a failure of the publish, so it does not stop one. It
// is the one thing about a successful render an operator has to be told: the
// account still exists, still has its grant, and simply cannot reach that share
// over this protocol.
func logDropped(d Deps, res smb.Result) {
	if d.Log == nil {
		return
	}
	for _, drop := range res.Dropped {
		d.Log.Warn("an account was left out of a rendered SMB share",
			"share", drop.Share, "field", drop.Field, "name", drop.Name, "reason", drop.Reason)
	}
}

// Disable removes the rendered files, which is how the setting being switched
// off reaches the agent.
//
// Removal rather than an empty configuration: the agent treats absence as the
// off switch and tears down the accounts and their credentials with it. A file
// left behind would keep a revoked credential working.
//
// Every removal is attempted before any failure is reported, and the failures
// are joined. Stopping at the first one would leave the rest of the set in
// place, which is the state this call exists to end.
func Disable(ctx context.Context, d Deps) (agent.Report, error) {
	var errs []error
	for _, name := range []string{fileConf, filePassdb, filePasswd, filePolicy} {
		if err := os.Remove(filepath.Join(d.ConfigDir, name)); err != nil && !os.IsNotExist(err) {
			errs = append(errs, fmt.Errorf("removing %s: %w", name, err))
		}
	}
	if len(errs) > 0 {
		return agent.Report{}, errors.Join(errs...)
	}
	return push(ctx, d)
}

// push requests an apply of the set that was just written.
func push(ctx context.Context, d Deps) (agent.Report, error) {
	if d.Socket == "" {
		// No sidecar named, which is a deployment running without the SMB
		// container. The files are rendered and nothing applies them.
		return agent.Report{OK: true, Smbd: agent.ActionUnchanged}, nil
	}

	ctx, cancel := context.WithTimeout(ctx, agent.DefaultTimeout)
	defer cancel()

	report, err := agent.Apply(ctx, d.Socket)
	if err != nil {
		// The files are written either way, so the poll on the other side still
		// applies them. What is lost is the answer, and the caller is told that
		// rather than shown a success it did not receive.
		return agent.Report{}, fmt.Errorf(
			"the configuration is written but the SMB agent did not answer: %w", err)
	}
	return report, nil
}

// lists are one share's three account lists, built as sets so a name granted
// twice appears once.
type lists struct {
	valid, read, write map[string]bool
}

// shareDefs converts this server's shares and grants into share blocks.
//
// Shares carrying no grant are omitted rather than written with an empty account
// list. Emptiness in this format admits every account, so writing one would
// publish a share this server considers private.
func shareDefs(ctx context.Context, d Deps) ([]smb.ShareDef, error) {
	// Lacking any one of these there is nothing to render: without grants no
	// account may reach a share, without names every grant is attributed to
	// nobody, and without the share list there is nothing to attach a list to.
	// Each answers with nothing rather than a share carrying no account list,
	// which in that format is a share open to everyone.
	if d.Grants == nil || d.Names == nil || d.Shares == nil {
		return nil, nil
	}
	grants, err := d.Grants(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading the grants: %w", err)
	}

	byShare, berr := accountLists(ctx, d, grants)
	if berr != nil {
		return nil, berr
	}

	var out []smb.ShareDef
	for _, s := range d.Shares() {
		l := byShare[s.ID]
		if l == nil || len(l.valid) == 0 {
			continue
		}
		out = append(out, smb.ShareDef{
			Name:             s.Name,
			Path:             s.Path,
			ValidUsers:       sortedKeys(l.valid),
			ReadList:         sortedKeys(l.read),
			WriteList:        sortedKeys(l.write),
			ModeFile:         s.ModeFile,
			ModeDir:          s.ModeDir,
			SharedExternally: s.SharedExternally,
		})
	}
	// Sorted, so identical state renders an identical file and the agent's
	// unchanged case actually occurs.
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// accountLists folds the grants into per-share account lists.
//
// The deny rule is the requirement this function exists to state. A share where
// the user holds any grant carrying a deny bit is dropped from that user's SMB
// view entirely, even where the deny covers nothing they could otherwise reach.
// SMB grants are whole-share and additive only, so the format cannot express a
// denial that survives the other lists. Fine-grained authority belongs to the
// web evaluator, and an SMB render approximating subtree denials would be an
// approximation somebody relies on.
//
// The deny is applied per user and share, after every grant has been read.
// Deciding while iterating would let a deny arriving after an allow leave the
// name already in the list.
func accountLists(ctx context.Context, d Deps, grants []Grant) (map[int64]*lists, error) {
	// Whole-share grants only. A grant on a subpath cannot be expressed in this
	// format at all, which has no notion of a permission beginning partway down
	// a tree, so rendering one would grant the whole share.
	type who struct {
		share int64
		name  string
	}
	denied := map[who]bool{}
	type allowed struct {
		key   who
		write bool
	}
	var admits []allowed

	for _, g := range grants {
		if !g.WholeShare || g.User == 0 {
			continue
		}
		// Accounts whose names do not resolve are skipped rather than written
		// with an empty name, which the daemon cannot look up and which turns
		// the grant into a silent no-op.
		name, uerr := d.Names(ctx, g.User)
		if uerr != nil || name == "" {
			continue
		}
		key := who{share: g.Share, name: name}

		if g.Denies {
			denied[key] = true
			continue
		}
		if !g.AllowRead {
			continue
		}
		admits = append(admits, allowed{key: key, write: g.AllowWrite})
	}

	byShare := map[int64]*lists{}
	for _, a := range admits {
		if denied[a.key] {
			continue
		}
		l := byShare[a.key.share]
		if l == nil {
			l = &lists{valid: map[string]bool{}, read: map[string]bool{}, write: map[string]bool{}}
			byShare[a.key.share] = l
		}
		l.valid[a.key.name] = true
		if a.write {
			l.write[a.key.name] = true
			// A name that arrived read-only earlier is now a writer, and
			// leaving it on both lists renders a contradiction.
			delete(l.read, a.key.name)
			continue
		}
		if !l.write[a.key.name] {
			l.read[a.key.name] = true
		}
	}
	return byShare, nil
}

// policyFile emits the two flags the agent consults when deciding network
// scope.
//
// A pin means the rendered lines are already final and detection must not widen
// them.
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

// writeFile replaces a file so the sidecar never reads part of one.
//
// A timer drives the sidecar's poll of this directory, and it promotes whatever
// it finds. That is exactly the window a plain write opens, letting a truncated
// configuration be validated and promoted.
func writeFile(path string, body []byte, mode uint32) error {
	err := fsatomic.ReplaceFileDurable(path, mode, func(f *os.File) error {
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
