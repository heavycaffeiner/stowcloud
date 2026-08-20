package handler

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"

	"github.com/heavycaffeiner/stowcloud/go/internal/apierr"
	"github.com/heavycaffeiner/stowcloud/go/internal/auth"
	"github.com/heavycaffeiner/stowcloud/go/internal/httpapi/mw"
	"github.com/heavycaffeiner/stowcloud/go/internal/secret"
)

// SessionCookie is the cookie the handlers set and clear; the auth middleware
// reads the same name, so the two sides cannot drift.
const SessionCookie = mw.SessionCookie

// The one route that creates a session. The body is the username and
// password; the response sets the session cookie and returns the account.
// A second-factor account answers 401 with the totp-required signal, and the
// client sends the code through the same route's factor field.
type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Factor   string `json:"factor,omitempty"`
}

type loginResponse struct {
	Status string `json:"status"`
	User   struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	} `json:"user"`
}

func Login(d Deps) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		var req loginRequest
		if err := decodeJSON(r, &req); err != nil {
			return err
		}
		if req.Username == "" || req.Password == "" {
			return apierr.BadRequest("auth.login_fields", "username")
		}
		ip := mw.ClientFrom(r.Context()).String()
		sess, err := d.Auth.Login(r.Context(), auth.LoginRequest{
			Name:     req.Username,
			Password: secret.New([]byte(req.Password)),
			Factor:   req.Factor,
			IP:       ip,
			UA:       r.UserAgent(),
			AMR:      1,
		}, 0)
		if err != nil {
			// An account with a second factor is not a refusal: the password
			// was right and the code screen is next. Reporting it as a failure
			// is what leaves a client with no way to ask for the code.
			if c, ok := secondFactorChallenge(d, r, req.Username, err); ok {
				return writeJSON(w, http.StatusOK, c)
			}
			return err
		}
		resp := loginResponse{Status: "ok"}
		resp.User.ID = sess.UserID
		resp.User.Name = req.Username
		setSessionCookie(w, sess)
		return writeJSON(w, http.StatusOK, resp)
	})
}

// The current session: the account behind the cookie, or 401 when there is
// none. The login screen's GET /api/setup is the only other unauthenticated
// read this surface answers.
type sessionResponse struct {
	User struct {
		ID      int64  `json:"id"`
		Name    string `json:"name"`
		Display string `json:"display,omitempty"`
		Admin   bool   `json:"admin"`
	} `json:"user"`
	// CSRF is the token the client sends back on state-changing requests. It
	// derives from the presenting session, and the login screen needs it
	// before the first write it makes.
	CSRF string `json:"csrf,omitempty"`
}

func Session(d Deps) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		uid, cerr := userOf(r)
		if cerr != nil {
			return cerr
		}
		p, _ := mw.PrincipalFrom(r.Context())
		admin, aerr := d.Auth.IsAdmin(r.Context(), int64(uid))
		if aerr != nil {
			return aerr
		}
		resp := sessionResponse{}
		resp.User.ID = int64(uid)
		resp.User.Name = p.Display
		if p.Display != "" {
			resp.User.Display = p.Display
		}
		resp.User.Admin = admin
		if c, err := r.Cookie(SessionCookie); err == nil && c.Value != "" {
			resp.CSRF = mw.DeriveCSRFToken(d.CSRFKey, c.Value)
		}
		return writeJSON(w, http.StatusOK, resp)
	})
}

// Logout revokes the presenting session and clears the cookie. 204, like the
// reference: there is nothing to say back.
func Logout(d Deps) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		if c, err := r.Cookie(SessionCookie); err == nil && c.Value != "" {
			_ = d.Auth.RevokeSession(r.Context(), secret.New([]byte(c.Value))) //nolint:errcheck // a session that fails to revoke is expired anyway.
		}
		http.SetCookie(w, &http.Cookie{
			Name: SessionCookie, Value: "", Path: "/",
			Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: -1,
		})
		w.WriteHeader(http.StatusNoContent)
		return nil
	})
}

// The app-password surface: mint, list, revoke. Each password is shown once,
// at creation, and a listing never reveals a token because none is stored.
type appPasswordRequest struct {
	Name   string   `json:"name"`
	Perms  uint16   `json:"perms"`
	Shares []string `json:"shares,omitempty"`
}

type appPasswordResponse struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Token string `json:"token,omitempty"`
}

func AppPasswords(d Deps) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		uid, cerr := userOf(r)
		if cerr != nil {
			return cerr
		}
		switch r.Method {
		case http.MethodGet:
			rows, err := d.Auth.AppPasswords(r.Context(), int64(uid))
			if err != nil {
				return err
			}
			out := make([]appPasswordResponse, 0, len(rows))
			for _, row := range rows {
				out = append(out, appPasswordResponse{ID: row.ID, Name: row.Name})
			}
			return writeJSON(w, http.StatusOK, map[string]any{"app_passwords": out})
		case http.MethodPost:
			var req appPasswordRequest
			if err := decodeJSON(r, &req); err != nil {
				return err
			}
			if req.Name == "" {
				return apierr.BadRequest("auth.apppw_name", "name")
			}
			token, err := d.Auth.CreateAppPassword(r.Context(), int64(uid), req.Name,
				auth.Scope{Perms: req.Perms, Shares: req.Shares}, 0)
			if err != nil {
				return err
			}
			return writeJSON(w, http.StatusCreated, appPasswordResponse{Name: req.Name, Token: token})
		}
		return apierr.BadRequest("auth.method", "method")
	})
}

// AppPasswordDelete revokes one of the caller's own app passwords.
func AppPasswordDelete(d Deps) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		uid, cerr := userOf(r)
		if cerr != nil {
			return cerr
		}
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			return apierr.BadRequest("auth.apppw_id", "id")
		}
		if err := d.Auth.RevokeAppPassword(r.Context(), int64(uid), id); err != nil {
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	})
}

// The session-management surface: list the caller's devices and sign one out.
type sessionRow struct {
	IDHash   string `json:"id"`
	LastSeen int64  `json:"last_seen_ns"`
	IP       string `json:"ip,omitempty"`
	UA       string `json:"ua,omitempty"`
	Current  bool   `json:"current"`
}

func Sessions(d Deps) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		uid, cerr := userOf(r)
		if cerr != nil {
			return cerr
		}
		if r.Method == http.MethodGet {
			rows, err := d.Auth.Sessions(r.Context(), int64(uid))
			if err != nil {
				return err
			}
			var current []byte
			if c, err := r.Cookie(SessionCookie); err == nil && c.Value != "" {
				if raw, derr := hex.DecodeString(c.Value); derr == nil {
					h := sha256.Sum256(raw)
					current = h[:]
				}
			}
			out := make([]sessionRow, 0, len(rows))
			for _, row := range rows {
				item := sessionRow{
					IDHash:   hex.EncodeToString(row.IDHash),
					LastSeen: row.LastSeenNs,
					IP:       row.IP,
					UA:       row.UA,
					Current:  string(row.IDHash) == string(current),
				}
				out = append(out, item)
			}
			return writeJSON(w, http.StatusOK, map[string]any{"sessions": out})
		}
		// DELETE /api/auth/sessions/{id}: sign out that device.
		hash, err := hex.DecodeString(r.PathValue("id"))
		if err != nil || len(hash) != 32 {
			return apierr.BadRequest("auth.session_id", "id")
		}
		if err := d.Auth.RevokeSessionByHash(r.Context(), int64(uid), hash); err != nil {
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	})
}
