package handler

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"

	"github.com/heavycaffeiner/stowcloud/go/internal/acl"
	"github.com/heavycaffeiner/stowcloud/go/internal/apierr"
	"github.com/heavycaffeiner/stowcloud/go/internal/auth"
	"github.com/heavycaffeiner/stowcloud/go/internal/core"
	"github.com/heavycaffeiner/stowcloud/go/internal/httpapi/mw"
	"github.com/heavycaffeiner/stowcloud/go/internal/limits"
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
	User sessionUser `json:"user"`
	// Roots is the caller's virtual root: one entry per share they may read.
	// It is the whole of what the interface has to navigate with, and without
	// it the browse screen has no folder to open: it falls back to the virtual
	// root, which no path names, and every listing is refused.
	//
	// Always a list, never absent. A client that has to tell "no shares" from
	// "the field is missing" branches on the difference and one of the two
	// branches is never tested.
	Roots []sessionRoot `json:"roots"`
	// CSRF is the token the client sends back on state-changing requests. It
	// derives from the presenting session, and the login screen needs it
	// before the first write it makes.
	CSRF string `json:"csrf,omitempty"`
	// Limits are the upload bounds a client plans a transfer against, and
	// Features what this deployment actually serves.
	Limits   sessionLimits   `json:"limits"`
	Features sessionFeatures `json:"features"`
	// OIDC is what the settings screen draws the single-sign-on section from.
	OIDC sessionOIDC `json:"oidc"`
}

// sessionUser is the account as the interface reads it.
//
// The field names are the client's contract rather than this package's
// preference: it reads is_admin, and a server sending admin draws no
// administrator navigation at all while every admin API call still succeeds.
// That is the shape of failure a route check cannot see, because the route is
// mounted and the status is 200.
type sessionUser struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	IsAdmin     bool   `json:"is_admin"`
	TOTPEnabled bool   `json:"totp_enabled"`
	SMBOptOut   bool   `json:"smb_opt_out"`
	SMBEnabled  bool   `json:"smb_enabled"`
}

// sessionRoot is one readable share, labeled the way the caller's own grant
// labels it.
type sessionRoot struct {
	Label            string `json:"label"`
	Perms            uint16 `json:"perms"`
	ShareKind        string `json:"share_kind"`
	SharedExternally bool   `json:"shared_externally"`
	TrashEnabled     bool   `json:"trash_enabled"`
}

type sessionLimits struct {
	ChunkSize uint64 `json:"chunk_size"`
	ChunkMin  uint64 `json:"chunk_min"`
	// Null rather than zero when there is no cap: zero is a size, and a client
	// reading it as one refuses every file.
	MaxFileSize *uint64 `json:"max_file_size"`
	Parallel    int     `json:"parallel"`
}

type sessionFeatures struct {
	WebDAV  bool   `json:"webdav"`
	SMB     bool   `json:"smb"`
	Preview bool   `json:"preview"`
	Trash   bool   `json:"trash"`
	Shares  bool   `json:"shares"`
	Search  string `json:"search"`
}

// sessionOIDC is the caller's own link.
//
// The hint is a few characters from each end of the identifier, never the whole
// thing: enough to recognise which identity is attached, which is the only
// question this screen asks, and not enough to be an identifier somebody can
// take away with them.
type sessionOIDC struct {
	Linked bool `json:"linked"`
	// Absent rather than empty when nothing is linked: the field being there
	// with no value reads as a link with a blank subject.
	SubjectHint string `json:"subject_hint,omitempty"`
	// A decimal string for the reason every nanosecond field here is: the
	// value is past what a JSON number survives in a browser.
	LinkedNs string `json:"linked_ns,omitempty"`
}

// subjectHint is the first and last few characters of an identifier.
//
// A short one is shown whole rather than padded into something that looks
// longer than it is: it is already no more identifying than its own length
// allows, and a fake ellipsis would misrepresent what is stored.
func subjectHint(sub string) string {
	const edge = 4
	if len(sub) <= edge*2 {
		return sub
	}
	return sub[:edge] + "..." + sub[len(sub)-edge:]
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
		// The account row, not the principal alone: the principal carries what
		// the credential path needed, and the interface draws the SMB and
		// second-factor toggles from the stored account.
		row, rerr := d.Auth.UserByID(r.Context(), int64(uid))
		if rerr != nil {
			return rerr
		}

		resp := sessionResponse{
			User: sessionUser{
				ID:          int64(uid),
				Name:        row.Name,
				DisplayName: row.Display,
				IsAdmin:     admin,
				TOTPEnabled: row.TOTPEnabled,
				SMBOptOut:   !row.SMBEnabled,
				SMBEnabled:  row.SMBEnabled,
			},
			Roots:    rootsOf(d, uid),
			Limits:   limitsOf(d),
			Features: featuresOf(d),
		}
		if row.Display == "" {
			resp.User.DisplayName = p.Display
		}
		if c, err := r.Cookie(SessionCookie); err == nil && c.Value != "" {
			resp.CSRF = mw.DeriveCSRFToken(d.CSRFKey, c.Value)
		}

		// An account with no link is the ordinary case, not a failure: this
		// server never creates an account from a provider identity, so most
		// accounts have none.
		switch link, lerr := d.Auth.OIDCLinkOf(r.Context(), int64(uid)); {
		case lerr == nil:
			resp.OIDC = sessionOIDC{
				Linked:      true,
				SubjectHint: subjectHint(link.Subject),
				LinkedNs:    strconv.FormatInt(link.LinkedNs, 10),
			}
		case errors.Is(lerr, auth.ErrNoOIDCLink):
		default:
			return lerr
		}

		return writeJSON(w, http.StatusOK, resp)
	})
}

// rootsOf projects the caller's readable shares.
//
// Always a list, so a client never has to tell an absent field from an empty
// one. An account with no grant genuinely has nothing to browse, which is a
// state an administrator can see and fix rather than one the interface has to
// guess at.
func rootsOf(d Deps, uid core.UserID) []sessionRoot {
	entries := d.Core.Roots(uid)
	out := make([]sessionRoot, 0, len(entries))
	for _, e := range entries {
		out = append(out, sessionRoot{
			Label:            e.Label,
			Perms:            uint16(e.Perms),
			ShareKind:        shareKindOf(e),
			SharedExternally: e.SharedExternally,
			TrashEnabled:     e.TrashEnabled,
		})
	}
	return out
}

// shareKindOf is what the interface labels a root with. A grant that cannot
// write is read-only whatever the share behind it is, because the label
// describes what this caller may do rather than how the share was configured.
func shareKindOf(e acl.RootEntry) string {
	if !e.Perms.Has(acl.Write) {
		return "ReadOnly"
	}
	return "Normal"
}

// limitsOf is what a client plans an upload against.
func limitsOf(d Deps) sessionLimits {
	out := sessionLimits{
		ChunkSize: limits.UploadChunkSizeDefault,
		ChunkMin:  limits.UploadChunkFloor,
		Parallel:  limits.UploadsInFlightPerUser,
	}
	// The engine's live values when there is one: an administrator may have
	// moved them, and a client told the compiled-in default would size its
	// chunks against a bound this server no longer applies.
	if d.Uploads != nil {
		s := d.Uploads.Settings()
		out.ChunkSize, out.ChunkMin = s.Default(), s.Min()
	}
	return out
}

// featuresOf is what this deployment actually serves, so the interface draws
// the surfaces that answer rather than the ones that were compiled in.
func featuresOf(d Deps) sessionFeatures {
	return sessionFeatures{
		WebDAV:  true,
		SMB:     d.PublishSMB != nil,
		Preview: false,
		Trash:   true,
		Shares:  true,
		Search:  searchTierOf(d),
	}
}

// searchTierOf says which tier answers a query. The walk is always there; the
// name index is the escalation, and it is off by default.
func searchTierOf(d Deps) string {
	if d.Search != nil && d.Search.HasIndex() {
		return "name"
	}
	return "walk"
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
