//go:build linux

// Joining the two halves of file-sharing publication.
//
// The server decides what the protocol ought to serve and can apply none of
// it: the daemon runs in another container, reading files from a directory
// both sides mount. So this renders into that directory and asks the sidecar
// to import what it finds.
//
// The wiring lives here because neither half can reach the other. Auth holds
// the sealed hashes and cannot name the file format; the renderer holds the
// format and cannot open a hash; the publisher needs both plus the share
// registry. Assembly is the one place that sees all three.
package lifecycle

import (
	"context"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/apierr"
	"github.com/heavycaffeiner/stowcloud/go/engine/http/handler"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/clock"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/auth"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/settings/runtimecfg"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/smb"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/smb/agent"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/smb/publish"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/state"
)

// passdbFile is the credential file's name inside the mounted directory. The
// publisher names the other three; this one is named here because auth writes
// it directly, on every credential change, without going through a publish.
const passdbFile = "smbpasswd"

// publishTimeout bounds one push. It is the agent's own timeout with room for
// rendering the files on top, so this never cuts off a call the agent is still
// working on.
const publishTimeout = agent.DefaultTimeout + 5*time.Second

// smbPublisher pushes the whole rendered set and is the sink every credential
// change tells.
//
// It reads the settings on every push rather than holding the ones it was
// built with. The file-sharing switch is a value the agent acts on: enabled
// renders the shares and starts the daemon, disabled tears it down and prunes
// the credentials. Holding the construction-time copy meant the switch reached
// the sidecar only at the next start, so turning sharing off left the daemon
// serving until somebody restarted the container.
type smbPublisher struct {
	engine *Engine

	// mu serializes pushes. Two at once would render the same directory from
	// two reads of the database, and whichever finished last would win
	// regardless of which read the newer state.
	mu sync.Mutex
}

// smbSettings is what a push needs out of the settings document.
type smbSettings struct {
	Config     smb.Config
	ConfigDir  string
	Socket     string
	GID        uint32
	Configured bool
}

// smbSettingsOf reads the stored document for the file-sharing section.
//
// A document that cannot be read leaves the protocol off. Off is the direction
// that refuses rather than admits, and a deployment that never configured a
// sidecar looks the same as one whose settings row would not load.
func smbSettingsOf(ctx context.Context, e *Engine) smbSettings {
	values := runtimecfg.Load(ctx, e.State, runtimecfg.Defaults(), e.logger)
	return smbSettings{
		Config:     values.SMB,
		ConfigDir:  values.SMBConfigDir,
		Socket:     values.SMBSocket,
		GID:        values.SMBServiceGID,
		Configured: values.SMBConfigured,
	}
}

// newSMBPublisher builds the publisher, or nil when this deployment has no
// sidecar to talk to at all.
//
// Nil means the settings hold no file-sharing section, which is most
// deployments: they serve files over the web alone and there is nothing on the
// other end of a push. The switch inside that section is not consulted here,
// because it can be flipped while the server runs and a publisher that did not
// exist could not carry the change.
func newSMBPublisher(e *Engine, s smbSettings) *smbPublisher {
	if !s.Configured || s.ConfigDir == "" {
		return nil
	}
	return &smbPublisher{engine: e}
}

// Publish renders the whole set from current state and asks the agent to apply
// it.
//
// Rebuilt whole rather than diffed. A change that stopped at one surface is
// then still corrected by the next publish, whatever caused that publish.
//
// The settings are re-read here, so flipping the file-sharing switch reaches
// the sidecar on the next push: enabled renders the shares and starts the
// daemon, disabled sends the same message the agent reads as teardown, which
// stops it and prunes the credentials.
func (p *smbPublisher) Publish(ctx context.Context) (agent.Report, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	s := smbSettingsOf(ctx, p.engine)

	// Auth writes the credential file on every credential change, so it needs
	// the path this push is using. It was fixed at startup, and a server that
	// booted with sharing off then held an empty one forever: credentials were
	// stored and never written, and the daemon reported every account as
	// having none.
	p.engine.Auth.SetPassdbPath(passdbPathOf(s))

	return publish.Publish(ctx, publish.Deps{
		Shares: func() []publish.Share {
			return publishShares(p.engine.Core.Shares(), p.engine.logger)
		},
		Accounts:   p.engine.Auth,
		Grants:     func(c context.Context) ([]publish.Grant, error) { return publishGrants(c, p.engine.State) },
		Names:      p.engine.Auth.NameOf,
		ConfigDir:  s.ConfigDir,
		Socket:     s.Socket,
		ServiceGID: s.GID,
		Log:        p.engine.logger,
	}, s.Config)
}

// AccessChanged is the sink auth calls once a credential change has committed.
//
// Synchronous, because it is a revocation reaching the other surface and the
// administrator who asked for it is the right person to wait for it. Detached
// from the caller's context, because a browser navigating away must not cancel
// a revocation that is halfway to the sidecar.
//
// It never reports failure upward. The database write has committed and this
// server is already enforcing it, so refusing here would describe a change
// that happened as one that did not. An unreachable sidecar is logged instead.
func (p *smbPublisher) AccessChanged(ctx context.Context) {
	pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), publishTimeout)
	defer cancel()

	report, err := p.Publish(pctx)
	switch {
	case err != nil:
		p.engine.logger.Warn("a change did not reach the SMB sidecar", "error", err)
	case !report.OK:
		// The files were promoted and something in them needs an operator: a
		// share path absent where the daemon runs, or an account the import
		// produced no credential for. Neither is this caller's failure.
		p.engine.logger.Warn("the SMB sidecar applied a change with a warning",
			"error", report.Error)
	}
}

// publishShares adapts the registry's definitions to what rendering reads.
//
// A broken share is left out. Its backing did not open, so rendering it would
// publish a network name whose path does not resolve, which reads to a client
// as a share that exists and refuses.
//
// A non-local share is left out too, and logged rather than silently
// dropped: this format renders a path into a share stanza, and a
// bucket or a container has none. An operator who expected it on the
// network share list needs to see why it is not there rather than
// conclude the publish is broken.
func publishShares(defs []core.ShareDef, logger *slog.Logger) []publish.Share {
	out := make([]publish.Share, 0, len(defs))
	for _, d := range defs {
		if d.BrokenReason != "" {
			continue
		}
		if d.Backend != "" && d.Backend != core.BackendLocal {
			logger.Warn("a share is not published over SMB because it has no local path",
				"share", d.Name, "backend", d.Backend)
			continue
		}
		out = append(out, publish.Share{
			ID:               int64(d.ID),
			Name:             d.Name,
			Path:             d.Host,
			ModeFile:         d.Policy.ModeFile,
			ModeDir:          d.Policy.ModeDir,
			SharedExternally: d.SharedExternally,
		})
	}
	return out
}

// publishGrants adapts the stored grants to the four questions rendering asks
// of one.
//
// The permission bits are collapsed here rather than passed through, so a bit
// this format cannot express cannot reach the renderer at all. Whole-share is
// the empty subpath: the format has no way to state a permission beginning
// partway down a tree.
func publishGrants(ctx context.Context, st *state.DB) ([]publish.Grant, error) {
	rows, err := st.ListGrants(ctx, state.GrantFilter{})
	if err != nil {
		return nil, err
	}
	memberships, err := st.Memberships(ctx)
	if err != nil {
		return nil, err
	}
	return grantsOf(rows, memberships), nil
}

// grantsOf is the mapping itself, separated from the read so the collapse can
// be checked without a database behind it.
func grantsOf(rows []state.GrantRow, memberships ...[]state.MembershipRow) []publish.Grant {
	groupUsers := make(map[int64][]int64)
	if len(memberships) > 0 {
		for _, m := range memberships[0] {
			groupUsers[m.Group] = append(groupUsers[m.Group], m.User)
		}
	}
	out := make([]publish.Grant, 0, len(rows))
	for _, r := range rows {
		allow, deny := acl.Perms(r.Allow), acl.Perms(r.Deny)
		wholeShare := r.Subpath == ""
		allowRead := allow.Has(acl.Read | acl.Download)
		allowWrite := allow.Intersects(acl.Write | acl.Create)
		denies := !deny.IsEmpty()

		if r.User != nil {
			out = append(out, publish.Grant{
				User:       *r.User,
				Share:      r.Share,
				WholeShare: wholeShare,
				AllowRead:  allowRead,
				AllowWrite: allowWrite,
				Denies:     denies,
			})
		}
		if r.Group != nil {
			users := groupUsers[*r.Group]
			if len(users) == 0 {
				out = append(out, publish.Grant{
					User:       0,
					Share:      r.Share,
					WholeShare: wholeShare,
					AllowRead:  allowRead,
					AllowWrite: allowWrite,
					Denies:     denies,
				})
			} else {
				for _, u := range users {
					out = append(out, publish.Grant{
						User:       u,
						Share:      r.Share,
						WholeShare: wholeShare,
						AllowRead:  allowRead,
						AllowWrite: allowWrite,
						Denies:     denies,
					})
				}
			}
		}
	}
	return out
}

// GrantsOf exports grantsOf for package callers and tests.
func GrantsOf(rows []state.GrantRow, memberships ...[]state.MembershipRow) []publish.Grant {
	return grantsOf(rows, memberships...)
}

// publishSMBAtBoot pushes once at startup.
//
// The state can have moved while this server was not running: a migration, an
// edited database, or a change made by a build with no sink. Without this the
// daemon serves whatever it was left with until the next write happens.
//
// A failure degrades rather than stops the boot. File sharing is one surface
// of several, and refusing to start would take the rest of the deployment down
// with it.
func (e *Engine) publishSMBAtBoot(ctx context.Context) {
	if e.smb == nil {
		return
	}
	if _, err := e.smb.Publish(ctx); err != nil {
		e.logger.Warn("the SMB configuration could not be published at startup", "error", err)
	}
}

// smbPublisherOf reads the publisher under the settings lock.
//
// A lock, because a settings save can build one while the server runs: the
// field is no longer written once at startup and read forever after.
func (e *Engine) smbPublisherOf() *smbPublisher {
	e.settingsMu.RLock()
	defer e.settingsMu.RUnlock()
	return e.smb
}

// publishSMBSettings pushes after a settings save.
//
// This is what makes the file-sharing switch live. The agent reads an enabled
// configuration as "render the shares and run the daemon" and a disabled one
// as teardown: stop it and prune the credentials. Before this the switch was
// only read when the process started, so turning sharing off left the daemon
// serving every share until somebody restarted the container.
//
// A failure is logged rather than returned. The document has committed and the
// next push corrects the sidecar, so refusing the save would describe a change
// that happened as one that did not.
func (e *Engine) publishSMBSettings(ctx context.Context) {
	// Built here as well as at boot, because the section can be configured
	// while the server runs. A publisher that only ever existed from startup
	// meant the save that first named a sidecar could not reach it, and the
	// screen asked for a restart to do what the save had already stored.
	e.settingsMu.Lock()
	if e.smb == nil {
		if p := newSMBPublisher(e, smbSettingsOf(ctx, e)); p != nil {
			e.smb = p
			e.Auth.SetAccessChangeSink(p)
		}
	}
	p := e.smb
	e.settingsMu.Unlock()

	if p == nil {
		return
	}
	pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), publishTimeout)
	defer cancel()

	report, err := p.Publish(pctx)
	switch {
	case err != nil:
		e.logger.Warn("a file-sharing settings change did not reach the SMB sidecar", "error", err)
	case !report.OK:
		e.logger.Warn("the SMB sidecar applied the settings change with a warning", "error", report.Error)
	}
}

// smbRenderers are the two seams auth publishes its credential facts through.
//
// They exist because auth must not name this format and the renderer must not
// open a sealed hash. Both adapt one list, so the pair of files they produce
// cannot disagree about which accounts exist or what uid each one carries.
func smbRenderers(clk clock.Clock) (
	passdb func([]auth.SMBCredential) ([]byte, error),
	passwd func([]auth.SMBCredential, uint32) ([]byte, error),
) {
	passdb = func(creds []auth.SMBCredential) ([]byte, error) {
		return smb.PassdbEntries(smbCredentialsOf(creds), clk.Nanos()/int64(time.Second))
	}
	passwd = func(creds []auth.SMBCredential, gid uint32) ([]byte, error) {
		return smb.PasswdEntries(smb.PasswdUsers(smbCredentialsOf(creds)), gid)
	}
	return passdb, passwd
}

// smbCredentialsOf crosses the one seam between the facts and the format.
func smbCredentialsOf(creds []auth.SMBCredential) []smb.Credential {
	out := make([]smb.Credential, 0, len(creds))
	for _, c := range creds {
		out = append(out, smb.Credential{Name: c.Name, Uid: c.UID, NTHash: c.NTHash})
	}
	return out
}

// adminSMBApply re-renders the configuration and asks the sidecar to take it.
//
// The report is the answer whether or not it is good news. A share path that
// does not exist where the daemon runs is something an operator has to see,
// and it is not a failure of this request.
func (e *Engine) adminSMBApply(c *fiber.Ctx) error {
	if _, ok, written := e.admin(c); !ok {
		return written
	}
	p := e.smbPublisherOf()
	if p == nil {
		// Nothing to apply to. Said plainly rather than answered with a
		// success, which would report an apply that never happened.
		return refuse(c, apierr.Classified{
			Class: apierr.SubsystemUnavailable,
			Key:   "smb.not_configured",
		})
	}

	// Detached from the request for the same reason the sink is: an
	// administrator who navigates away must not cancel a push that is already
	// rewriting the daemon's world.
	ctx, cancel := context.WithTimeout(
		context.WithoutCancel(c.UserContext()), publishTimeout)
	defer cancel()

	report, err := p.Publish(ctx)
	if err != nil {
		// The files are written either way. What failed is getting an answer,
		// and saying so beats reporting a success nobody confirmed.
		e.logger.Warn("the SMB agent did not answer an apply", "error", err)
		return refuse(c, apierr.Classified{
			Class: apierr.BadGateway,
			Key:   "smb.agent_unreachable",
		})
	}
	return writeJSON(c, fiber.StatusOK, handler.SMBReportOf(report))
}

// passdbPathOf is where the credential file goes, and empty when this
// deployment publishes none.
//
// The switch is deliberately not consulted. It used to be, and the path was
// then decided once at startup: a server that booted with sharing off held an
// empty path forever, so turning sharing on stored credentials that were never
// written and every account was told it had none. Only a restart fixed it.
//
// The directory is the honest condition. Publishing while the switch is off
// writes a file the agent has already been told to tear down, and the teardown
// removes it.
func passdbPathOf(s smbSettings) string {
	if s.ConfigDir == "" {
		return ""
	}
	return filepath.Join(s.ConfigDir, passdbFile)
}
