//go:build linux

// The auth family: getting a session, holding one, and giving it up.
//
// Signing in is two requests when the account has a second factor, and the
// server carries the accepted password between them in a signed challenge
// rather than a stored row. Nothing here decides whether a credential is good:
// that is the auth service's, and this reads its answer.
package lifecycle

import (
	"encoding/hex"
	"errors"
	"math"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/apierr"
	"github.com/heavycaffeiner/stowcloud/go/engine/http/handler"
	"github.com/heavycaffeiner/stowcloud/go/engine/http/middleware"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/limits"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/secret"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/auth"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
)

// defaultUploadParallel is how many chunks a client is told to send at once.
//
// A client-side hint, not a server limit: the server accepts what arrives and
// the rate limiter decides the rest. Four keeps a single upload from occupying
// every connection a browser will open to one origin.
const defaultUploadParallel = 4

// The AMR values a session records: what was actually presented to establish
// it. A policy asking for a second factor reads this, so a password-only
// session cannot be mistaken for one that produced a code.
const (
	amrPassword       = 1
	amrPasswordFactor = 2
)

// loginRequest is the password screen.
type loginRequest struct {
	Login    string `json:"login"`
	Password string `json:"password"`
}

// login verifies a password and either issues a session or asks for a code.
func (e *Engine) login(c *fiber.Ctx) error {
	var req loginRequest
	if err := decodeBody(c, &req); err != nil {
		return refuse(c, apierr.Classified{Class: apierr.Malformed})
	}
	if req.Login == "" || req.Password == "" {
		// Refused as a credential failure rather than as malformed input. A
		// missing field and a wrong password are the same event to anyone
		// probing, and answering differently says which one this was.
		return refuse(c, apierr.Classify(auth.ErrCredentials, apierr.VisibilityKnown))
	}

	sess, err := e.Auth.Login(c.UserContext(), auth.LoginRequest{
		Name:     req.Login,
		Password: secret.New([]byte(req.Password)),
		IP:       clientAddr(c),
		UA:       string(c.Request().Header.UserAgent()),
		AMR:      amrPassword,
	}, 0)

	if errors.Is(err, auth.ErrSecondFactor) {
		return e.askForFactor(c, req.Login)
	}
	if err != nil {
		return failKnown(c, err)
	}
	return e.grantSession(c, sess)
}

// askForFactor turns the password step's refusal into the code screen.
//
// The account is looked up again rather than carried out of the failed call,
// because Login deliberately returns nothing identifying who it refused.
func (e *Engine) askForFactor(c *fiber.Ctx, login string) error {
	uid, err := e.Auth.UserIDByName(c.UserContext(), login)
	if err != nil {
		// The password verified, so this account exists; failing to read its
		// id is an infrastructure fault, not a credential answer.
		return failKnown(c, err)
	}

	challenge, err := handler.MintChallenge(e.csrfKey(), uid, e.clock.Now().Unix())
	if err != nil {
		return failKnown(c, err)
	}

	return writeJSON(c, fiber.StatusOK, handler.ChallengeView{
		Required:         "totp",
		Challenge:        challenge,
		ExpiresInSeconds: handler.ChallengeTTL,
	})
}

// totpRequest is the code screen.
type totpRequest struct {
	Challenge string `json:"challenge"`
	Code      string `json:"code"`
}

// loginTOTP completes a sign-in whose password already verified.
func (e *Engine) loginTOTP(c *fiber.Ctx) error {
	var req totpRequest
	if err := decodeBody(c, &req); err != nil {
		return refuse(c, apierr.Classified{Class: apierr.Malformed})
	}

	uid, err := handler.OpenChallenge(e.csrfKey(), req.Challenge, e.clock.Now().Unix())
	if err != nil {
		return refuse(c, apierr.Classify(auth.ErrCredentials, apierr.VisibilityKnown))
	}
	if req.Code == "" {
		return refuse(c, apierr.Classify(auth.ErrCredentials, apierr.VisibilityKnown))
	}

	ok, err := e.acceptFactor(c, uid, req.Code)
	if err != nil {
		return failKnown(c, err)
	}
	if !ok {
		return refuse(c, apierr.Classify(auth.ErrCredentials, apierr.VisibilityKnown))
	}

	sess, err := e.Auth.CreateSession(c.UserContext(), uid,
		clientAddr(c), string(c.Request().Header.UserAgent()), amrPasswordFactor, 0)
	if err != nil {
		return failKnown(c, err)
	}
	return e.grantSession(c, sess)
}

// acceptFactor checks the code as a TOTP and then as a recovery code.
//
// TOTP first because it is the one that does not get consumed: checking the
// recovery code first would burn one every time a person mistyped six digits.
func (e *Engine) acceptFactor(c *fiber.Ctx, uid int64, code string) (bool, error) {
	accepted, err := e.Auth.VerifyTOTP(c.UserContext(), uid, code, e.clock.Nanos())
	if err != nil {
		return false, err
	}
	if accepted {
		return true, nil
	}
	return e.Auth.UseRecoveryCode(c.UserContext(), uid, code)
}

// grantSession sets the cookie and answers with the identity.
func (e *Engine) grantSession(c *fiber.Ctx, sess auth.Session) error {
	info, err := e.Auth.AccountInfo(c.UserContext(), sess.UserID)
	if err != nil {
		return failKnown(c, err)
	}
	admin, err := e.Auth.IsAdmin(c.UserContext(), sess.UserID)
	if err != nil {
		return failKnown(c, err)
	}

	printable := printableToken(sess.Token)
	e.setSessionCookie(c, printable)

	return writeJSON(c, fiber.StatusOK, handler.IdentityViewOf(
		sess.UserID, info.LoginName, info.DisplayName, admin,
		middleware.CSRFToken(e.csrfKey(), printable),
	))
}

// session reports the identity behind the credential this request carried.
func (e *Engine) session(c *fiber.Ctx) error {
	owner, ok := ownerOf(c)
	if !ok {
		return refuse(c, apierr.Classified{Class: apierr.AuthRequired})
	}

	info, err := e.Auth.AccountInfo(c.UserContext(), int64(owner))
	if err != nil {
		return failKnown(c, err)
	}
	admin, err := e.Auth.IsAdmin(c.UserContext(), int64(owner))
	if err != nil {
		return failKnown(c, err)
	}

	// The CSRF token is derived from the cookie this request presented. A
	// caller holding an app password gets an empty one, correctly: it has no
	// ambient authority to protect, and a token derived from an empty cookie
	// would be a value that validates for nobody.
	var csrf string
	if cookie := c.Cookies(middleware.SessionCookieName); cookie != "" {
		csrf = middleware.CSRFToken(e.csrfKey(), cookie)
	}

	// The account's own state, read here so a settings screen and the session
	// it was drawn from cannot disagree.
	row, err := e.Auth.UserByID(c.UserContext(), int64(owner))
	if err != nil {
		return failKnown(c, err)
	}
	smb, err := e.Auth.SMBStateOf(c.UserContext(), int64(owner))
	if err != nil {
		return failKnown(c, err)
	}

	view := handler.WhoAmIView{
		IdentityView: handler.IdentityViewOf(
			int64(owner), info.LoginName, info.DisplayName, admin, csrf,
		),
		TOTPEnabled:   row.TOTPEnabled,
		SMBOptOut:     smb.OptOut,
		SMBEnabled:    smb.Enabled,
		SMBCredential: string(smb.Credential),
		Roots:         e.rootViews(owner),
		Limits:        e.limitsView(),
		Features:      e.featuresView(),
	}
	// Carried only where there is no usable credential, which is the one case
	// it explains.
	if smb.Credential == auth.SMBCredentialNone {
		view.SMBUnavailableReason = string(smb.Reason)
	}
	return writeJSON(c, fiber.StatusOK, view)
}

// rootViews lists the folders this account can reach.
//
// Never nil: an account with no grants answers an empty list, which the
// interface reports as a deployment with no folders yet. A nil would decode as
// null and every caller iterating it would have to test the field first.
func (e *Engine) rootViews(owner core.UserID) []handler.RootView {
	roots := e.Core.Roots(owner)
	out := make([]handler.RootView, 0, len(roots))
	for _, r := range roots {
		out = append(out, handler.RootView{
			Label:            r.Label,
			Perms:            core.PermNames(r.Perms),
			SharedExternally: r.SharedExternally,
			TrashEnabled:     r.TrashEnabled,
			BrokenReason:     r.BrokenReason,
		})
	}
	return out
}

// limitsView reports what an upload may do, read from the running engine
// rather than from configuration: an operator who changed the chunk size gets
// the value in force, not the one the process started with.
func (e *Engine) limitsView() handler.LimitsView {
	if e.Upload == nil {
		// No resumable transfer on this deployment. The floor and the default
		// are still reported, because a client plans against them before it
		// discovers the route is absent.
		return handler.LimitsView{
			ChunkSize: limits.UploadChunkSizeDefault,
			ChunkMin:  limits.UploadChunkMinDefault,
			Parallel:  defaultUploadParallel,
		}
	}
	minBytes, defaultBytes := e.Upload.Settings().Snapshot()
	return handler.LimitsView{
		ChunkSize: chunkBytes(defaultBytes),
		ChunkMin:  chunkBytes(minBytes),
		Parallel:  defaultUploadParallel,
	}
}

// chunkBytes narrows a configured size for the wire. A value past the signed
// range cannot be a real chunk size, and reporting a negative one would have a
// client plan an upload against nonsense.
func chunkBytes(v uint64) int64 {
	if v > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(v)
}

// featuresView says which screens lead somewhere on this deployment.
func (e *Engine) featuresView() handler.FeaturesView {
	return handler.FeaturesView{
		// Both are surfaces this engine does not serve yet. Reported as absent
		// rather than as present-and-broken: the interface hides the screen,
		// which is honest, where drawing it would offer a route that 404s.
		WebDAV: false,
		SMB:    e.smbPublisherOf() != nil,

		Preview: e.thumbnailEnabled(),
		Trash:   true,
		Shares:  true,

		// The name tier when an index is open, the walk otherwise. Content
		// search is not a tier this engine serves, so it is never reported:
		// naming it would put a search mode in the interface that answers
		// filename matches.
		Search: searchTierName(e.Search.HasIndex()),
	}
}

// searchTierName names the tier a query would run on right now.
func searchTierName(hasIndex bool) string {
	if hasIndex {
		return "name"
	}
	return "walk"
}

// logout revokes the session and clears the cookie.
//
// The cookie is cleared only after the revoke succeeds. Clearing first and
// swallowing the failure is how a browser stops presenting a session that is
// still live in the database: the person believes they signed out, and the
// token in anything that copied the cookie keeps working.
func (e *Engine) logout(c *fiber.Ctx) error {
	cookie := c.Cookies(middleware.SessionCookieName)
	if cookie == "" {
		// An app password logging out has no session to revoke. Reporting
		// success is right: the caller asked to hold no session and holds
		// none. Its own credential is revoked through the account family.
		return c.SendStatus(fiber.StatusNoContent)
	}

	raw, err := decodeSessionCookie(cookie)
	if err != nil {
		// A cookie this server never issued cannot name a session. It is
		// cleared rather than refused, because leaving an unusable cookie in
		// place makes every later request carry a credential that fails.
		clearSessionCookie(c)
		return c.SendStatus(fiber.StatusNoContent)
	}

	err = e.Auth.RevokeSession(c.UserContext(), secret.New(raw))
	if err != nil && !errors.Is(err, auth.ErrCredentials) {
		// The session is still live and the caller must not be told it is
		// gone. ErrCredentials is different: the service is confirming there
		// is no such session, which is the state being asked for.
		return failKnown(c, err)
	}

	clearSessionCookie(c)
	return c.SendStatus(fiber.StatusNoContent)
}

// sessionCookieMaxAge bounds the cookie in the browser. Shorter than the
// server's absolute window on purpose: the browser forgetting first costs a
// sign-in, while the server forgetting first would leave a cookie presenting
// a session that no longer exists.
const sessionCookieMaxAge = 30 * 24 * time.Hour

// setSessionCookie is the one place the attributes are written, so the two
// sign-in paths cannot disagree about them.
//
// The __Host- prefix is part of the name and the browser enforces what it
// implies: Secure, Path=/, and no Domain. SameSite=Lax rather than Strict
// because Strict withholds the cookie on a top-level navigation back into the
// app, which reads as being signed out to anyone arriving from a link.
func (e *Engine) setSessionCookie(c *fiber.Ctx, printable string) {
	c.Cookie(&fiber.Cookie{
		Name:     middleware.SessionCookieName,
		Value:    printable,
		Path:     "/",
		Secure:   true,
		HTTPOnly: true,
		SameSite: fiber.CookieSameSiteLaxMode,
		Expires:  e.clock.Now().Add(sessionCookieMaxAge),
	})
}

// clearSessionCookie expires it.
//
// Every attribute except the value repeats what setSessionCookie wrote. A
// browser matches a deletion by name, path and domain, so a clear that omits
// the path leaves the original cookie in place.
func clearSessionCookie(c *fiber.Ctx) {
	c.Cookie(&fiber.Cookie{
		Name:     middleware.SessionCookieName,
		Value:    "",
		Path:     "/",
		Secure:   true,
		HTTPOnly: true,
		SameSite: fiber.CookieSameSiteLaxMode,
		Expires:  time.Unix(0, 0),
		MaxAge:   -1,
	})
}

// failKnown renders an error whose subject the caller already knows about.
//
// The auth family is where Hidden is wrong: a caller signing in named the
// account themselves, so rendering a disabled account as not-found tells them
// nothing they did not supply and hides an answer they can act on.
func failKnown(c *fiber.Ctx, err error) error {
	return refuse(c, apierr.Classify(err, apierr.VisibilityKnown))
}

// clientAddr is the address the chain resolved, as the audit log records it.
func clientAddr(c *fiber.Ctx) string {
	return middleware.ClientOf(c).String()
}

// printableToken renders a session token for the cookie.
//
// Hex rather than the secret's String, which redacts: a redacted cookie value
// is a session nobody can present. The credential step decodes the same way.
func printableToken(t secret.Secret) string {
	return hex.EncodeToString(t.Reveal())
}

// decodeSessionCookie recovers the bytes the auth store hashes.
func decodeSessionCookie(cookie string) ([]byte, error) {
	return hex.DecodeString(cookie)
}
