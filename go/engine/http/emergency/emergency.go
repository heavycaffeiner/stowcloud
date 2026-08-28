// Linux only, because the settings it edits configure a Linux-only engine.
//go:build linux

// Package emergency is the second way into the settings database.
//
// There is no configuration file. Every setting is stored in the database and
// edited from the web interface that those same settings configure, so a value
// that stops the engine starting also takes away the tool for fixing it. What
// would be left is a SQL client on the volume.
//
// This door avoids that by depending on nothing the engine owns: no core, no
// shares, no uploads, no watcher. A store, an authenticator and a listener are
// enough to identify an administrator and commit a change, and each of those
// can be built by a process whose engine failed.
//
// The page edits the host guard and the share list, which makes it the most
// valuable target here, so three rules hold in every mode. A request from a
// public peer is answered 404 rather than 403, since 403 confirms the path
// exists. Authentication is the product's own, with the same limiter and the
// same second factor, because a private password check here would be a second
// authentication surface with none of the defences. Every login and every write
// is recorded under an event name of its own, so the log can answer whether
// this door was used.
package emergency

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/netzone"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/secret"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/auth"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/settings/check"
)

// Prefix is the only path this package answers on.
const Prefix = "/emergency"

// Audit events, named for this door rather than reusing the ordinary ones. The
// question the log gets read to answer is whether safe mode was used at all,
// and a shared event name cannot answer it.
const (
	EventLogin = "emergency.login"
	EventSave  = "emergency.settings_write"
)

// bodyLimit caps a settings document. Every value in it is a scalar or a short
// list, so a whole document runs to a few kilobytes; a megabyte leaves plenty of
// slack and still refuses a body no repair screen would send.
const bodyLimit = 1 << 20

// SessionCookie is the product's session cookie, and this door sets the same
// one the rest of the product reads: a repaired server then finds the operator
// already signed in rather than presenting a second login.
//
// The __Host- prefix is a browser-enforced rule, not decoration. A cookie
// carrying it is rejected unless it is Secure, has Path=/ and names no Domain,
// which together stop a subdomain writing a session for this origin. Every
// Set-Cookie below satisfies all three, and a change that drops one silently
// stops the cookie being stored at all.
const SessionCookie = "__Host-sc_sid"

// Authenticator is the part of the auth service this door uses. An interface
// rather than the concrete service because the door's own rules (administrator
// only, its own audit events, no app-password path) are what the tests need to
// pin, and they are visible here rather than inside a login implementation.
type Authenticator interface {
	Login(ctx context.Context, req auth.LoginRequest, ttl time.Duration) (auth.Session, error)
	LookupSession(ctx context.Context, token secret.Secret) (auth.Principal, error)
	IsAdmin(ctx context.Context, userID int64) (bool, error)
	CountUsers(ctx context.Context) (int64, error)
	Record(ctx context.Context, actor int64, event, target, ip, ua string, ok bool)
}

// SettingsStore is the settings document, read whole and merged one section at
// a time.
type SettingsStore interface {
	Settings(ctx context.Context) (map[string]any, error)
	MergeSettings(ctx context.Context, section string, value any) error
}

// Deps is what the door needs, and every field is something a process with no
// engine can still produce.
type Deps struct {
	Auth  Authenticator
	State SettingsStore

	// DataDir is the base the homes probe falls back to when the submitted
	// section names no root of its own.
	DataDir string

	// Reason names why the door is being fronted, and is empty when the
	// deployment is healthy and this is only the always-on route. The screen
	// shows it, so somebody who arrived at a redirect learns what failed.
	Reason func() string

	// Restart ends the process so a supervisor brings it back, which is how a
	// saved value reaches a running engine. Leave it nil where nothing would
	// restart the process; the action then says so instead of pretending.
	Restart func()

	// ClientAddr resolves who a request is from. The main chain has already
	// decided this when the door is mounted inside it, and re-deciding would be
	// a second rule that can disagree with the first. A nil one falls back to
	// the peer address, which trusts no proxy.
	ClientAddr func(r *http.Request) netip.Addr

	// Page draws the screen. A nil one leaves the API answering and the path
	// with no document, which is what a build carrying no bundle has.
	Page http.Handler
}

// Handler is the door: the network gate, then login, settings read and write,
// and restart. Nothing else is mounted, so there is no file browsing and no
// user management behind it.
func Handler(d Deps) http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET "+Prefix+"/api/state", state(d))
	m.HandleFunc("POST "+Prefix+"/api/login", login(d))
	m.HandleFunc("GET "+Prefix+"/api/settings", readSettings(d))
	m.HandleFunc("PATCH "+Prefix+"/api/settings/{section}", writeSettings(d))
	m.HandleFunc("POST "+Prefix+"/api/restart", restart(d))
	if d.Page != nil {
		m.Handle(Prefix, d.Page)
		m.Handle(Prefix+"/", d.Page)
	}
	return gate(d, m)
}

// gate is the network scope and the outermost thing here.
//
// 404 rather than 403 because 403 tells the caller the path exists and is
// guarded, which is an invitation. 404 is what this server says about every
// path it does not serve.
func gate(d Deps, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !netzone.IsPrivate(clientAddr(d, r)) {
			http.NotFound(w, r)
			return
		}
		if !sameOrigin(r) {
			refuse(w, http.StatusBadRequest, "invalid_request", "malformed request")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// sameOrigin compares Origin against the request's own Host rather than against
// the configured app-host list. That list is one of the things this door
// repairs, so a wrong list would take the repair with it. What is asserted is
// only that the page posting was served from the address it is posting to.
//
// The session cookie is SameSite=Lax, so a cross-site write carries no
// credential in the first place. This is the second layer.
func sameOrigin(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		// No Origin on a write means it did not come from a browser, and a
		// browser is the only thing a cross-site request can come from. A
		// terminal client is the operator, who already holds the credential.
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

// clientAddr is who this request is from. Without a resolver the peer address
// is used, which is the safe reading: no proxy is trusted, so no forwarded
// header can claim a private address.
func clientAddr(d Deps, r *http.Request) netip.Addr {
	if d.ClientAddr != nil {
		return d.ClientAddr(r)
	}
	ap, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		// A RemoteAddr that does not parse is not something to guess at. The
		// zero address is not private, so the gate refuses.
		return netip.Addr{}
	}
	return ap.Addr().Unmap()
}

// state is what the screen reads before anybody signs in: whether there is an
// account to sign in as, and what the banner says.
func state(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		out := map[string]any{"setup_required": false, "reason": ""}
		if d.Auth != nil {
			n, err := d.Auth.CountUsers(r.Context())
			// A count that cannot be read reports an account existing, which
			// draws the login. The other way round would tell somebody who
			// cannot reach the account table to go and create an administrator.
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

// login is the ordinary credential check with the administrator check on top.
//
// The same limiter, because this door is reachable from every machine on the
// network and an unlimited password oracle behind it would be worse than no
// door. The same second factor, because an account either has one everywhere or
// has one nowhere.
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
				// Getting here means the password verified and only the code is
				// outstanding. Answering 401 would be a lie that leaves an
				// enrolled administrator with no way to present it.
				writeJSON(w, http.StatusOK, map[string]any{"status": "totp_required"})
				return
			}
			refuse(w, http.StatusUnauthorized, "auth_invalid", "invalid credentials")
			return
		}

		// Ordering matters: the credential is checked first and this is asked
		// afterwards, never in its place. Behind this door is the settings
		// document, and editing that document rewrites every permission this
		// deployment enforces.
		admin, aerr := d.Auth.IsAdmin(r.Context(), sess.UserID)
		if aerr != nil || !admin {
			d.Auth.Record(r.Context(), sess.UserID, EventLogin, "not_an_administrator", ip, r.UserAgent(), false)
			refuse(w, http.StatusUnauthorized, "auth_invalid", "invalid credentials")
			return
		}
		d.Auth.Record(r.Context(), sess.UserID, EventLogin, "", ip, r.UserAgent(), true)

		// The product's own cookie rather than a second one. The credential and
		// the administrator check match the ordinary login, so this grants
		// nothing that login would not.
		//
		// Lax is doing the job a CSRF token does in the main chain, which this
		// mux deliberately sits outside. It withholds the cookie from any POST
		// or PATCH that started on another site, and every write here is one.
		http.SetCookie(w, &http.Cookie{
			Name: SessionCookie, Value: hex.EncodeToString(sess.Token.Reveal()),
			Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode,
		})
		writeJSON(w, http.StatusOK, map[string]any{
			"status": "ok",
			"user":   map[string]any{"id": sess.UserID, "name": req.Username},
		})
	}
}

// requireAdmin resolves the caller and refuses anyone who is not an
// administrator.
//
// The session cookie only. There is deliberately no app-password path: an app
// password is a filesystem capability handed to a device, and a device able to
// edit the settings document could grant itself anything.
func requireAdmin(d Deps, w http.ResponseWriter, r *http.Request) (int64, bool) {
	c, err := r.Cookie(SessionCookie)
	if err != nil || c.Value == "" {
		refuse(w, http.StatusUnauthorized, "auth_required", "authentication required")
		return 0, false
	}
	raw, derr := hex.DecodeString(c.Value)
	if derr != nil {
		refuse(w, http.StatusUnauthorized, "auth_required", "authentication required")
		return 0, false
	}
	p, lerr := d.Auth.LookupSession(r.Context(), secret.New(raw))
	if lerr != nil {
		refuse(w, http.StatusUnauthorized, "auth_required", "authentication required")
		return 0, false
	}
	ok, aerr := d.Auth.IsAdmin(r.Context(), p.UserID)
	if aerr != nil || !ok {
		refuse(w, http.StatusForbidden, "auth_invalid", "administrators only")
		return 0, false
	}
	return p.UserID, true
}

// readSettings returns the settings document unmodified.
//
// Whole rather than a rendered field list, because the field list is built from
// what the engine loaded and the engine may not be running. What is true in
// every mode is what the database holds.
func readSettings(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := requireAdmin(d, w, r); !ok {
			return
		}
		doc, err := d.State.Settings(r.Context())
		if err != nil {
			refuse(w, http.StatusInternalServerError, "internal", "the settings could not be read")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"stored":   doc,
			"sections": check.Sections(),
		})
	}
}

// writeSettings commits one section through the same probes every other surface
// runs.
//
// Lockout findings warn here where the settings screen refuses. Somebody
// reaching this page has usually already been shut out by the host list, and a
// refusal keyed on the host they arrived on would reject the repair itself.
//
// A save takes effect on the next start. Nothing in this process holds the
// value: standalone there is no engine to push it into, and inside the normal
// server the engine is either degraded or needs its own restart anyway.
func writeSettings(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid, ok := requireAdmin(d, w, r)
		if !ok {
			return
		}
		section := r.PathValue("section")
		if !check.Known(section) {
			refuse(w, http.StatusNotFound, "not_found", "no such settings section")
			return
		}
		var body map[string]any
		if !decode(w, r, &body) {
			return
		}

		findings := check.Section(check.Input{
			Section: section, Body: body,
			SelfHost: check.HostOnly(r.Host), DataDir: d.DataDir,
			Lockout: check.LockoutWarns,
		})
		if check.Blocked(findings) {
			writeFindings(w, http.StatusUnprocessableEntity, check.Blocking(findings))
			return
		}
		ip := clientAddr(d, r).String()
		if err := d.State.MergeSettings(r.Context(), section, body); err != nil {
			d.Auth.Record(r.Context(), uid, EventSave, section, ip, r.UserAgent(), false)
			refuse(w, http.StatusInternalServerError, "internal", "the settings could not be written")
			return
		}
		d.Auth.Record(r.Context(), uid, EventSave, section, ip, r.UserAgent(), true)
		writeJSON(w, http.StatusOK, map[string]any{
			// Committed to the database, waiting on a restart to take effect.
			// The caller is told which, because the engine that would pick the
			// value up is the one whose failure brought them here.
			"applied":  "restart_required",
			"warnings": renderFindings(check.Advisory(findings)),
		})
	}
}

// restart ends the process so a supervisor brings it back with the saved
// values loaded.
func restart(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := requireAdmin(d, w, r); !ok {
			return
		}
		if d.Restart == nil {
			// Honest rather than a success that changes nothing. A deployment
			// with no supervisor stays stopped, and telling somebody the server
			// is coming back when nothing will start it is worse than telling
			// them to start it themselves.
			writeJSON(w, http.StatusOK, map[string]any{"restarting": false})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"restarting": true})
		// Sequenced after the write so the client sees its request accepted.
		// Exiting first drops the connection and leaves the operator unsure
		// whether the restart was even reached.
		d.Restart()
	}
}

// Redirecting sends every path outside the prefix to the door.
//
// This is what the serve layer mounts in place of the ordinary handler when the
// engine could not be built, so browsing to the deployment lands on the repair
// screen with a banner naming what failed rather than on a refused connection.
//
// A client expecting JSON treats an HTML body as a broken server, so API paths
// get a 503 carrying the reason instead of a redirect to the page.
func Redirecting(door http.Handler, page http.Handler, reason func() string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, Prefix) {
			door.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			why := ""
			if reason != nil {
				why = reason()
			}
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{
				"error":  "engine_unavailable",
				"reason": why,
			})
			return
		}
		// Scripts and icons have to resolve for the redirect target to render
		// at all, so they are served rather than bounced.
		if page != nil && isAsset(r.URL.Path) {
			page.ServeHTTP(w, r)
			return
		}
		http.Redirect(w, r, Prefix, http.StatusFound)
	})
}

// isAsset reports whether a path is a build artifact the page needs rather than
// a route the client owns.
func isAsset(p string) bool {
	return strings.HasPrefix(p, "/app/") || strings.HasPrefix(p, "/favicon")
}

// decode reads a JSON body under the size limit.
//
// The limit is applied by reading one byte past it rather than by trusting
// Content-Length, which a client controls and may omit.
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	body, err := io.ReadAll(io.LimitReader(r.Body, bodyLimit+1))
	if err != nil {
		refuse(w, http.StatusBadRequest, "invalid_request", "malformed request")
		return false
	}
	if len(body) > bodyLimit {
		refuse(w, http.StatusRequestEntityTooLarge, "invalid_request", "the request is too large")
		return false
	}
	if err := json.Unmarshal(body, v); err != nil {
		refuse(w, http.StatusBadRequest, "invalid_request", "malformed request")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v) //nolint:errcheck // a client that stopped reading cannot be told anything.
}

func refuse(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{"error": code, "message": msg})
}

// renderFindings turns findings into what the screen draws. The key and its
// arguments travel separately, because the rendering contract is the client's.
func renderFindings(fs []check.Finding) []map[string]any {
	out := make([]map[string]any, 0, len(fs))
	for _, f := range fs {
		out = append(out, map[string]any{
			"section": f.Section,
			"field":   f.Field,
			"reason":  f.ReasonKey,
			"args":    f.Args,
		})
	}
	return out
}

func writeFindings(w http.ResponseWriter, status int, fs []check.Finding) {
	writeJSON(w, status, map[string]any{
		"error":    "settings_refused",
		"findings": renderFindings(fs),
	})
}
