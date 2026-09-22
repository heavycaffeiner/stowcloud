//go:build linux

// Credential changes an account makes to itself.
//
// Every route here is gated on the caller's own password, not merely on
// holding a session. A session is what someone who walked past an unlocked
// screen already has, and the credentials these routes create outlive the
// session that created them.
package app

import (
	"crypto/subtle"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/heavycaffeiner/stowcloud/go/internal/feature/auth"
	"github.com/heavycaffeiner/stowcloud/go/internal/kit/secret"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/apierr"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/handler"
)

// reconfirm checks the caller's own password before a sensitive change.
//
// The refusal is the credential one rather than a denial, because that is
// what it is: the account did not prove itself. It is deliberately the same
// answer a wrong password gives anywhere else.
//
// The bool reports whether verification succeeded. A refusal is written
// directly to the Gin response before false is returned.
func (e *Engine) reconfirm(c *gin.Context, owner int64, password string) bool {
	if password == "" {
		refuse(c, apierr.Classify(auth.ErrCredentials, apierr.VisibilityKnown))
		return false
	}
	ok, err := e.Auth.VerifyAccountPassword(c.Request.Context(), owner, secret.New([]byte(password)))
	if err != nil {
		failKnown(c, err)
		return false
	}
	if !ok {
		refuse(c, apierr.Classify(auth.ErrCredentials, apierr.VisibilityKnown))
		return false
	}
	return true
}

// passwordRequest changes the caller's own password.
type passwordRequest struct {
	Current string `json:"current"`
	New     string `json:"new"`
}

// accountPassword sets a new password after the current one verifies.
func (e *Engine) accountPassword(c *gin.Context) {
	owner, ok := ownerOf(c)
	if !ok {
		refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}

	var req passwordRequest
	if err := decodeBody(c, &req); err != nil {
		refuse(c, apierr.Classified{Class: apierr.Malformed})
		return
	}
	if !e.reconfirm(c, int64(owner), req.Current) {
		return
	}

	if err := e.Auth.SetPassword(c.Request.Context(), int64(owner), secret.New([]byte(req.New))); err != nil {
		failKnown(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// accountSessionDelete signs one of the caller's devices out.
//
// The path names a session by the handle the listing published, which is a
// second hash of the stored digest. The digest itself is what the store
// compares against, so it is resolved here from the caller's own sessions:
// a handle for somebody else's session matches nothing, which is why the
// listing is the caller's rather than every session.
func (e *Engine) accountSessionDelete(c *gin.Context) {
	owner, ok := ownerOf(c)
	if !ok {
		refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}

	wanted := c.Param("id")
	if wanted == "" {
		notFound(c)
		return
	}

	rows, err := e.Auth.Sessions(c.Request.Context(), int64(owner))
	if err != nil {
		fail(c, err)
		return
	}

	for _, row := range rows {
		// Constant time, because the handle names a session and a timing
		// difference over a prefix would let a caller find one they were not
		// shown.
		got := handler.SessionHandle(row.IDHash)
		if subtle.ConstantTimeCompare([]byte(got), []byte(wanted)) != 1 {
			continue
		}
		if rerr := e.Auth.RevokeSessionByHash(c.Request.Context(), int64(owner), row.IDHash); rerr != nil {
			fail(c, rerr)
			return
		}
		c.Status(http.StatusNoContent)
		return
	}

	// Nothing matched. Hidden, so a handle belonging to another account
	// answers exactly as one that never existed.
	notFound(c)
}

// appPasswordRequest mints a credential for one device.
type appPasswordRequest struct {
	Current string `json:"current"`
	Name    string `json:"name"`

	// Scope narrows what the credential may do. Absent means the whole
	// filesystem surface, which is what a sync client needs.
	//
	// It is settable because the narrow case is the useful one: a backup tool
	// that only ever reads should hold a credential that only ever reads, so a
	// copy of it taken off a machine cannot write anything.
	Scope *appPasswordScope `json:"scope,omitempty"`

	// ExpiresInDays is optional. Zero means no silent expiry, which is right
	// for a device credential: one that stops working on its own looks like a
	// broken client rather than a policy.
	ExpiresInDays int `json:"expires_in_days"`
}

// appPasswordScope is the narrowing a caller may ask for.
type appPasswordScope struct {
	// Perms are permission names. Empty leaves the permission set unnarrowed.
	Perms []string `json:"perms,omitempty"`

	// Shares are root labels, the strings the session reports. A label is what
	// a person actually sees, and an unknown one is refused rather than
	// dropped: a credential that quietly ends up broader than asked for is the
	// failure worth preventing.
	Shares []string `json:"shares,omitempty"`
}

// scopeOf reads the requested narrowing.
//
// No scope is the whole surface. A scope naming no permission at all is
// refused rather than treated as absent: a credential that can reach nothing
// is not a restriction anybody asked for, and reading it as "unrestricted"
// would turn a typo into the opposite of what was meant.
func scopeOf(req *appPasswordScope) (auth.Scope, bool) {
	if req == nil {
		return auth.Scope{Perms: auth.SyncScopePerms}, true
	}
	scope := auth.Scope{Perms: auth.SyncScopePerms, Shares: req.Shares}
	if len(req.Perms) > 0 {
		perms, ok := permsOf(req.Perms)
		if !ok || perms == 0 {
			return auth.Scope{}, false
		}
		scope.Perms = uint16(perms)
	}
	return scope, true
}

// accountAppPasswordCreate mints one and returns it once.
func (e *Engine) accountAppPasswordCreate(c *gin.Context) {
	owner, ok := ownerOf(c)
	if !ok {
		refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}

	var req appPasswordRequest
	if err := decodeBody(c, &req); err != nil {
		refuse(c, apierr.Classified{Class: apierr.Malformed})
		return
	}
	if req.Name == "" {
		refuse(c, apierr.Classified{Class: apierr.Unprocessable})
		return
	}
	const maxAppPasswordExpiryDays = 36500
	if req.ExpiresInDays < 0 || req.ExpiresInDays > maxAppPasswordExpiryDays {
		refuse(c, apierr.Classified{Class: apierr.Unprocessable})
		return
	}
	scope, ok := scopeOf(req.Scope)
	if !ok {
		refuse(c, apierr.Classified{Class: apierr.Unprocessable})
		return
	}
	if !e.reconfirm(c, int64(owner), req.Current) {
		return
	}

	token, err := e.Auth.CreateAppPassword(c.Request.Context(), int64(owner), req.Name,
		scope, time.Duration(req.ExpiresInDays)*24*time.Hour)
	if err != nil {
		failKnown(c, err)
		return
	}

	// The one time this value is ever readable. It is not stored, so a
	// response that is lost cannot be recovered and the person mints another.
	writeJSON(c, http.StatusCreated, map[string]string{
		"name":  req.Name,
		"token": token,
	})
}

// accountAppPasswordWipe asks a device to erase what it holds.
//
// This is a request, not a revocation: the device acts on it when it next
// connects. Revoking the credential outright is the delete route, and doing
// both here would remove the credential the device has to present in order to
// ever learn it was asked.
func (e *Engine) accountAppPasswordWipe(c *gin.Context) {
	owner, ok := ownerOf(c)
	if !ok {
		refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	id, ok := pathID(c)
	if !ok {
		notFound(c)
		return
	}

	if err := e.Auth.RequestWipe(c.Request.Context(), int64(owner), id); err != nil {
		fail(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// accountTOTPSetup hands back a secret to enrol, without enrolling it.
//
// Two steps on purpose: a person has to prove the authenticator actually
// works before the factor starts gating their sign-ins. Enrolling here would
// lock out anyone who scanned the code into an app that then failed.
func (e *Engine) accountTOTPSetup(c *gin.Context) {
	owner, ok := ownerOf(c)
	if !ok {
		refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}

	var req reconfirmRequest
	if err := decodeBody(c, &req); err != nil {
		refuse(c, apierr.Classified{Class: apierr.Malformed})
		return
	}
	if !e.reconfirm(c, int64(owner), req.Current) {
		return
	}

	secretB32, err := e.Auth.GenerateTOTPSecret()
	if err != nil {
		failKnown(c, err)
		return
	}

	info, err := e.Auth.AccountInfo(c.Request.Context(), int64(owner))
	if err != nil {
		failKnown(c, err)
		return
	}
	writeJSON(c, http.StatusOK, handler.TOTPSetupOf(secretB32, info.LoginName))
}

// reconfirmRequest is a body that carries nothing but the proof.
type reconfirmRequest struct {
	Current string `json:"current"`
}

// enrollRequest completes an enrolment by proving the authenticator works.
type enrollRequest struct {
	Current string `json:"current"`
	Secret  string `json:"secret"`
	Code    string `json:"code"`
}

// accountTOTPEnroll turns the factor on once a code from it verifies.
func (e *Engine) accountTOTPEnroll(c *gin.Context) {
	owner, ok := ownerOf(c)
	if !ok {
		refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}

	var req enrollRequest
	if err := decodeBody(c, &req); err != nil {
		refuse(c, apierr.Classified{Class: apierr.Malformed})
		return
	}
	if req.Secret == "" || req.Code == "" {
		refuse(c, apierr.Classified{Class: apierr.Unprocessable})
		return
	}
	if !e.reconfirm(c, int64(owner), req.Current) {
		return
	}

	// Verify the candidate code against the candidate secret first, so an
	// invalid code never touches or disables an existing enrolled factor.
	accepted, err := e.Auth.VerifyCandidateTOTP(req.Secret, req.Code, e.clock.Nanos())
	if err != nil {
		failKnown(c, err)
		return
	}
	if !accepted {
		refuse(c, apierr.Classify(auth.ErrCredentials, apierr.VisibilityKnown))
		return
	}

	if eerr := e.Auth.EnrollTOTP(c.Request.Context(), int64(owner), req.Secret); eerr != nil {
		failKnown(c, eerr)
		return
	}
	if _, verr := e.Auth.VerifyTOTP(c.Request.Context(), int64(owner), req.Code, e.clock.Nanos()); verr != nil {
		failKnown(c, verr)
		return
	}

	codes, err := e.Auth.GenerateRecoveryCodes(c.Request.Context(), int64(owner), recoveryCodeCount)
	if err != nil {
		failKnown(c, err)
		return
	}
	writeJSON(c, http.StatusOK, map[string]any{"recovery_codes": codes})
}

// recoveryCodeCount is what an enrolment issues. Enough that losing a few does
// not matter, few enough that a person will actually store them.
const recoveryCodeCount = 10

// accountTOTPDisable turns the factor off.
func (e *Engine) accountTOTPDisable(c *gin.Context) {
	owner, ok := ownerOf(c)
	if !ok {
		refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}

	var req reconfirmRequest
	if err := decodeBody(c, &req); err != nil {
		refuse(c, apierr.Classified{Class: apierr.Malformed})
		return
	}
	if !e.reconfirm(c, int64(owner), req.Current) {
		return
	}

	if err := e.Auth.DisableTOTPWithPassword(c.Request.Context(), int64(owner), secret.New([]byte(req.Current))); err != nil {
		failKnown(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// accountRecoveryCodesList reports how many codes are left, never the codes.
//
// They are stored hashed, so no listing could return them. What a person needs
// from this screen is whether they are running out.
func (e *Engine) accountRecoveryCodesList(c *gin.Context) {
	owner, ok := ownerOf(c)
	if !ok {
		refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}

	remaining, err := e.Auth.RecoveryCodesRemaining(c.Request.Context(), int64(owner))
	if err != nil {
		failKnown(c, err)
		return
	}
	writeJSON(c, http.StatusOK, map[string]int{"remaining": remaining})
}

// accountRecoveryCodesCreate replaces the whole set.
//
// Replacing rather than adding: a person asking for new codes has lost the old
// ones, and leaving those live would keep whatever found them working.
func (e *Engine) accountRecoveryCodesCreate(c *gin.Context) {
	owner, ok := ownerOf(c)
	if !ok {
		refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}

	var req reconfirmRequest
	if err := decodeBody(c, &req); err != nil {
		refuse(c, apierr.Classified{Class: apierr.Malformed})
		return
	}
	if !e.reconfirm(c, int64(owner), req.Current) {
		return
	}

	codes, err := e.Auth.GenerateRecoveryCodes(c.Request.Context(), int64(owner), recoveryCodeCount)
	if err != nil {
		failKnown(c, err)
		return
	}
	writeJSON(c, http.StatusOK, map[string]any{"recovery_codes": codes})
}
