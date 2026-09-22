//go:build linux

// The auth family: getting a session, holding one, and giving it up.
//
// Signing in is two requests when the account has a second factor, and the
// server carries the accepted password between them in a signed challenge
// rather than a stored row. Nothing here decides whether a credential is good:
// that is the auth service's, and this reads its answer.
package app

import (
	"encoding/hex"
	"errors"
	"math"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/heavycaffeiner/stowcloud/go/internal/feature/auth"
	"github.com/heavycaffeiner/stowcloud/go/internal/feature/files"
	"github.com/heavycaffeiner/stowcloud/go/internal/kit/limits"
	"github.com/heavycaffeiner/stowcloud/go/internal/kit/secret"
	"github.com/heavycaffeiner/stowcloud/go/internal/platform/storage/objstore"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/apierr"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/handler"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/middleware"
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

// decodeAuthBody accepts only the JSON media type used by the browser auth forms.
func decodeAuthBody(c *gin.Context, into any) error {
	media, _, err := mime.ParseMediaType(c.GetHeader("Content-Type"))
	if err != nil || !strings.EqualFold(media, "application/json") {
		return errors.New("the authentication body is not JSON")
	}
	return decodeBody(c, into)
}

// login verifies a password and either issues a session or asks for a code.
func (e *Engine) login(c *gin.Context) {
	var req loginRequest
	if err := decodeAuthBody(c, &req); err != nil {
		refuse(c, apierr.Classified{Class: apierr.Malformed})
		return
	}
	if req.Login == "" || req.Password == "" {
		// Refused as a credential failure rather than as malformed input. A
		// missing field and a wrong password are the same event to anyone
		// probing, and answering differently says which one this was.
		refuse(c, apierr.Classify(auth.ErrCredentials, apierr.VisibilityKnown))
		return
	}

	sess, err := e.Auth.Login(c.Request.Context(), auth.LoginRequest{
		Name:     req.Login,
		Password: secret.New([]byte(req.Password)),
		IP:       clientAddr(c),
		UA:       string(c.Request.UserAgent()),
		AMR:      amrPassword,
	}, 0)

	if errors.Is(err, auth.ErrSecondFactor) {
		e.askForFactor(c, req.Login)
		return
	}
	if err != nil {
		failKnown(c, err)
		return
	}
	e.grantSession(c, sess)
}

// askForFactor turns the password step's refusal into the code screen.
//
// The account is looked up again rather than carried out of the failed call,
// because Login deliberately returns nothing identifying who it refused.
func (e *Engine) askForFactor(c *gin.Context, login string) {
	uid, err := e.Auth.UserIDByName(c.Request.Context(), login)
	if err != nil {
		// The password verified, so this account exists; failing to read its
		// id is an infrastructure fault, not a credential answer.
		failKnown(c, err)
		return
	}

	challenge, err := handler.MintChallenge(e.csrfKey(), uid, e.clock.Now().Unix())
	if err != nil {
		failKnown(c, err)
		return
	}

	writeJSON(c, http.StatusOK, handler.ChallengeView{
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
func (e *Engine) loginTOTP(c *gin.Context) {
	var req totpRequest
	if err := decodeAuthBody(c, &req); err != nil {
		refuse(c, apierr.Classified{Class: apierr.Malformed})
		return
	}

	uid, err := handler.OpenChallenge(e.csrfKey(), req.Challenge, e.clock.Now().Unix())
	if err != nil {
		refuse(c, apierr.Classify(auth.ErrCredentials, apierr.VisibilityKnown))
		return
	}
	if req.Code == "" {
		refuse(c, apierr.Classify(auth.ErrCredentials, apierr.VisibilityKnown))
		return
	}

	totpKey := clientAddr(c) + "/" + strconv.FormatInt(uid, 10)
	if e.totpLimiter != nil && !e.totpLimiter.Allow(totpKey) {
		refuse(c, apierr.Classified{Class: apierr.RateLimited, Key: "auth.rate_limited"})
		return
	}
	ok, err := e.acceptFactor(c, uid, req.Code)
	if err != nil {
		failKnown(c, err)
		return
	}
	if !ok {
		refuse(c, apierr.Classify(auth.ErrCredentials, apierr.VisibilityKnown))
		return
	}

	sess, err := e.Auth.CreateSession(c.Request.Context(), uid,
		clientAddr(c), string(c.Request.UserAgent()), amrPasswordFactor, 0)
	if err != nil {
		failKnown(c, err)
		return
	}
	e.grantSession(c, sess)
}

// acceptFactor checks the code as a TOTP and then as a recovery code.
//
// TOTP first because it is the one that does not get consumed: checking the
// recovery code first would burn one every time a person mistyped six digits.
func (e *Engine) acceptFactor(c *gin.Context, uid int64, code string) (bool, error) {
	accepted, err := e.Auth.VerifyTOTP(c.Request.Context(), uid, code, e.clock.Nanos())
	if err != nil {
		return false, err
	}
	if accepted {
		return true, nil
	}
	return e.Auth.UseRecoveryCode(c.Request.Context(), uid, code)
}

// grantSession sets the cookie and answers with the identity.
func (e *Engine) grantSession(c *gin.Context, sess auth.Session) {
	info, err := e.Auth.AccountInfo(c.Request.Context(), sess.UserID)
	if err != nil {
		failKnown(c, err)
		return
	}
	admin, err := e.Auth.IsAdmin(c.Request.Context(), sess.UserID)
	if err != nil {
		failKnown(c, err)
		return
	}

	printable := printableToken(sess.Token)
	e.setSessionCookie(c, printable)

	writeJSON(c, http.StatusOK, handler.IdentityViewOf(
		sess.UserID, info.LoginName, info.DisplayName, admin,
		middleware.CSRFToken(e.csrfKey(), printable),
	))
}

// session reports the identity behind the credential this request carried.
func (e *Engine) session(c *gin.Context) {
	owner, ok := ownerOf(c)
	if !ok {
		refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}

	info, err := e.Auth.AccountInfo(c.Request.Context(), int64(owner))
	if err != nil {
		failKnown(c, err)
		return
	}
	admin, err := e.Auth.IsAdmin(c.Request.Context(), int64(owner))
	if err != nil {
		failKnown(c, err)
		return
	}

	// The CSRF token is derived from the cookie this request presented. A
	// caller holding an app password gets an empty one, correctly: it has no
	// ambient authority to protect, and a token derived from an empty cookie
	// would be a value that validates for nobody.
	var csrf string
	if cookie, cookieErr := c.Cookie(middleware.SessionCookieName); cookieErr == nil && cookie != "" {
		csrf = middleware.CSRFToken(e.csrfKey(), cookie)
	}

	// The account's own state, read here so a settings screen and the session
	// it was drawn from cannot disagree.
	row, err := e.Auth.UserByID(c.Request.Context(), int64(owner))
	if err != nil {
		failKnown(c, err)
		return
	}
	smb, err := e.Auth.SMBStateOf(c.Request.Context(), int64(owner))
	if err != nil {
		failKnown(c, err)
		return
	}

	// Absent is the ordinary case, not a failure: most accounts hold no
	// provider link, and the view still reports {"linked": false} for one
	// rather than an empty struct silently reading as unlinked either way.
	oidcView := handler.SessionOidcView{}
	switch link, lerr := e.Auth.OIDCLinkOf(c.Request.Context(), int64(owner)); {
	case lerr == nil:
		oidcView = handler.SessionOidcOf(link)
	case !errors.Is(lerr, auth.ErrNoOIDCLink):
		failKnown(c, lerr)
		return
	}

	view := handler.WhoAmIView{
		IdentityView: handler.IdentityViewOf(
			int64(owner), info.LoginName, info.DisplayName, admin, csrf,
		),
		TOTPEnabled:   row.TOTPEnabled,
		SMBOptOut:     smb.OptOut,
		SMBEnabled:    smb.Enabled,
		SMBCredential: string(smb.Credential),
		Oidc:          oidcView,
		Roots:         e.rootViews(owner),
		Limits:        e.limitsView(),
		Features:      e.featuresView(),
	}
	// Carried only where there is no usable credential, which is the one case
	// it explains.
	if smb.Credential == auth.SMBCredentialNone {
		view.SMBUnavailableReason = string(smb.Reason)
	}
	writeJSON(c, http.StatusOK, view)
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

func (e *Engine) featuresView() handler.FeaturesView {
	directUploads := false
	for _, share := range e.Core.Shares() {
		if share.BrokenReason != "" || share.Backend != core.BackendS3 {
			continue
		}
		if root, ok := e.Core.ShareRoot(share.ID); ok {
			if provider, ok := root.(objstore.DirectTransferProvider); ok && provider.DirectTransfer() {
				directUploads = true
				break
			}
		}
	}
	return handler.FeaturesView{
		WebDAV: true, SMB: e.smbPublisherOf() != nil, Preview: e.thumbnailEnabled(),
		Trash: true, Shares: true, Search: searchTierName(e.Search.HasIndex()), DirectUploads: directUploads,
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
//
// A session the provider established gets one thing more: the answer names
// the provider's own end-session URL, so the client can send the browser
// there next and end the provider's session too. Without this the provider
// session outlives the local one and the next sign-in is silent, no consent
// screen and no password prompt, because the browser is still authenticated
// to the provider even though this server has forgotten it. A session
// established locally answers exactly as before: no provider was ever
// involved in establishing it, so there is nothing on that side to end.
func (e *Engine) logout(c *gin.Context) {
	cookie, cookieErr := c.Cookie(middleware.SessionCookieName)
	if cookieErr != nil || cookie == "" {
		c.Status(http.StatusNoContent)
		return
	}
	raw, err := decodeSessionCookie(cookie)
	if err != nil {
		clearSessionCookie(c)
		c.Status(http.StatusNoContent)
		return
	}
	token := secret.New(raw)
	establishedByProvider := e.Auth.SessionAMR(c.Request.Context(), token) == amrProvider
	err = e.Auth.RevokeSession(c.Request.Context(), token)
	if err != nil && !errors.Is(err, auth.ErrCredentials) {
		failKnown(c, err)
		return
	}
	clearSessionCookie(c)
	if establishedByProvider {
		if endSessionURL, ok := e.oidcEndSessionURL(c); ok {
			writeJSON(c, http.StatusOK, handler.LogoutView{EndSessionURL: endSessionURL})
			return
		}
	}
	c.Status(http.StatusNoContent)
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
func (e *Engine) setSessionCookie(c *gin.Context, printable string) {
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(middleware.SessionCookieName, printable, int(sessionCookieMaxAge/time.Second), "/", "", true, true)
}

// clearSessionCookie expires it.
//
// Every attribute except the value repeats what setSessionCookie wrote. A
// browser matches a deletion by name, path and domain, so a clear that omits
// the path leaves the original cookie in place.
func clearSessionCookie(c *gin.Context) {
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(middleware.SessionCookieName, "", -1, "/", "", true, true)
}

// failKnown renders an error whose subject the caller already knows about.
//
// The auth family is where Hidden is wrong: a caller signing in named the
// account themselves, so rendering a disabled account as not-found tells them
// nothing they did not supply and hides an answer they can act on.
func failKnown(c *gin.Context, err error) {
	refuse(c, apierr.Classify(err, apierr.VisibilityKnown))
}

// clientAddr is the address the chain resolved, as the audit log records it.
func clientAddr(c *gin.Context) string {
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
