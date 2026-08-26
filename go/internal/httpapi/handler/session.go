// Linux only: it depends on packages that are Linux only.
//go:build linux

package handler

import (
	"context"
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
	// What actually works over SMB right now, not which row exists: the
	// deployment's second-factor policy is folded in server-side, because the
	// two can disagree and a line the user can only disprove by failing to
	// connect is worse than none.
	SMBCredential string `json:"smb_credential,omitempty"`
	// Present only with "none".
	SMBUnavailableReason string `json:"smb_unavailable_reason,omitempty"`
}

// sessionRoot is one readable share, labeled the way the caller's own grant
// labels it.
type sessionRoot struct {
	Label string `json:"label"`
	// The eight named booleans, like every other perms object this surface
	// sends. It was a bitmask here alone, which the client declares as the
	// object and would have read as eight undefined fields.
	Perms            permsJSON `json:"perms"`
	ShareKind        string    `json:"share_kind"`
	SharedExternally bool      `json:"shared_externally"`
	TrashEnabled     bool      `json:"trash_enabled"`
	// BrokenReason is why this folder cannot be opened right now, or absent
	// when it can. The folder stays in the list either way: a share whose disk
	// did not come back used to vanish, which reads as somebody having deleted
	// it rather than as hardware that needs looking at.
	BrokenReason string `json:"broken_reason,omitempty"`
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

		// What the SMB section reads. A failure here is not a failed session:
		// the rest of the response is what the interface needs to draw itself,
		// and one absent line beats an error page.
		var smb auth.SMBState
		if st, serr := d.Auth.SMBStateOf(r.Context(), int64(uid)); serr == nil {
			smb = st
		}
		resp := sessionResponse{
			User: sessionUser{
				ID:          int64(uid),
				Name:        row.Name,
				DisplayName: row.Display,
				IsAdmin:     admin,
				TOTPEnabled: row.TOTPEnabled,
				SMBOptOut:   smb.OptOut,
				SMBEnabled:  row.SMBEnabled,
				// Empty is a build that could not read the state, which the
				// client treats as "unknown" rather than as "unavailable".
				SMBCredential:        string(smb.Credential),
				SMBUnavailableReason: string(smb.Reason),
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
			Perms:            permsOf(e.Perms),
			ShareKind:        shareKindOf(e),
			SharedExternally: e.SharedExternally,
			TrashEnabled:     e.TrashEnabled,
			BrokenReason:     e.BrokenReason,
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
			// The caller before the session is revoked, because afterwards
			// there is nobody to attribute the row to.
			if uid, uerr := userOf(r); uerr == nil {
				record(r, d, int64(uid), "logout", "", true)
			}
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
//
// Scope is a nested object with named permission booleans, not a bitmask: the
// client works in the same eight named booleans everywhere else on this
// surface, and a bitmask on one route was decoded as zero, which minted tokens
// that could reach nothing.
type appPasswordRequest struct {
	Name  string `json:"name"`
	Scope *struct {
		Perms  permsJSON `json:"perms"`
		Shares []string  `json:"shares,omitempty"`
	} `json:"scope,omitempty"`
}

type appPasswordResponse struct {
	ID         int64   `json:"id"`
	Name       string  `json:"name"`
	Token      string  `json:"token,omitempty"`
	CreatedNs  string  `json:"created_ns"`
	LastUsedNs *string `json:"last_used_ns"`
	ExpiresNs  *string `json:"expires_ns"`
	ReadOnly   bool    `json:"read_only"`
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
				out = append(out, appPasswordResponse{
					ID: row.ID, Name: row.Name,
					CreatedNs:  strconv.FormatInt(row.CreatedNs, 10),
					LastUsedNs: nsString(row.LastUsedNs),
					ExpiresNs:  nsString(row.ExpiresNs),
					ReadOnly:   scopeIsReadOnly(row.ScopePerms),
				})
			}
			return writeJSON(w, http.StatusOK, out)
		case http.MethodPost:
			var req appPasswordRequest
			if err := decodeJSON(r, &req); err != nil {
				return err
			}
			if req.Name == "" {
				return apierr.BadRequest("auth.apppw_name", "name")
			}
			// No scope object means the whole account, which is the sentinel
			// the gate reads as unrestricted. A scope object with no permission
			// set would mint a token that can reach nothing, so it is refused
			// rather than stored.
			scope := auth.Scope{Perms: mw.ScopeFull}
			if req.Scope != nil {
				perms := uint16(permsFrom(req.Scope.Perms))
				if perms == 0 {
					return apierr.Unprocessable("auth.apppw_scope_empty", "scope.perms")
				}
				shares, serr := knownShareLabels(d, uid, req.Scope.Shares)
				if serr != nil {
					return serr
				}
				scope = auth.Scope{Perms: perms, Shares: shares}
			}
			token, err := d.Auth.CreateAppPassword(r.Context(), int64(uid), req.Name, scope, 0)
			if err != nil {
				return err
			}
			// Read back so the id is the stored one. The screen shows the token
			// once and then lists rows by id, and a zero id collides.
			id, lerr := latestAppPasswordID(r.Context(), d, int64(uid), req.Name)
			if lerr != nil {
				return lerr
			}
			return writeJSON(w, http.StatusCreated, appPasswordResponse{
				ID: id, Name: req.Name, Token: token,
				CreatedNs: strconv.FormatInt(d.Clock.Nanos(), 10),
				ReadOnly:  scopeIsReadOnly(scope.Perms),
			})
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

// scopeIsReadOnly reports whether a scope can look but not change anything.
func scopeIsReadOnly(perms uint16) bool {
	if perms == mw.ScopeFull {
		return false
	}
	const writing = uint16(acl.Write | acl.Create | acl.Delete | acl.Rename | acl.Move | acl.Share)
	return perms&writing == 0
}

// knownShareLabels refuses a scope naming a root the caller cannot reach. A
// label silently dropped would mint a token broader or narrower than the one
// that was asked for, and neither is what the screen showed.
func knownShareLabels(d Deps, uid core.UserID, want []string) ([]string, error) {
	if len(want) == 0 {
		return nil, nil
	}
	roots := d.Core.Roots(uid)
	have := make(map[string]struct{}, len(roots))
	for _, root := range roots {
		have[root.Label] = struct{}{}
	}
	for _, label := range want {
		if _, ok := have[label]; !ok {
			return nil, &apierr.RequestError{
				Status: http.StatusUnprocessableEntity, Code: apierr.CodeInvalidRequest,
				Message: "no such share", Key: "auth.unknown_share",
				Args: []apierr.Arg{{Name: "label", Value: label}},
			}
		}
	}
	return want, nil
}

// latestAppPasswordID finds the row just minted. The mint returns the token
// only, and the list is newest first, so the first row carrying the name is
// the one this request created.
func latestAppPasswordID(ctx context.Context, d Deps, uid int64, name string) (int64, error) {
	rows, err := d.Auth.AppPasswords(ctx, uid)
	if err != nil {
		return 0, err
	}
	for _, row := range rows {
		if row.Name == name {
			return row.ID, nil
		}
	}
	return 0, nil
}

// The session-management surface: list the caller's devices and sign one out.
//
// Field names are the client's, and every nanosecond stamp is a string: the
// screen keys rows on id_hash and renders three dates, and a number here
// rounds them.
type sessionRow struct {
	IDHash     string `json:"id_hash"`
	CreatedNs  string `json:"created_ns"`
	LastSeenNs string `json:"last_seen_ns"`
	AbsoluteNs string `json:"absolute_expiry_ns"`
	IPFirst    string `json:"ip_first,omitempty"`
	UAFirst    string `json:"ua_first,omitempty"`
	Current    bool   `json:"current"`
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
					IDHash:     hex.EncodeToString(row.IDHash),
					CreatedNs:  strconv.FormatInt(row.CreatedNs, 10),
					LastSeenNs: strconv.FormatInt(row.LastSeenNs, 10),
					AbsoluteNs: strconv.FormatInt(row.AbsoluteNs, 10),
					IPFirst:    row.IP,
					UAFirst:    row.UA,
					Current:    string(row.IDHash) == string(current),
				}
				out = append(out, item)
			}
			return writeJSON(w, http.StatusOK, out)
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
