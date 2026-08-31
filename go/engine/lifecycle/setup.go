//go:build linux

// First-run setup: the one window in which a deployment with no accounts can
// be given its first administrator.
//
// Two facts close this window and they are not the same. The token proves a
// request came from whoever started the process; the account count says setup
// is finished. The count is the authority, so a token recovered from a log or
// a backup is worth nothing once an account exists.
//
// The form carries the deployment's first network configuration alongside the
// account, because this is the one moment somebody is certainly looking at the
// screen. A host list saved here is one nobody has to discover later from a
// refused request.
package lifecycle

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/gofiber/fiber/v2"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/apierr"
	"github.com/heavycaffeiner/stowcloud/go/engine/http/handler"
	"github.com/heavycaffeiner/stowcloud/go/engine/http/server"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/secret"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/settings/check"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/fsatomic"
)

// systemSetupGet says whether this deployment still needs setting up.
//
// It is the one thing an unauthenticated caller may learn about the server's
// state, and it has to be learnable: a client that cannot ask sends a person
// to a sign-in screen for an account that does not exist yet.
func (e *Engine) systemSetupGet(c *fiber.Ctx) error {
	if e.setup == nil {
		// No gate was built, so nothing here can be completed. Reported as
		// finished rather than as an error: the client's next move either way
		// is the sign-in screen.
		return writeJSON(c, fiber.StatusOK, handler.SetupStateOf(false))
	}

	open, err := e.setup.Open(c.UserContext())
	if err != nil {
		// The count could not be read. Answering "required" would invite a
		// caller to complete a form the gate will refuse, so the closed
		// direction is reported and the reason is logged.
		e.logger.Warn("the setup state could not be read", "error", err)
		return writeJSON(c, fiber.StatusOK, handler.SetupStateOf(false))
	}
	return writeJSON(c, fiber.StatusOK, handler.SetupStateOf(open))
}

// setupRequest is everything the first-run screen submits at once.
//
// Only the token and the account are required. The rest is configuration this
// screen asks for because it is the one moment somebody is certainly present:
// a host list named now is one nobody has to work out later from a request
// that was refused for a reason the screen did not explain.
type setupRequest struct {
	Token    string `json:"token"`
	Username string `json:"username"`
	Password string `json:"password"`

	// AppHosts are the names this server answers for. Until one is stored the
	// host guard runs in its first-boot mode, admitting a private client on
	// the strength of its address alone.
	AppHosts []string `json:"app_hosts"`

	// TrustedProxies may stay empty, which trusts none and reads the peer
	// address as the client's.
	TrustedProxies []string `json:"trusted_proxies"`

	// FirstShare is optional. A deployment with none lands on a screen that
	// says so and offers the button that creates one.
	FirstShare *setupShare `json:"first_share"`
}

type setupShare struct {
	Name string `json:"name"`
	Host string `json:"host"`
}

// systemSetupPost spends the token and creates the first administrator.
func (e *Engine) systemSetupPost(c *fiber.Ctx) error {
	if e.setup == nil {
		return refuse(c, apierr.Classified{
			Class: apierr.SetupComplete, Key: "setup.complete",
		})
	}

	var req setupRequest
	if err := decodeBody(c, &req); err != nil {
		return refuse(c, apierr.Classified{Class: apierr.Malformed})
	}
	if req.Username == "" || req.Password == "" {
		return refuse(c, apierr.Classified{Class: apierr.Malformed})
	}

	// Probed before anything is created, using the settings screen's own
	// checks. Creating the account is what shuts the gate, so a value that
	// would be refused has to surface while the form can still be resubmitted.
	network := setupNetworkOf(req)
	findings := check.Section(check.Input{
		Section:  "network",
		Body:     network,
		SelfHost: check.HostOnly(string(c.Request().Host())),
		DataDir:  e.dataDir,
		// Advisory here, blocking on the settings screen. Naming a DNS name
		// while connected by address is the normal way to set this up, and it
		// is indistinguishable from the typo that locks the operator out. The
		// screen can refuse and be corrected; this form has no second attempt,
		// so it reports and continues.
		Lockout: check.LockoutWarns,
	})
	if handler.Blocking(findings) {
		return writeJSON(c, fiber.StatusUnprocessableEntity,
			handler.ApplyOutcomeOf(false, false, false, findings))
	}

	var userID int64
	err := e.setup.Use(c.UserContext(), req.Token, func(ctx context.Context) error {
		id, cerr := e.Auth.CreateAdmin(ctx, req.Username, "", secret.New([]byte(req.Password)))
		if cerr != nil {
			return cerr
		}
		userID = id
		// Inside the gate's lock, so the grant lands before a second request
		// can observe the account and conclude setup is finished.
		return e.Core.GrantEveryShare(ctx, id)
	})
	if err != nil {
		return refuse(c, setupRefusal(err))
	}

	// Everything from here has an account behind it, so a failure is reported
	// rather than rolled back: the gate has closed and there is no second pass
	// at this form.
	out := handler.SetupOutcomeOf(userID, req.Username, findings)

	if serr := e.saveSetupNetwork(c.UserContext(), network); serr != nil {
		// Logged rather than returned. The account exists and the gate has
		// closed, so refusing here would report the whole first run as failed
		// when its irreversible half succeeded.
		e.logger.Error("the first-run network settings were not stored", "error", serr)
	}

	if req.FirstShare != nil && req.FirstShare.Name != "" && req.FirstShare.Host != "" {
		share, cerr := e.Core.CreateShare(c.UserContext(), core.ShareSpec{
			Name: req.FirstShare.Name,
			Host: req.FirstShare.Host,
		})
		if cerr != nil {
			// Named rather than fatal. The administrator exists and can sign
			// in, and the folder is one screen away.
			e.logger.Warn("the first share was not created", "error", cerr)
			out.ShareFailed = true
		} else {
			// The share was registered after the grant pass ran, so it carries
			// none yet. Granting again covers it, and the earlier shares are
			// already granted rather than granted twice.
			if gerr := e.Core.GrantEveryShare(c.UserContext(), userID); gerr != nil {
				e.logger.Warn("the first share was created without a grant", "error", gerr)
			}
			view := handler.ShareOf(share)
			out.Share = &view
		}
	}

	// Deliberately not a session. The account exists, and the client then
	// authenticates through the one path that issues a credential.
	return writeJSON(c, fiber.StatusOK, out)
}

// setupNetworkOf is the network section the form fills in, in the shape both
// the probes and the store read.
func setupNetworkOf(req setupRequest) map[string]any {
	out := map[string]any{"app_hosts": anyList(req.AppHosts)}
	if req.TrustedProxies != nil {
		out["trusted_proxies"] = anyList(req.TrustedProxies)
	}
	return out
}

// anyList widens a string slice into what the settings document holds.
func anyList(in []string) []any {
	out := make([]any, 0, len(in))
	for _, s := range in {
		out = append(out, s)
	}
	return out
}

// saveSetupNetwork stores the section and rereads it into the running server,
// which is what makes the host list the form just named take effect on the
// next request rather than at the next start.
func (e *Engine) saveSetupNetwork(ctx context.Context, network map[string]any) error {
	if err := e.State.MergeSettings(ctx, "network", network); err != nil {
		return err
	}
	e.loadSettings(ctx)
	return nil
}

// setupRefusal maps the gate's outcomes onto the wire.
//
// They are separate classes because they call for different actions: finish
// setting up, present a different token, or stop trying because setup is over.
func setupRefusal(err error) apierr.Classified {
	switch {
	case errors.Is(err, server.ErrSetupClosed):
		return apierr.Classified{Class: apierr.SetupComplete, Key: "setup.complete"}
	case errors.Is(err, server.ErrSetupNotIssued):
		return apierr.Classified{Class: apierr.SetupExpired, Key: "setup.not_issued"}
	case errors.Is(err, server.ErrSetupToken):
		return apierr.Classified{Class: apierr.SetupInvalidToken, Key: "setup.invalid_token"}
	}
	// Everything else came from creating the account: a name the rule refuses,
	// a password under the floor, a name already taken. The one classifier
	// maps those, so they are not spelled a second time here.
	return apierr.Classify(err, apierr.VisibilityKnown)
}

// setupTokenFile is where the issued token is published, under the data
// directory the operator already has to reach.
const setupTokenFile = "setup-token"

// issueSetupToken mints a first-run token and publishes it, when this
// deployment still needs one.
//
// Published rather than only printed: a process started by a supervisor has no
// terminal anybody reads, and the token is the only thing that opens the form.
// It goes to the data directory, which is somewhere the operator can already
// reach, at 0600 because anyone who reads it can create the administrator.
//
// A failure here does not stop the boot. The server still serves, and the
// operator's next move is to look at why the file could not be written rather
// than to restart into the same state.
func (e *Engine) issueSetupToken(ctx context.Context) {
	if e.setup == nil {
		return
	}
	open, err := e.setup.Open(ctx)
	if err != nil || !open {
		// Already set up, or a count that could not be read. Neither mints a
		// token: the gate would refuse it, and printing one would say
		// otherwise.
		return
	}

	token, ierr := e.setup.Issue(ctx)
	if ierr != nil {
		e.logger.Error("no first-run setup token could be issued", "error", ierr)
		return
	}

	path := filepath.Join(e.dataDir, setupTokenFile)
	werr := fsatomic.ReplaceFileDurable(path, 0o600, func(f *os.File) error {
		_, w := f.WriteString(token + "\n")
		return w
	})
	if werr != nil {
		e.logger.Error("the first-run setup token could not be written",
			"path", path, "error", werr)
		return
	}
	// The path and not the token. A log line carrying it would put the one
	// credential that creates the administrator into whatever collects logs.
	e.logger.Info("this deployment needs setting up; the token is in the data directory",
		"path", path, "valid_for", server.SetupTokenLifetime.String())
}
