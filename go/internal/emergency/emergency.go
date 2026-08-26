// Linux only: it edits the settings the Linux-only engine reads.
//go:build linux

// Package emergency is the safe-mode editor for the settings database.
//
// It exists because there is no configuration file any more. Every setting
// lives in the database and is edited from the web interface, which is served
// by the engine those same settings configure. A stored value that stops the
// engine coming up therefore takes the repair tool down with it, and the only
// remaining fix would be a SQL client on the volume.
//
// So this is the second door: it reads and writes the same settings document
// through the same probes, and it depends on nothing the engine owns. No core,
// no shares, no upload engine, no watcher. What it needs is the store, the
// auth service and a listener, which is the smallest set that can authenticate
// an administrator and commit a change.
//
// Three constraints hold in every mode, because this page edits the host guard
// and the share list and is therefore the most valuable target in the product:
//
// Network scope. A request is admitted only when the peer address is one
// smb.IsPrivate accepts. Everything else gets 404, not 403: the page does not
// confirm its existence to the outside.
//
// Authentication. The registered administrator's credentials, through the same
// auth service and the same rate limiter as the normal login, including the
// second factor when one is enrolled. No token shortcut and no recovery
// bypass.
//
// Audit. Every login and every write here is recorded with an event name of
// its own, so reading the log answers whether the safe-mode door was used.
package emergency

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"strings"

	"github.com/heavycaffeiner/stowcloud/go/internal/apierr"
	"github.com/heavycaffeiner/stowcloud/go/internal/auth"
	"github.com/heavycaffeiner/stowcloud/go/internal/httpapi/mw"
	"github.com/heavycaffeiner/stowcloud/go/internal/runtimecfg"
	"github.com/heavycaffeiner/stowcloud/go/internal/secret"
	"github.com/heavycaffeiner/stowcloud/go/internal/settingscheck"
	"github.com/heavycaffeiner/stowcloud/go/internal/smb"
	"github.com/heavycaffeiner/stowcloud/go/internal/store/state"
)

// Prefix is where the mux lives under the normal server, and the only path
// this package answers on.
const Prefix = "/emergency"

// The audit events. Their own names rather than the ordinary ones, because
// the question this log is read to answer is whether the safe-mode door was
// used at all.
const (
	EventLogin = "emergency.login"
	EventSave  = "emergency.settings_write"
)

// bodyLimit bounds a settings document. The whole document is a few kilobytes
// of scalars; this is room for an order of magnitude more and a refusal for
// anything a repair screen has no reason to send.
const bodyLimit = 1 << 20

// Deps is what the mux needs. Every field is something a process with no
// engine can still produce.
type Deps struct {
	Auth  *auth.Service
	State *state.DB
	Log   *slog.Logger

	// DataDir resolves a homes root the request did not name, for the probe
	// that checks whether one can be created.
	DataDir string

	// Reason names why the emergency door is being fronted, or is empty when
	// the deployment is healthy and this is only the always-on route. The
	// screen puts it in the banner, so somebody arriving at a redirect learns
	// what failed rather than only that something did.
	Reason func() string

	// Restart asks the process to exit so a supervisor starts it again, which
	// is how a repaired setting takes effect. A nil one leaves the action
	// reporting that this deployment has no supervisor to come back from.
	Restart func()

	// Trusted is the proxy boundary, read per request so a repair that widens
	// or narrows it applies to the next one. A nil one trusts no proxy, which
	// is the safe reading: the peer address is then the client address.
	Trusted *mw.TrustedSet

	// Page draws the screen. It is the same bundle the rest of the product is
	// served from, because the alternative is a second frontend maintained for
	// the one screen nobody looks at until it matters. A nil one leaves the
	// API answering and the path with no document, which is what a build
	// carrying no bundle has.
	Page http.Handler
}

// Handler is the emergency mux: the network gate, then login, settings read
// and write, and a restart action. Nothing else is mounted, so there is no
// file browsing and no user management behind this door.
func Handler(d Deps) http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET "+Prefix+"/api/state", door(d))
	m.HandleFunc("POST "+Prefix+"/api/login", login(d))
	m.HandleFunc("GET "+Prefix+"/api/settings", readSettings(d))
	m.HandleFunc("PATCH "+Prefix+"/api/settings/{section}", writeSettings(d))
	m.HandleFunc("POST "+Prefix+"/api/restart", restart(d))
	// The document itself, and only under this prefix. The client router owns
	// the path from there; everything the page fetches is one of the four
	// routes above.
	if d.Page != nil {
		m.Handle(Prefix, d.Page)
		m.Handle(Prefix+"/", d.Page)
	}
	return gate(d, m)
}

// gate is the network scope, and it is the outermost thing in this package.
//
// The refusal is 404 rather than 403 for the reason the threat model gives: a
// 403 tells whoever asked that the path exists and is guarded, which is an
// invitation. A 404 says the same thing this server says about every path it
// does not serve.
//
// The client address is resolved the same way the main chain resolves it, so a
// deployment behind a reverse proxy in a private range is not locked out of
// its own repair door by its own proxy. A forwarded header from a peer that is
// not a trusted proxy is discarded unparsed, which is what stops a public
// client claiming a private address.
func gate(d Deps, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !smb.IsPrivate(clientAddr(d, r)) {
			http.NotFound(w, r)
			return
		}
		if !sameOrigin(r) {
			refuse(w, http.StatusBadRequest, apierr.CodeInvalidRequest, "malformed request",
				"csrf.origin_refused")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// sameOrigin is the cross-site check every write here passes.
//
// It compares the Origin against the request's own Host rather than against
// the declared app-host list, which is deliberate: that list is one of the
// things this door repairs, so a list that is wrong would take the repair with
// it. What is being asserted is only that the page making the request was
// served from the same address it is posting to, which is true of this screen
// and false of a page on somebody else's site.
//
// The session cookie is SameSite=Lax, so a cross-site write does not carry a
// credential in the first place. This is the second layer, for the browser
// that gets that wrong.
func sameOrigin(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		// No Origin on a write is not a browser, and a browser is the only
		// thing a cross-site request can come from. A curl in a terminal is the
		// operator, who already has the credential.
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

// clientAddr is who this request is from, under the same trust rule the main
// chain uses.
//
// When the chain already resolved it, that answer is taken: the emergency mux
// is mounted inside the normal server too, and re-deciding there would be a
// second rule that can disagree with the first. Standalone there is no chain,
// so the resolution happens here.
func clientAddr(d Deps, r *http.Request) netip.Addr {
	if a, ok := mw.ResolvedClient(r.Context()); ok {
		return a
	}
	var prefixes []netip.Prefix
	if d.Trusted != nil {
		prefixes = d.Trusted.Get()
	}
	return mw.ResolveClient(prefixes, r.RemoteAddr,
		r.Header.Get("CF-Connecting-IP"), r.Header.Get("X-Forwarded-For"))
}

// door is what the screen reads before anybody has signed in: whether there
// is an account to sign in as, and what the banner says.
//
// A data directory with no administrator has nothing this can authenticate, so
// it says so and points at setup rather than drawing a login that cannot
// succeed.
func door(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		out := map[string]any{"setup_required": false, "reason": ""}
		if d.Auth != nil {
			n, err := d.Auth.CountUsers(r.Context())
			// A count that cannot be read is reported as an account existing,
			// which draws the login. The alternative, drawing the setup
			// pointer, would tell somebody who cannot read the account table
			// to go and create an administrator.
			out["setup_required"] = err == nil && n == 0
		}
		if d.Reason != nil {
			out["reason"] = d.Reason()
		}
		writeJSON(w, http.StatusOK, out)
	}
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Factor   string `json:"factor"`
}

// login is the same credential check the ordinary login runs, and then the
// administrator check on top.
//
// The same rate limiter, because this door is reachable from every machine on
// the network and an unlimited password oracle behind it would be worse than
// no door. The same second factor, because an account with one enrolled has
// one everywhere or it has one nowhere.
func login(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req loginRequest
		if !decode(w, r, &req) {
			return
		}
		ip := clientAddr(d, r).String()
		sess, err := d.Auth.Login(r.Context(), auth.LoginRequest{
			Name: req.Username, Password: secret.New([]byte(req.Password)),
			Factor: req.Factor, IP: ip, UA: r.UserAgent(), AMR: 1,
		}, 0)
		if err != nil {
			if errors.Is(err, auth.ErrSecondFactor) {
				// The password was right and the code screen is next, which is
				// not a refusal. Reporting it as one leaves an enrolled
				// administrator with no way to ask for the code.
				writeJSON(w, http.StatusOK, map[string]any{"status": "totp_required"})
				return
			}
			refuse(w, http.StatusUnauthorized, apierr.CodeAuthInvalid, "invalid credentials", "")
			return
		}

		// The administrator check, after the credential and never instead of
		// it. An ordinary account signing in here would reach the settings
		// document, which is every permission decision this deployment makes.
		admin, aerr := d.Auth.IsAdmin(r.Context(), sess.UserID)
		if aerr != nil || !admin {
			d.Auth.Record(r.Context(), sess.UserID, EventLogin, "not_an_administrator", ip, r.UserAgent(), false)
			refuse(w, http.StatusUnauthorized, apierr.CodeAuthInvalid, "invalid credentials", "")
			return
		}
		d.Auth.Record(r.Context(), sess.UserID, EventLogin, "", ip, r.UserAgent(), true)

		// The product's own session cookie, not a second one. The credential
		// and the administrator check are the same as the ordinary login, so
		// this grants nothing that login would not, and it means the operator
		// is already signed in when the server comes back.
		//
		// SameSite is what stands in for the CSRF token the chain would add:
		// this mux is outside the chain by design, and Lax keeps the cookie off
		// every cross-site POST and PATCH, which is every write here.
		http.SetCookie(w, &http.Cookie{
			Name: mw.SessionCookie, Value: hex.EncodeToString(sess.Token.Reveal()),
			Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode,
		})
		writeJSON(w, http.StatusOK, map[string]any{
			"status": "ok",
			"user":   map[string]any{"id": sess.UserID, "name": req.Username},
		})
	}
}

// admin resolves the caller and refuses anyone who is not an administrator.
//
// The session cookie only. There is deliberately no app-password path here: an
// app password is a filesystem capability handed to a device, and a device that
// could edit the settings document could grant itself anything.
func admin(d Deps, w http.ResponseWriter, r *http.Request) (int64, bool) {
	c, err := r.Cookie(mw.SessionCookie)
	if err != nil || c.Value == "" {
		refuse(w, http.StatusUnauthorized, apierr.CodeAuthRequired, "authentication required", "")
		return 0, false
	}
	raw, derr := hex.DecodeString(c.Value)
	if derr != nil {
		refuse(w, http.StatusUnauthorized, apierr.CodeAuthRequired, "authentication required", "")
		return 0, false
	}
	p, lerr := d.Auth.LookupSession(r.Context(), secret.New(raw))
	if lerr != nil {
		refuse(w, http.StatusUnauthorized, apierr.CodeAuthRequired, "authentication required", "")
		return 0, false
	}
	ok, aerr := d.Auth.IsAdmin(r.Context(), p.UserID)
	if aerr != nil || !ok {
		refuse(w, http.StatusForbidden, apierr.CodeAuthInvalid, "administrators only", "")
		return 0, false
	}
	return p.UserID, true
}

// readSettings hands back the stored document whole.
//
// Whole rather than a rendered field list, because the engine may not be
// running and the field list is built from what the engine loaded. What is
// definitely true in every mode is what is in the database, so that is what
// this returns, alongside the defaults so the screen can show what an absent
// key runs as.
func readSettings(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := admin(d, w, r); !ok {
			return
		}
		doc, err := d.State.Settings(r.Context())
		if err != nil {
			refuse(w, http.StatusInternalServerError, "internal", "the settings could not be read", "")
			return
		}
		v := runtimecfg.Load(r.Context(), d.State, runtimecfg.Defaults(), d.Log)
		writeJSON(w, http.StatusOK, map[string]any{
			"stored":   doc,
			"sections": settingscheck.Sections(),
			// The two values a repair is usually about, resolved: a listener
			// nothing can bind and a host list nobody matches are what bring
			// somebody to this screen, and both are easier to fix when the
			// screen can show what is in force.
			"listen":    v.Listen,
			"app_hosts": v.AppHosts,
		})
	}
}

// writeSettings commits one section, through the same probes every other
// surface runs.
//
// The one difference is the lockout finding, which warns here instead of
// blocking. This screen is where somebody goes to repair a host list that
// already locked them out, so refusing over the host they are currently on
// would refuse the repair itself.
//
// A save here takes effect on the next start. Nothing in this process is
// holding the value: standalone there is no engine to push it into, and inside
// the normal server the engine is either degraded or would need its own
// restart for the sections that reach this screen.
func writeSettings(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid, ok := admin(d, w, r)
		if !ok {
			return
		}
		section := r.PathValue("section")
		if !settingscheck.Known(section) {
			refuse(w, http.StatusNotFound, apierr.CodeFsNotFound, "no such settings section",
				"settings.unknown_section")
			return
		}
		var body map[string]any
		if !decode(w, r, &body) {
			return
		}

		findings := settingscheck.Section(settingscheck.Input{
			Section: section, Body: body,
			SelfHost: settingscheck.HostOnly(r.Host), DataDir: d.DataDir,
			Lockout: settingscheck.LockoutWarns,
		})
		if settingscheck.Blocked(findings) {
			writeErr(w, settingscheck.Refused(findings))
			return
		}
		ip := clientAddr(d, r).String()
		if err := d.State.MergeSettings(r.Context(), section, body); err != nil {
			d.Auth.Record(r.Context(), uid, EventSave, section, ip, r.UserAgent(), false)
			refuse(w, http.StatusInternalServerError, "internal", "the settings could not be written", "")
			return
		}
		d.Auth.Record(r.Context(), uid, EventSave, section, ip, r.UserAgent(), true)
		writeJSON(w, http.StatusOK, map[string]any{
			// Stored, and in effect after a restart. Saying so is the point:
			// the engine that would apply it is the one this screen exists
			// because of.
			"applied":  "restart_required",
			"warnings": settingscheck.Warnings(findings),
		})
	}
}

// restart asks the process to exit so a supervisor starts it again, which is
// how the repair takes effect.
func restart(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := admin(d, w, r); !ok {
			return
		}
		if d.Restart == nil {
			// Honest rather than a success that changes nothing: a deployment
			// with no supervisor stays stopped, and telling somebody the
			// server is coming back when nothing will start it is worse than
			// telling them to start it themselves.
			writeJSON(w, http.StatusOK, map[string]any{"restarting": false})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"restarting": true})
		// After the response, so the caller learns the request was accepted
		// rather than losing the connection and having to guess.
		d.Restart()
	}
}

// Redirecting sends every path outside the emergency prefix to it.
//
// This is what the serve layer mounts in place of the ordinary handler when
// the engine could not be built: browsing to the deployment's address then
// lands on the repair screen with a banner naming what failed, rather than on
// a connection refused or a blank page.
//
// The API paths answer with a status instead of a redirect. A running client
// following a redirect to an HTML document reports a corrupt server; a 503
// naming the reason is something it can show.
func Redirecting(emergency http.Handler, page http.Handler, reason func() string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, Prefix) {
			emergency.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			why := ""
			if reason != nil {
				why = reason()
			}
			apierr.Write(w, http.StatusServiceUnavailable,
				apierr.NewError("engine.unavailable", "the server is in emergency mode",
					"emergency.engine_unavailable", apierr.Arg{Name: "reason", Value: why}))
			return
		}
		// The frontend's own assets still have to load, or the screen the
		// redirect lands on cannot draw. Everything else goes to it.
		if page != nil && isAsset(r.URL.Path) {
			page.ServeHTTP(w, r)
			return
		}
		http.Redirect(w, r, Prefix, http.StatusFound)
	})
}

// isAsset reports whether a path is a build artifact the page needs rather
// than a route the client owns.
func isAsset(p string) bool {
	return strings.HasPrefix(p, "/app/") || strings.HasPrefix(p, "/favicon")
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	body, err := io.ReadAll(io.LimitReader(r.Body, bodyLimit+1))
	if err != nil {
		refuse(w, http.StatusBadRequest, apierr.CodeInvalidRequest, "malformed request", "")
		return false
	}
	if len(body) > bodyLimit {
		refuse(w, http.StatusRequestEntityTooLarge, apierr.CodeInvalidRequest, "the request is too large", "")
		return false
	}
	if err := json.Unmarshal(body, v); err != nil {
		refuse(w, http.StatusBadRequest, apierr.CodeInvalidRequest, "malformed request", "fs.invalid_json")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v) //nolint:errcheck // a client that stopped reading cannot be told anything.
}

func refuse(w http.ResponseWriter, status int, code apierr.Code, msg apierr.Message, key apierr.MessageKey) {
	apierr.Write(w, status, apierr.NewError(code, msg, key))
}

// writeErr renders an error the probes produced, which already carries its
// own status and catalogue key.
func writeErr(w http.ResponseWriter, err error) {
	var re *apierr.RequestError
	if errors.As(err, &re) {
		apierr.Write(w, re.Status, apierr.NewError(re.Code, apierr.Message(re.Message), re.Key, re.Args...))
		return
	}
	refuse(w, http.StatusInternalServerError, "internal", "internal error", "")
}
