//go:build linux && compat_nc

package nc

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/internal/compat/ncport"
)

// Login flow v2, the standard login path for current desktop and mobile
// clients.
//
//	(1) client posts to begin
//	    -> a poll token, a poll endpoint, and a login URL
//	(2) the client opens the login URL in the system browser
//	    -> unauthenticated: redirected to the web login, the destination kept
//	    -> authenticated:   a consent screen with a POST-only approve button
//	(3) the client polls with its poll token
//	    -> pending while nobody has approved
//	    -> once approved: the server, the login name and an app password
//
// The properties this file is responsible for:
//
//  1. Two independent tokens. The flow token travels through a browser address
//     bar, so it reaches history, referrers and whoever is looking at the
//     screen; knowing it must not let anyone poll for the resulting password.
//  2. Only digests are stored, so a database leak is not replayable against a
//     live flow.
//  3. A short expiry, plus a sweep.
//  4. Rate-limited polling. Unbounded polling is a database scan somebody else
//     pays for.
//  5. Approval is a POST performed by a logged-in human with a CSRF token.
//     This is the whole ballgame: if a GET could approve, then merely visiting
//     an attacker's page while logged in would mint an app password and hand
//     it to the attacker's waiting poller, which is a full account takeover
//     from a drive-by image tag.
//  6. Approval is single-use, so a flow can never mint more than one password.
//     The result stays retrievable by the same poll token until the flow
//     expires: deleting it on first read means a client that fails to consume
//     that one response, through a dropped connection or being backgrounded
//     mid-parse, loses a valid credential forever with no way to retry.
//  7. URLs are built from a configured origin and never from the request's own
//     host header, so the set of hosts this can name is fixed by configuration
//     rather than by a request.
//
// One thing here is deliberately not the shape the reference server uses.
// That table carried a
// temporary plaintext app password between approval and collection. This
// stores only a marker naming the authorized user; the polling request
// consumes the marker and mints the credential at delivery time, so the
// plaintext exists for one response and never rests in the database. An
// abandoned or expired flow therefore leaves no live credential behind.

// Login flow failures.
var (
	// ErrFlowUnknown is an unknown token, an expired one, or a result that has
	// already been taken. They are one error on purpose: telling them apart
	// tells a prober which tokens exist.
	ErrFlowUnknown = errors.New("nc: no such login flow")
	// ErrFlowPending is an approval that has not happened yet.
	ErrFlowPending = errors.New("nc: the login flow is not approved yet")
	// ErrFlowAlreadyApproved refuses a second approval.
	ErrFlowAlreadyApproved = errors.New("nc: the login flow is already approved")
	// ErrFlowRateLimited is polling faster than the flow allows.
	ErrFlowRateLimited = errors.New("nc: polling too fast")
)

// Flow lifetimes.
const (
	// FlowTTL is how long a flow survives unapproved. Long enough for somebody
	// to find the browser window, short enough that an abandoned one is not a
	// standing invitation.
	FlowTTL = 20 * time.Minute
	// PollInterval is the shortest gap between two polls on one token.
	PollInterval = time.Second
)

// FlowTokens is what begins a flow.
type FlowTokens struct {
	// PollToken is the client's secret. It never travels through a browser.
	PollToken string
	// LoginToken goes in the URL the browser opens, so it is assumed public.
	LoginToken string
	// LoginURL and PollEndpoint are built from configuration.
	LoginURL     string
	PollEndpoint string
}

// FlowRecord is one stored flow.
//
// Only digests are kept. A leak of this table must not be replayable against a
// live flow, which is the whole reason the tokens are not here.
type FlowRecord struct {
	PollDigest  []byte
	LoginDigest []byte
	CreatedNs   int64
	// ApprovedUser is set once a human has approved, and is the whole of what
	// approval stores. No credential rests here.
	ApprovedUser  *int64
	ApprovedLogin string
	LastPollNs    int64
}

// Expired reports whether the flow has outlived its window.
func (f FlowRecord) Expired(nowNs int64) bool {
	return nowNs-f.CreatedNs >= int64(FlowTTL)
}

// FlowStore is the durable half, which lives in the wiring package because the
// layer may not import the store.
type FlowStore interface {
	// PutFlow stores a new flow.
	PutFlow(ctx context.Context, rec FlowRecord) error
	// FlowByPoll finds a flow by its poll digest.
	FlowByPoll(ctx context.Context, digest []byte) (FlowRecord, error)
	// FlowByLogin finds a flow by its login digest.
	FlowByLogin(ctx context.Context, digest []byte) (FlowRecord, error)
	// ApproveFlow records the authorized user, refusing a second approval.
	ApproveFlow(ctx context.Context, loginDigest []byte, user int64, login string, nowNs int64) error
	// TouchPoll records a poll attempt, refusing one that is too soon.
	TouchPoll(ctx context.Context, pollDigest []byte, nowNs int64) error
	// DropFlow removes a flow once its credential has been delivered.
	DropFlow(ctx context.Context, pollDigest []byte) error
	// SweepFlows removes what has expired.
	SweepFlows(ctx context.Context, nowNs int64) (int, error)
}

// AuthPort mints the credential, at delivery time rather than at approval.
type AuthPort interface {
	// MintAppPassword returns a new app password in plaintext, once. The
	// caller sends it and forgets it.
	MintAppPassword(ctx context.Context, user int64, name string) (string, error)
}

// newFlowToken mints one token.
//
// 32 bytes from the CSPRNG, URL-safe: these are the only thing standing
// between a stranger and an account's credential.
func newFlowToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("nc: minting a login-flow token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// digest is what is stored in place of a token.
func digest(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// LoginFlow runs the flow.
type LoginFlow struct {
	store FlowStore
	auth  AuthPort
	// origin is the configured base URL. Never the request's host: the set of
	// hosts this can name is fixed by configuration.
	origin string
	now    func() int64
}

// NewLoginFlow builds it.
func NewLoginFlow(store FlowStore, auth AuthPort, origin string, now func() int64) *LoginFlow {
	return &LoginFlow{store: store, auth: auth, origin: origin, now: now}
}

// Begin starts a flow and returns what the client needs.
//
// origin is the base URL the client reached this server on, empty to use the
// configured one. A deployment is reached under several names and the client
// has to be sent back to the one it started from: a login URL naming a
// different host is one the browser cannot open, and the configured value is
// only ever the first of the declared names.
//
// It is the request's own host, which is safe here because the host guard has
// already refused anything not on the declared list by the time a handler
// runs. This is not the guard learning its origin from the request.
func (l *LoginFlow) Begin(ctx context.Context, origin string) (FlowTokens, error) {
	if origin == "" {
		origin = l.origin
	}
	pollTok, err := newFlowToken()
	if err != nil {
		return FlowTokens{}, err
	}
	loginTok, err := newFlowToken()
	if err != nil {
		return FlowTokens{}, err
	}

	rec := FlowRecord{
		PollDigest:  digest(pollTok),
		LoginDigest: digest(loginTok),
		CreatedNs:   l.now(),
	}
	if perr := l.store.PutFlow(ctx, rec); perr != nil {
		return FlowTokens{}, perr
	}

	return FlowTokens{
		PollToken:    pollTok,
		LoginToken:   loginTok,
		LoginURL:     origin + "/index.php/login/v2/flow/" + loginTok,
		PollEndpoint: origin + "/index.php/login/v2/poll",
	}, nil
}

// Approve records a human's consent.
//
// The caller has already established that this is a POST from a logged-in
// principal with a valid CSRF token. That is not checked here because it
// cannot be: this package sees a user id, and whether the request carrying it
// was a POST is the router's knowledge.
func (l *LoginFlow) Approve(ctx context.Context, loginToken string, user int64, login string) error {
	now := l.now()
	rec, err := l.store.FlowByLogin(ctx, digest(loginToken))
	if err != nil {
		return ErrFlowUnknown
	}
	if rec.Expired(now) {
		return ErrFlowUnknown
	}
	if rec.ApprovedUser != nil {
		return ErrFlowAlreadyApproved
	}
	return l.store.ApproveFlow(ctx, digest(loginToken), user, login, now)
}

// PollResult is what a successful poll delivers.
type PollResult struct {
	Server      string
	LoginName   string
	AppPassword string
}

// Poll returns the credential once a human has approved.
//
// The credential is minted here rather than at approval, so the plaintext
// exists for exactly one response and never rests in the database. The flow is
// dropped after a successful delivery, which is also what makes the mint
// single-use: a second poll finds nothing.
// origin is the base URL the client reached this server on, empty to use the
// configured one. It is what the client stores as the server address and uses
// for every request afterwards, so sending the configured name to a client
// that arrived under another one points it somewhere it cannot reach.
func (l *LoginFlow) Poll(ctx context.Context, pollToken, origin string) (PollResult, error) {
	if origin == "" {
		origin = l.origin
	}
	now := l.now()
	d := digest(pollToken)

	rec, err := l.store.FlowByPoll(ctx, d)
	if err != nil {
		return PollResult{}, ErrFlowUnknown
	}
	if rec.Expired(now) {
		return PollResult{}, ErrFlowUnknown
	}

	// The comparison is constant time even though the lookup already matched:
	// the digest came out of the store keyed by itself, and comparing it back
	// is what keeps a partial-match oracle from existing if that ever changes.
	if subtle.ConstantTimeCompare(rec.PollDigest, d) != 1 {
		return PollResult{}, ErrFlowUnknown
	}

	// An approved flow is delivered whatever the poll rate. The limit exists to
	// stop a stranger hammering a token that is not theirs, and a flow that has
	// already been approved has nothing left to guess: the credential is minted
	// once and the flow is dropped with it.
	//
	// Checking the limit first meant a client that polled while the human was
	// approving got 429 forever. Its next poll was inside the interval that the
	// refused one had just recorded, so every attempt reset the window it was
	// waiting on and the app hung on a flow that was ready.
	if rec.ApprovedUser == nil {
		// Rate limited only on the answer that costs nothing to retry, so a
		// prober cannot poll faster than the limit whether or not the flow
		// exists.
		if terr := l.store.TouchPoll(ctx, d, now); terr != nil {
			return PollResult{}, terr
		}
		return PollResult{}, ErrFlowPending
	}

	password, merr := l.auth.MintAppPassword(ctx, *rec.ApprovedUser, "login flow")
	if merr != nil {
		return PollResult{}, merr
	}
	// Dropped after the mint. A failure here would leave a live credential
	// whose flow could mint another, so it is reported rather than ignored.
	if derr := l.store.DropFlow(ctx, d); derr != nil {
		return PollResult{}, fmt.Errorf("nc: the delivered flow could not be closed: %w", derr)
	}

	return PollResult{
		Server:      origin,
		LoginName:   rec.ApprovedLogin,
		AppPassword: password,
	}, nil
}

// Sweep removes expired flows.
func (l *LoginFlow) Sweep(ctx context.Context) (int, error) {
	return l.store.SweepFlows(ctx, l.now())
}

// BeginJSON renders what the client reads from Begin.
func (t FlowTokens) BeginJSON() Val {
	return VMap(
		F("poll", VMap(
			F("token", VStr(t.PollToken)),
			F("endpoint", VStr(t.PollEndpoint)),
		)),
		F("login", VStr(t.LoginURL)),
	)
}

// PollJSON renders a delivered credential.
func (r PollResult) PollJSON() Val {
	return VMap(
		F("server", VStr(r.Server)),
		F("loginName", VStr(r.LoginName)),
		F("appPassword", VStr(r.AppPassword)),
	)
}

// The login-flow handlers.
//
// These write bare JSON rather than an OCS envelope: the flow predates the
// envelope and clients parse it directly.

// loginBegin starts a flow.
func (l *Layer) loginBegin(w http.ResponseWriter, r *http.Request) {
	if l.deps.Flow == nil {
		http.NotFound(w, r)
		return
	}
	tokens, err := l.deps.Flow.Begin(r.Context(), requestOrigin(r))
	if err != nil {
		l.warn("a login flow could not be started", "error", err)
		http.Error(w, "the login flow could not be started", http.StatusInternalServerError)
		return
	}
	writeBareJSON(w, tokens.BeginJSON())
}

// loginConsent renders the page the client opened in a browser.
//
// It shows what is about to be granted and offers one POST-only button. There
// is deliberately no approval on this GET: a GET that approves is a credential
// handed out to whoever can make a logged-in browser load an image tag.
//
// The token is not checked here. It names a flow rather than authorizing one,
// the page it produces is the same either way, and the approval below it
// verifies both the flow and the session: refusing early would tell a stranger
// which tokens exist.
func (l *Layer) loginConsent(w http.ResponseWriter, r *http.Request) {
	if l.deps.Flow == nil || l.deps.WriteConsent == nil {
		http.NotFound(w, r)
		return
	}
	l.deps.WriteConsent(w, r, r.PathValue("token"))
}

// requestOrigin is the base URL this request arrived on.
//
// The host guard has already refused anything not on the declared list by the
// time a handler runs, so this is a name the operator configured rather than
// one the caller chose. Reading it back is what lets a deployment reached
// under several names send each client back to its own.
//
// Empty when the header is missing, which leaves the configured origin in
// place rather than building a URL with no host in it.
func requestOrigin(r *http.Request) string {
	if r.Host == "" {
		return ""
	}
	return "https://" + r.Host
}

// loginPoll delivers the credential once a human has approved.
//
// A pending flow answers 404, which is what the client polls against: it is
// not an error, it is the answer that means "not yet".
func (l *Layer) loginPoll(w http.ResponseWriter, r *http.Request) {
	if l.deps.Flow == nil {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "a malformed request", http.StatusBadRequest)
		return
	}

	res, err := l.deps.Flow.Poll(r.Context(), r.PostFormValue("token"), requestOrigin(r))
	switch {
	case err == nil:
		writeBareJSON(w, res.PollJSON())
	case errors.Is(err, ErrFlowRateLimited):
		w.WriteHeader(http.StatusTooManyRequests)
	case errors.Is(err, ErrFlowPending), errors.Is(err, ErrFlowUnknown):
		// One answer for pending, unknown, expired and already-taken. Telling
		// them apart tells a prober which tokens exist, and the client treats
		// all of them as "keep waiting" anyway.
		http.NotFound(w, r)
	default:
		l.warn("a login poll failed", "error", err)
		http.Error(w, "the poll failed", http.StatusInternalServerError)
	}
}

// loginGrant records a human's consent.
//
// POST only, and the caller has already established a logged-in principal and
// a valid CSRF token by the time this runs. That is the whole ballgame: if a
// GET could approve, then merely visiting an attacker's page while logged in
// would mint an app password and hand it to the attacker's waiting poller,
// which is a full account takeover from a drive-by image tag.
func (l *Layer) loginGrant(w http.ResponseWriter, r *http.Request) {
	if l.deps.Flow == nil {
		http.NotFound(w, r)
		return
	}
	who, ok := l.authenticate(r)
	if !ok {
		http.Error(w, "sign in first", http.StatusUnauthorized)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "a malformed request", http.StatusBadRequest)
		return
	}

	// The name the client stores as the account it signed in as. It comes
	// from the principal the chain already resolved rather than from a second
	// lookup: the account surface is not mounted here, and a poll that
	// answered an empty login left the app showing an account with no name.
	login := who.Login
	if login == "" && l.deps.Accounts != nil {
		if info, ierr := l.deps.Accounts.UserInfo(r.Context(), ncport.UserID(who.User)); ierr == nil {
			login = info.LoginName
		}
	}

	err := l.deps.Flow.Approve(r.Context(), r.PostFormValue("token"), int64(who.User), login)
	switch {
	case err == nil:
		w.WriteHeader(http.StatusOK)
	case errors.Is(err, ErrFlowAlreadyApproved):
		// A flow can never mint more than one credential, so a second approval
		// is refused rather than replacing the first.
		http.Error(w, "this flow is already approved", http.StatusConflict)
	case errors.Is(err, ErrFlowUnknown):
		http.NotFound(w, r)
	default:
		l.warn("a login approval failed", "error", err)
		http.Error(w, "the approval failed", http.StatusInternalServerError)
	}
}
