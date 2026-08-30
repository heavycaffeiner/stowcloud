//go:build linux

// Credential changes an account makes to itself.
//
// Every route here is gated on the caller's own password, not merely on
// holding a session. A session is what someone who walked past an unlocked
// screen already has, and the credentials these routes create outlive the
// session that created them.
package lifecycle

import (
	"crypto/subtle"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/apierr"
	"github.com/heavycaffeiner/stowcloud/go/engine/http/handler"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/secret"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/auth"
)

// reconfirm checks the caller's own password before a sensitive change.
//
// The refusal is the credential one rather than a denial, because that is
// what it is: the account did not prove itself. It is deliberately the same
// answer a wrong password gives anywhere else.
//
// The bool is the answer and the error is the written response, in that order,
// because the obvious signature is wrong here. Returning only an error cannot
// work: refuse writes the refusal and returns nil, so `if err := reconfirm();
// err != nil` reads a failed check as a passed one and the handler carries on.
// That was the first version, and it let a session alone change the account
// password.
func (e *Engine) reconfirm(c *fiber.Ctx, owner int64, password string) (bool, error) {
	// Defensive, not load-bearing: measured, removing this check changes no
	// answer, because an empty password does not verify against a real hash
	// either. It stays so a body with the field missing does not become an
	// Argon2 computation, and because the floor is the service's to change.
	if password == "" {
		return false, refuse(c, apierr.Classify(auth.ErrCredentials, apierr.VisibilityKnown))
	}
	ok, err := e.Auth.VerifyAccountPassword(c.UserContext(), owner, secret.New([]byte(password)))
	if err != nil {
		return false, failKnown(c, err)
	}
	if !ok {
		return false, refuse(c, apierr.Classify(auth.ErrCredentials, apierr.VisibilityKnown))
	}
	return true, nil
}

// passwordRequest changes the caller's own password.
type passwordRequest struct {
	Current string `json:"current"`
	New     string `json:"new"`
}

// accountPassword sets a new password after the current one verifies.
func (e *Engine) accountPassword(c *fiber.Ctx) error {
	owner, ok := ownerOf(c)
	if !ok {
		return refuse(c, apierr.Classified{Class: apierr.AuthRequired})
	}

	var req passwordRequest
	if err := decodeBody(c, &req); err != nil {
		return refuse(c, apierr.Classified{Class: apierr.Malformed})
	}
	if proved, rerr := e.reconfirm(c, int64(owner), req.Current); !proved {
		return rerr
	}

	if err := e.Auth.SetPassword(c.UserContext(), int64(owner), secret.New([]byte(req.New))); err != nil {
		return failKnown(c, err)
	}
	return c.SendStatus(fiber.StatusNoContent)
}

// accountSessionDelete signs one of the caller's devices out.
//
// The path names a session by the handle the listing published, which is a
// second hash of the stored digest. The digest itself is what the store
// compares against, so it is resolved here from the caller's own sessions:
// a handle for somebody else's session matches nothing, which is why the
// listing is the caller's rather than every session.
func (e *Engine) accountSessionDelete(c *fiber.Ctx) error {
	owner, ok := ownerOf(c)
	if !ok {
		return refuse(c, apierr.Classified{Class: apierr.AuthRequired})
	}

	wanted := c.Params("id")
	if wanted == "" {
		return notFound(c)
	}

	rows, err := e.Auth.Sessions(c.UserContext(), int64(owner))
	if err != nil {
		return fail(c, err)
	}

	for _, row := range rows {
		// Constant time, because the handle names a session and a timing
		// difference over a prefix would let a caller find one they were not
		// shown.
		got := handler.SessionHandle(row.IDHash)
		if subtle.ConstantTimeCompare([]byte(got), []byte(wanted)) != 1 {
			continue
		}
		if rerr := e.Auth.RevokeSessionByHash(c.UserContext(), int64(owner), row.IDHash); rerr != nil {
			return fail(c, rerr)
		}
		return c.SendStatus(fiber.StatusNoContent)
	}

	// Nothing matched. Hidden, so a handle belonging to another account
	// answers exactly as one that never existed.
	return notFound(c)
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
func (e *Engine) accountAppPasswordCreate(c *fiber.Ctx) error {
	owner, ok := ownerOf(c)
	if !ok {
		return refuse(c, apierr.Classified{Class: apierr.AuthRequired})
	}

	var req appPasswordRequest
	if err := decodeBody(c, &req); err != nil {
		return refuse(c, apierr.Classified{Class: apierr.Malformed})
	}
	if req.Name == "" {
		return refuse(c, apierr.Classified{Class: apierr.Unprocessable})
	}
	if req.ExpiresInDays < 0 {
		return refuse(c, apierr.Classified{Class: apierr.Unprocessable})
	}
	scope, ok := scopeOf(req.Scope)
	if !ok {
		return refuse(c, apierr.Classified{Class: apierr.Unprocessable})
	}
	if proved, rerr := e.reconfirm(c, int64(owner), req.Current); !proved {
		return rerr
	}

	token, err := e.Auth.CreateAppPassword(c.UserContext(), int64(owner), req.Name,
		scope, time.Duration(req.ExpiresInDays)*24*time.Hour)
	if err != nil {
		return failKnown(c, err)
	}

	// The one time this value is ever readable. It is not stored, so a
	// response that is lost cannot be recovered and the person mints another.
	return writeJSON(c, fiber.StatusCreated, map[string]string{
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
func (e *Engine) accountAppPasswordWipe(c *fiber.Ctx) error {
	owner, ok := ownerOf(c)
	if !ok {
		return refuse(c, apierr.Classified{Class: apierr.AuthRequired})
	}
	id, ok := pathID(c)
	if !ok {
		return notFound(c)
	}

	if err := e.Auth.RequestWipe(c.UserContext(), int64(owner), id); err != nil {
		return fail(c, err)
	}
	return c.SendStatus(fiber.StatusNoContent)
}

// accountTOTPSetup hands back a secret to enrol, without enrolling it.
//
// Two steps on purpose: a person has to prove the authenticator actually
// works before the factor starts gating their sign-ins. Enrolling here would
// lock out anyone who scanned the code into an app that then failed.
func (e *Engine) accountTOTPSetup(c *fiber.Ctx) error {
	owner, ok := ownerOf(c)
	if !ok {
		return refuse(c, apierr.Classified{Class: apierr.AuthRequired})
	}

	var req reconfirmRequest
	if err := decodeBody(c, &req); err != nil {
		return refuse(c, apierr.Classified{Class: apierr.Malformed})
	}
	if proved, rerr := e.reconfirm(c, int64(owner), req.Current); !proved {
		return rerr
	}

	secretB32, err := e.Auth.GenerateTOTPSecret()
	if err != nil {
		return failKnown(c, err)
	}

	info, err := e.Auth.AccountInfo(c.UserContext(), int64(owner))
	if err != nil {
		return failKnown(c, err)
	}
	return writeJSON(c, fiber.StatusOK, handler.TOTPSetupOf(secretB32, info.LoginName))
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
func (e *Engine) accountTOTPEnroll(c *fiber.Ctx) error {
	owner, ok := ownerOf(c)
	if !ok {
		return refuse(c, apierr.Classified{Class: apierr.AuthRequired})
	}

	var req enrollRequest
	if err := decodeBody(c, &req); err != nil {
		return refuse(c, apierr.Classified{Class: apierr.Malformed})
	}
	if req.Secret == "" || req.Code == "" {
		return refuse(c, apierr.Classified{Class: apierr.Unprocessable})
	}
	if proved, rerr := e.reconfirm(c, int64(owner), req.Current); !proved {
		return rerr
	}

	// The secret is enrolled first and checked second, because verification
	// reads what is stored. A code that does not verify undoes the enrolment,
	// so the account is never left holding a factor it cannot produce.
	if err := e.Auth.EnrollTOTP(c.UserContext(), int64(owner), req.Secret); err != nil {
		return failKnown(c, err)
	}

	accepted, err := e.Auth.VerifyTOTP(c.UserContext(), int64(owner), req.Code, e.clock.Nanos())
	if err != nil {
		return failKnown(c, err)
	}
	if !accepted {
		if derr := e.Auth.DisableTOTP(c.UserContext(), int64(owner)); derr != nil {
			// The enrolment stands and the code did not verify, which is the
			// lockout this ordering exists to prevent. Reported rather than
			// hidden behind the code refusal.
			return failKnown(c, derr)
		}
		return refuse(c, apierr.Classify(auth.ErrCredentials, apierr.VisibilityKnown))
	}

	codes, err := e.Auth.GenerateRecoveryCodes(c.UserContext(), int64(owner), recoveryCodeCount)
	if err != nil {
		return failKnown(c, err)
	}
	return writeJSON(c, fiber.StatusOK, map[string]any{"recovery_codes": codes})
}

// recoveryCodeCount is what an enrolment issues. Enough that losing a few does
// not matter, few enough that a person will actually store them.
const recoveryCodeCount = 10

// accountTOTPDisable turns the factor off.
func (e *Engine) accountTOTPDisable(c *fiber.Ctx) error {
	owner, ok := ownerOf(c)
	if !ok {
		return refuse(c, apierr.Classified{Class: apierr.AuthRequired})
	}

	var req reconfirmRequest
	if err := decodeBody(c, &req); err != nil {
		return refuse(c, apierr.Classified{Class: apierr.Malformed})
	}
	if proved, rerr := e.reconfirm(c, int64(owner), req.Current); !proved {
		return rerr
	}

	if err := e.Auth.DisableTOTP(c.UserContext(), int64(owner)); err != nil {
		return failKnown(c, err)
	}
	return c.SendStatus(fiber.StatusNoContent)
}

// accountRecoveryCodesList reports how many codes are left, never the codes.
//
// They are stored hashed, so no listing could return them. What a person needs
// from this screen is whether they are running out.
func (e *Engine) accountRecoveryCodesList(c *fiber.Ctx) error {
	owner, ok := ownerOf(c)
	if !ok {
		return refuse(c, apierr.Classified{Class: apierr.AuthRequired})
	}

	remaining, err := e.Auth.RecoveryCodesRemaining(c.UserContext(), int64(owner))
	if err != nil {
		return failKnown(c, err)
	}
	return writeJSON(c, fiber.StatusOK, map[string]int{"remaining": remaining})
}

// accountRecoveryCodesCreate replaces the whole set.
//
// Replacing rather than adding: a person asking for new codes has lost the old
// ones, and leaving those live would keep whatever found them working.
func (e *Engine) accountRecoveryCodesCreate(c *fiber.Ctx) error {
	owner, ok := ownerOf(c)
	if !ok {
		return refuse(c, apierr.Classified{Class: apierr.AuthRequired})
	}

	var req reconfirmRequest
	if err := decodeBody(c, &req); err != nil {
		return refuse(c, apierr.Classified{Class: apierr.Malformed})
	}
	if proved, rerr := e.reconfirm(c, int64(owner), req.Current); !proved {
		return rerr
	}

	codes, err := e.Auth.GenerateRecoveryCodes(c.UserContext(), int64(owner), recoveryCodeCount)
	if err != nil {
		return failKnown(c, err)
	}
	return writeJSON(c, fiber.StatusOK, map[string]any{"recovery_codes": codes})
}
