//go:build linux

package handler

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/heavycaffeiner/stowcloud/go/internal/feature/auth"
	"github.com/heavycaffeiner/stowcloud/go/internal/feature/shares/acl"
	"github.com/heavycaffeiner/stowcloud/go/internal/platform/clock"
	secret "github.com/heavycaffeiner/stowcloud/go/internal/platform/security/secret"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/apierr"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/middleware"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/route"
)

type AccountHandlersDeps struct {
	Service *auth.Service
	Clock   clock.Clock
}

// NewAccountHandlers builds the caller-scoped account and credential routes.
func NewAccountHandlers(d AccountHandlersDeps) map[string]gin.HandlerFunc {
	h := &accountHandlers{d: d}
	return map[string]gin.HandlerFunc{
		"account.sessions.list":              h.sessions,
		"account.sessions.delete":            h.sessionDelete,
		"account.app-passwords.list":         h.appPasswords,
		"account.app-passwords.delete":       h.appPasswordDelete,
		"account.app-passwords.create":       h.appPasswordCreate,
		"account.app-passwords.wipe":         h.appPasswordWipe,
		"account.password":                   h.password,
		"account.totp.setup":                 h.totpSetup,
		"account.totp.enroll":                h.totpEnroll,
		"account.totp.disable":               h.totpDisable,
		"account.totp.recovery-codes.list":   h.recoveryList,
		"account.totp.recovery-codes.create": h.recoveryCreate,
	}
}

type accountHandlers struct{ d AccountHandlersDeps }

type passwordRequestTransport struct {
	Current string `json:"current"`
	New     string `json:"new"`
}
type reconfirmRequestTransport struct {
	Current string `json:"current"`
}
type enrollRequestTransport struct {
	Current string `json:"current"`
	Secret  string `json:"secret"`
	Code    string `json:"code"`
}
type appPasswordRequestTransport struct {
	Current       string                     `json:"current"`
	Name          string                     `json:"name"`
	Scope         *appPasswordScopeTransport `json:"scope,omitempty"`
	ExpiresInDays int                        `json:"expires_in_days"`
}
type appPasswordScopeTransport struct {
	Perms  []string `json:"perms,omitempty"`
	Shares []string `json:"shares,omitempty"`
}

func (h *accountHandlers) password(c *gin.Context) {
	owner, ok := ownerTransport(c)
	if !ok {
		accountRefuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	var req passwordRequestTransport
	if err := accountDecode(c, &req); err != nil {
		accountRefuse(c, apierr.Classified{Class: apierr.Malformed})
		return
	}
	if !h.reconfirm(c, owner, req.Current) {
		return
	}
	if err := h.d.Service.SetPassword(c.Request.Context(), owner, secret.New([]byte(req.New))); err != nil {
		accountFail(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *accountHandlers) sessions(c *gin.Context) {
	owner, ok := ownerTransport(c)
	if !ok {
		accountRefuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	rows, err := h.d.Service.Sessions(c.Request.Context(), owner)
	if err != nil {
		accountFail(c, err)
		return
	}
	accountJSON(c, http.StatusOK, SessionsOf(rows, currentDigestTransport(c)))
}
func (h *accountHandlers) sessionDelete(c *gin.Context) {
	owner, ok := ownerTransport(c)
	if !ok {
		accountRefuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	wanted := c.Param("id")
	if wanted == "" {
		c.Status(http.StatusNotFound)
		return
	}
	rows, err := h.d.Service.Sessions(c.Request.Context(), owner)
	if err != nil {
		accountFail(c, err)
		return
	}
	for _, row := range rows {
		got := SessionHandle(row.IDHash)
		if subtle.ConstantTimeCompare([]byte(got), []byte(wanted)) != 1 {
			continue
		}
		if err := h.d.Service.RevokeSessionByHash(c.Request.Context(), owner, row.IDHash); err != nil {
			accountFail(c, err)
			return
		}
		c.Status(http.StatusNoContent)
		return
	}
	c.Status(http.StatusNotFound)
}
func (h *accountHandlers) appPasswords(c *gin.Context) {
	owner, ok := ownerTransport(c)
	if !ok {
		accountRefuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	rows, err := h.d.Service.AppPasswords(c.Request.Context(), owner)
	if err != nil {
		accountFail(c, err)
		return
	}
	accountJSON(c, http.StatusOK, AppPasswordsOf(rows))
}
func (h *accountHandlers) appPasswordDelete(c *gin.Context) {
	owner, ok := ownerTransport(c)
	if !ok {
		accountRefuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	id, ok := accountPathID(c)
	if !ok {
		c.Status(http.StatusNotFound)
		return
	}
	if err := h.d.Service.RevokeAppPassword(c.Request.Context(), owner, id); err != nil {
		accountFail(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
func (h *accountHandlers) appPasswordWipe(c *gin.Context) {
	owner, ok := ownerTransport(c)
	if !ok {
		accountRefuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	id, ok := accountPathID(c)
	if !ok {
		c.Status(http.StatusNotFound)
		return
	}
	if err := h.d.Service.RequestWipe(c.Request.Context(), owner, id); err != nil {
		accountFail(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
func (h *accountHandlers) appPasswordCreate(c *gin.Context) {
	owner, ok := ownerTransport(c)
	if !ok {
		accountRefuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	var req appPasswordRequestTransport
	if err := accountDecode(c, &req); err != nil {
		accountRefuse(c, apierr.Classified{Class: apierr.Malformed})
		return
	}
	if req.Name == "" || req.ExpiresInDays < 0 || req.ExpiresInDays > 36500 {
		accountRefuse(c, apierr.Classified{Class: apierr.Unprocessable})
		return
	}
	scope, ok := accountScope(req.Scope)
	if !ok {
		accountRefuse(c, apierr.Classified{Class: apierr.Unprocessable})
		return
	}
	if !h.reconfirm(c, owner, req.Current) {
		return
	}
	token, err := h.d.Service.CreateAppPassword(c.Request.Context(), owner, req.Name, scope, time.Duration(req.ExpiresInDays)*24*time.Hour)
	if err != nil {
		accountFail(c, err)
		return
	}
	accountJSON(c, http.StatusCreated, map[string]string{"name": req.Name, "token": token})
}
func (h *accountHandlers) totpSetup(c *gin.Context) {
	owner, ok := ownerTransport(c)
	if !ok {
		accountRefuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	var req reconfirmRequestTransport
	if err := accountDecode(c, &req); err != nil {
		accountRefuse(c, apierr.Classified{Class: apierr.Malformed})
		return
	}
	if !h.reconfirm(c, owner, req.Current) {
		return
	}
	secretB32, err := h.d.Service.GenerateTOTPSecret()
	if err != nil {
		accountFail(c, err)
		return
	}
	info, err := h.d.Service.AccountInfo(c.Request.Context(), owner)
	if err != nil {
		accountFail(c, err)
		return
	}
	accountJSON(c, http.StatusOK, TOTPSetupOf(secretB32, info.LoginName))
}
func (h *accountHandlers) totpEnroll(c *gin.Context) {
	owner, ok := ownerTransport(c)
	if !ok {
		accountRefuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	var req enrollRequestTransport
	if err := accountDecode(c, &req); err != nil {
		accountRefuse(c, apierr.Classified{Class: apierr.Malformed})
		return
	}
	if req.Secret == "" || req.Code == "" {
		accountRefuse(c, apierr.Classified{Class: apierr.Unprocessable})
		return
	}
	if !h.reconfirm(c, owner, req.Current) {
		return
	}
	accepted, err := h.d.Service.VerifyCandidateTOTP(req.Secret, req.Code, h.d.Clock.Nanos())
	if err != nil {
		accountFail(c, err)
		return
	}
	if !accepted {
		accountRefuse(c, apierr.Classify(auth.ErrCredentials, apierr.VisibilityKnown))
		return
	}
	if enrollErr := h.d.Service.EnrollTOTP(c.Request.Context(), owner, req.Secret); enrollErr != nil {
		accountFail(c, enrollErr)
		return
	}
	if _, verifyErr := h.d.Service.VerifyTOTP(c.Request.Context(), owner, req.Code, h.d.Clock.Nanos()); verifyErr != nil {
		accountFail(c, verifyErr)
		return
	}
	codes, err := h.d.Service.GenerateRecoveryCodes(c.Request.Context(), owner, 10)
	if err != nil {
		accountFail(c, err)
		return
	}
	accountJSON(c, http.StatusOK, map[string]any{"recovery_codes": codes})
}
func (h *accountHandlers) totpDisable(c *gin.Context) {
	owner, ok := ownerTransport(c)
	if !ok {
		accountRefuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	var req reconfirmRequestTransport
	if err := accountDecode(c, &req); err != nil {
		accountRefuse(c, apierr.Classified{Class: apierr.Malformed})
		return
	}
	if !h.reconfirm(c, owner, req.Current) {
		return
	}
	if err := h.d.Service.DisableTOTPWithPassword(c.Request.Context(), owner, secret.New([]byte(req.Current))); err != nil {
		accountFail(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
func (h *accountHandlers) recoveryList(c *gin.Context) {
	owner, ok := ownerTransport(c)
	if !ok {
		accountRefuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	remaining, err := h.d.Service.RecoveryCodesRemaining(c.Request.Context(), owner)
	if err != nil {
		accountFail(c, err)
		return
	}
	accountJSON(c, http.StatusOK, map[string]int{"remaining": remaining})
}
func (h *accountHandlers) recoveryCreate(c *gin.Context) {
	owner, ok := ownerTransport(c)
	if !ok {
		accountRefuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	var req reconfirmRequestTransport
	if err := accountDecode(c, &req); err != nil {
		accountRefuse(c, apierr.Classified{Class: apierr.Malformed})
		return
	}
	if !h.reconfirm(c, owner, req.Current) {
		return
	}
	codes, err := h.d.Service.GenerateRecoveryCodes(c.Request.Context(), owner, 10)
	if err != nil {
		accountFail(c, err)
		return
	}
	accountJSON(c, http.StatusOK, map[string]any{"recovery_codes": codes})
}
func (h *accountHandlers) reconfirm(c *gin.Context, owner int64, password string) bool {
	if password == "" {
		accountRefuse(c, apierr.Classify(auth.ErrCredentials, apierr.VisibilityKnown))
		return false
	}
	ok, err := h.d.Service.VerifyAccountPassword(c.Request.Context(), owner, secret.New([]byte(password)))
	if err != nil {
		accountFail(c, err)
		return false
	}
	if !ok {
		accountRefuse(c, apierr.Classify(auth.ErrCredentials, apierr.VisibilityKnown))
		return false
	}
	return true
}
func accountScope(req *appPasswordScopeTransport) (auth.Scope, bool) {
	if req == nil {
		return auth.Scope{Perms: auth.SyncScopePerms}, true
	}
	scope := auth.Scope{Perms: auth.SyncScopePerms, Shares: append([]string(nil), req.Shares...)}
	if len(req.Perms) > 0 {
		var bits acl.Perms
		for _, name := range req.Perms {
			bit, ok := acl.PermByName(name)
			if !ok {
				return auth.Scope{}, false
			}
			bits |= bit
		}
		if bits == 0 {
			return auth.Scope{}, false
		}
		scope.Perms = uint16(bits)
	}
	return scope, true
}
func currentDigestTransport(c *gin.Context) []byte {
	cookie, err := c.Cookie(middleware.SessionCookieName)
	if err != nil || cookie == "" {
		return nil
	}
	raw, err := hex.DecodeString(cookie)
	if err != nil {
		return nil
	}
	sum := sha256.Sum256(raw)
	return sum[:]
}
func accountDecode(c *gin.Context, into any) error {
	return middleware.DecodeJSON(middleware.LimitBody(c.Request.Body, route.BodyJSON), into)
}
func accountPathID(c *gin.Context) (int64, bool) {
	n, err := strconv.ParseInt(c.Param("id"), 10, 64)
	return n, err == nil && n > 0
}
func accountJSON(c *gin.Context, status int, value any) { c.JSON(status, value) }
func accountFail(c *gin.Context, err error) {
	accountRefuse(c, apierr.Classify(err, apierr.VisibilityKnown))
}
func accountRefuse(c *gin.Context, class apierr.Classified) {
	status, body := apierr.REST(class)
	accountJSON(c, status, body)
}
