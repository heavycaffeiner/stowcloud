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
// It holds the settings it was built with rather than reading them per call.
// The section requires a restart, so a value that changed since construction
// is not yet in effect anywhere, and reading it here would apply half of it.
type smbPublisher struct {
	engine *Engine
	cfg    smb.Config

	configDir string
	socket    string
	gid       uint32

	// mu serializes pushes. Two at once would render the same directory from
	// two reads of the database, and whichever finished last would win
	// regardless of which read the newer state.
	mu sync.Mutex
}

// smbSettings is what construction needs out of the settings document.
type smbSettings struct {
	Config    smb.Config
	ConfigDir string
	Socket    string
	GID       uint32
}

// smbSettingsOf reads the stored document for the file-sharing section.
//
// Read here rather than taken from loadSettings because this half is decided
// once, at construction, while that function runs again on every save. The
// section requires a restart for exactly this reason: the credential path and
// the publisher are assembled from it and neither can be replaced under a
// running server.
//
// A document that cannot be read leaves the protocol off. Off is the direction
// that refuses rather than admits, and a deployment that never configured a
// sidecar looks the same as one whose settings row would not load.
func smbSettingsOf(ctx context.Context, e *Engine) smbSettings {
	values := runtimecfg.Load(ctx, e.State, runtimecfg.Defaults(), e.logger)
	return smbSettings{
		Config:    values.SMB,
		ConfigDir: values.SMBConfigDir,
		Socket:    values.SMBSocket,
		GID:       values.SMBServiceGID,
	}
}

// newSMBPublisher builds the publisher, or nil when this deployment has no
// sidecar.
//
// Nil is a supported state and the ordinary one: most deployments serve files
// over the web alone. It is distinct from a publisher that fails, which is a
// configured sidecar that could not be reached.
func newSMBPublisher(e *Engine, s smbSettings) *smbPublisher {
	if !s.Config.Enabled || s.ConfigDir == "" {
		return nil
	}
	return &smbPublisher{
		engine:    e,
		cfg:       s.Config,
		configDir: s.ConfigDir,
		socket:    s.Socket,
		gid:       s.GID,
	}
}

// Publish renders the whole set from current state and asks the agent to apply
// it.
//
// Rebuilt whole rather than diffed. A change that stopped at one surface is
// then still corrected by the next publish, whatever caused that publish.
func (p *smbPublisher) Publish(ctx context.Context) (agent.Report, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	return publish.Publish(ctx, publish.Deps{
		Shares:     func() []publish.Share { return publishShares(p.engine.Core.Shares()) },
		Accounts:   p.engine.Auth,
		Grants:     func(c context.Context) ([]publish.Grant, error) { return publishGrants(c, p.engine.State) },
		Names:      p.engine.Auth.NameOf,
		ConfigDir:  p.configDir,
		Socket:     p.socket,
		ServiceGID: p.gid,
		Log:        p.engine.logger,
	}, p.cfg)
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
		p.engine.logger.Warn("a change did not reach the SMB sidecar",
			"error", err, "socket", p.socket)
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
func publishShares(defs []core.ShareDef) []publish.Share {
	out := make([]publish.Share, 0, len(defs))
	for _, d := range defs {
		if d.BrokenReason != "" {
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
	return grantsOf(rows), nil
}

// grantsOf is the mapping itself, separated from the read so the collapse can
// be checked without a database behind it.
func grantsOf(rows []state.GrantRow) []publish.Grant {
	out := make([]publish.Grant, 0, len(rows))
	for _, r := range rows {
		var user int64
		if r.User != nil {
			user = *r.User
		}
		allow, deny := acl.Perms(r.Allow), acl.Perms(r.Deny)
		out = append(out, publish.Grant{
			User:       user,
			Share:      r.Share,
			WholeShare: r.Subpath == "",
			// Reading over this protocol delivers the bytes, so a grant that
			// admits looking without downloading admits neither here.
			AllowRead:  allow.Has(acl.Read | acl.Download),
			AllowWrite: allow.Intersects(acl.Write | acl.Create),
			Denies:     !deny.IsEmpty(),
		})
	}
	return out
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
	if e.smb == nil {
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

	report, err := e.smb.Publish(ctx)
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
// Empty is what stops every credential change writing a file nobody reads. It
// is also what makes the seam's nil check meaningful rather than decorative.
func passdbPathOf(s smbSettings) string {
	if !s.Config.Enabled || s.ConfigDir == "" {
		return ""
	}
	return filepath.Join(s.ConfigDir, passdbFile)
}
