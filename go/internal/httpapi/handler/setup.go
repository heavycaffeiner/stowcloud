// Linux only: it depends on packages that are Linux only.
//go:build linux

package handler

import (
	"context"
	"errors"
	"net/http"

	"github.com/heavycaffeiner/stowcloud/go/internal/apierr"
	"github.com/heavycaffeiner/stowcloud/go/internal/core"
	"github.com/heavycaffeiner/stowcloud/go/internal/httpapi/mw"
	"github.com/heavycaffeiner/stowcloud/go/internal/runtimecfg"
	"github.com/heavycaffeiner/stowcloud/go/internal/secret"
	"github.com/heavycaffeiner/stowcloud/go/internal/settingscheck"
)

// SetupOutcome is what a completed bootstrap returns: the account is created,
// and the client then authenticates through the one credential-issuing path.
type SetupOutcome struct {
	UserID   int64
	Username string
}

// SetupError is every way the bootstrap can refuse, each mapping to exactly
// one wire code in the handler.
type SetupError struct {
	Kind  SetupErrorKind
	Field string
}

func (e SetupError) Error() string { return e.Kind.String() }

// SetupErrorKind is the refusal category. The wire code is a function of it.
type SetupErrorKind int

const (
	SetupCompleted SetupErrorKind = iota
	SetupExpired
	SetupInvalidToken
	SetupInvalidUsername
	SetupWeakPassword
)

func (k SetupErrorKind) String() string {
	switch k {
	case SetupCompleted:
		return "setup is already complete"
	case SetupExpired:
		return "the setup token has expired"
	case SetupInvalidToken:
		return "invalid setup token"
	case SetupInvalidUsername:
		return "invalid username"
	case SetupWeakPassword:
		return "password is too short"
	}
	return "setup refused"
}

// Setup is the setup surface the Deps wire in. The interface is what the
// handler needs and nothing else, so a test can substitute a closed gate.
type Setup interface {
	IsRequired(ctx context.Context) bool
	Complete(ctx context.Context, token, username string, pw secret.Secret, ip string) (SetupOutcome, error)
}

// SetupState answers GET /api/setup: a bare boolean, the one thing an
// unauthenticated caller may learn about this server's state.
func SetupState(d Deps, gate Setup) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		// The field is the one the client reads. It was "open" here and
		// "required" on the wire the frontend was written against, so the
		// first-run screen never appeared: the client asked whether setup was
		// required, got an object without that field, read the absence as
		// false, and sent the person to a sign-in screen for an account that
		// does not exist yet.
		return writeJSON(w, http.StatusOK, map[string]bool{"required": gate.IsRequired(r.Context())})
	})
}

// setupRequest is the whole first-run form.
//
// The account is the only required part beyond the token. Everything else is
// a deployment's first configuration, offered here because this is the one
// moment somebody is definitely looking at the screen: an app-host list saved
// now is one they will not have to discover from a refused request later.
type setupRequest struct {
	Token    string `json:"token"`
	Username string `json:"username"`
	Password string `json:"password"`

	// AppHosts are the names this server will answer for. Required, because
	// until one is saved the host guard is running in its first-boot mode and
	// admitting the local network on the strength of the address alone.
	AppHosts []string `json:"app_hosts"`
	// TrustedProxies may stay empty, which trusts no proxy and reads the peer
	// address as the client address.
	TrustedProxies []string `json:"trusted_proxies"`
	// Bind may stay empty, which leaves the listener where it is.
	Bind string `json:"bind"`
	// FirstShare is optional. A deployment with none lands on a home screen
	// that says so and offers the button that creates one.
	FirstShare *setupShare `json:"first_share"`
}

type setupShare struct {
	Name     string `json:"name"`
	HostPath string `json:"host_path"`
}

// SetupComplete spends the one-time token and creates the first
// administrator. Every refusal maps to one stable code, and the gate closes
// permanently on success, so a token recovered from a log or a backup after
// setup is worth nothing.
func SetupComplete(d Deps, gate Setup) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		var req setupRequest
		if err := decodeJSON(r, &req); err != nil {
			return err
		}
		if req.Username == "" || req.Password == "" {
			return apierr.BadRequest("setup.fields", "username")
		}
		// The configuration is checked before the account is created, with the
		// same probes the settings screens run: a form that stored a value the
		// settings screen would refuse is a deployment that starts wrong. The
		// account is what makes the gate close, so nothing is created until
		// what would be saved around it is known to be acceptable.
		network := setupNetwork(req)
		findings := settingscheck.Section(settingscheck.Input{
			Section: "network", Body: network,
			SelfHost: settingscheck.HostOnly(r.Host), DataDir: d.DataDir,
			// A warning here, a block on the settings screen. The operator is
			// very often browsing by address while naming the DNS name the
			// deployment will actually be reached under, which is the correct
			// thing to do and which no rule can tell apart from the mistake.
			// Refusing it would make the form impossible to complete for that
			// deployment.
			Lockout: settingscheck.LockoutWarns,
		})
		if blocked(findings) {
			return settingsRefused(findings)
		}

		outcome, err := gate.Complete(r.Context(), req.Token, req.Username,
			secret.New([]byte(req.Password)), mw.ClientFrom(r.Context()).String())
		if err != nil {
			var se SetupError
			if errors.As(err, &se) {
				switch se.Kind {
				case SetupCompleted:
					return &apierr.RequestError{Status: http.StatusGone,
						Code: apierr.CodeSetupCompleted, Message: "first-run setup is already complete", Key: "setup.completed"}
				case SetupExpired:
					return &apierr.RequestError{Status: http.StatusForbidden,
						Code: apierr.CodeSetupTokenExpired, Message: "the setup token has expired", Key: "setup.token_expired"}
				case SetupInvalidToken:
					return &apierr.RequestError{Status: http.StatusForbidden,
						Code: apierr.CodeSetupInvalidToken, Message: "invalid setup token", Key: "setup.invalid_token"}
				case SetupInvalidUsername:
					return &apierr.RequestError{Status: http.StatusUnprocessableEntity,
						Code: apierr.CodeSetupInvalidUser, Message: "invalid username", Key: "setup.invalid_username",
						Args: []apierr.Arg{{Name: "reason", Value: se.Field}}}
				case SetupWeakPassword:
					return &apierr.RequestError{Status: http.StatusUnprocessableEntity,
						Code: apierr.CodeSetupWeakPassword, Message: "password is too short", Key: "setup.weak_password",
						Args: []apierr.Arg{{Name: "min_length", Value: "10"}}}
				}
			}
			return err
		}
		// Everything from here has an account behind it, so a failure is
		// reported rather than rolled back: the gate has closed and there is no
		// second chance at this form. Each piece says what it did.
		resp := map[string]any{
			"user": map[string]any{"id": outcome.UserID, "name": outcome.Username},
			// What the probes noticed and did not refuse over. The list the
			// operator just named not containing the address they are browsing
			// from is the one worth seeing: it is legitimate behind a proxy and
			// is a lockout otherwise.
			"warnings": settingscheck.Warnings(findings),
		}
		if serr := saveSetupNetwork(r, d, network); serr != nil {
			return serr
		}
		if req.FirstShare != nil && req.FirstShare.Name != "" && req.FirstShare.HostPath != "" {
			share, cerr := d.Core.CreateShare(r.Context(),
				core.ShareSpec{Name: req.FirstShare.Name, Host: req.FirstShare.HostPath})
			if cerr != nil {
				// Named rather than fatal: the administrator exists and can
				// sign in, and the folder is one click away on a screen built
				// for exactly that.
				return shareRefused(cerr)
			}
			if gerr := grantToCreator(r, d, outcome.UserID, share); gerr != nil {
				return gerr
			}
			sharesChanged(r, d)
			resp["share"] = shareOf(share)
		}

		// The listener last, because the response has to leave on the socket
		// the request arrived on. A bind that fails is reported and the old
		// address keeps serving, which is the swap's own contract.
		if req.Bind != "" && d.SwapListener != nil {
			if berr := d.SwapListener(r.Context(), req.Bind); berr != nil {
				resp["bind_failed"] = true
			}
		}
		// Deliberately not a session: the account is created, and the client
		// then authenticates through the one credential-issuing path.
		return writeJSON(w, http.StatusOK, resp)
	})
}

// setupNetwork is the network section the form fills in, in the shape the
// probes and the store both read.
func setupNetwork(req setupRequest) map[string]any {
	out := map[string]any{"app_hosts": toAnyList(req.AppHosts)}
	if req.TrustedProxies != nil {
		out["trusted_proxies"] = toAnyList(req.TrustedProxies)
	}
	if req.Bind != "" {
		out["bind"] = req.Bind
	}
	return out
}

// saveSetupNetwork stores the section and pushes the two live holders, which
// is what makes the host list the form just named take effect on the next
// request rather than the next start.
func saveSetupNetwork(r *http.Request, d Deps, network map[string]any) error {
	if d.State == nil {
		return nil
	}
	if err := d.State.MergeSettings(r.Context(), "network", network); err != nil {
		return err
	}
	if hosts, present, err := stringList(network, "app_hosts"); err == nil && present && len(hosts) > 0 {
		d.Hosts.Set(hosts)
	}
	if cidrs, present, err := stringList(network, "trusted_proxies"); err == nil && present {
		d.Trusted.Set(runtimecfg.ParsePrefixes(cidrs))
	}
	if d.Runtime != nil {
		d.Runtime.Set(runtimecfg.Load(r.Context(), d.State, runtimecfg.Defaults(), d.Log))
	}
	return nil
}
