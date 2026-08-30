//go:build linux

// Login flow v2, the standard sign-in path for current desktop and mobile
// clients.
//
//	A client posts to begin and receives a poll token, a poll endpoint and a
//	login URL. The URL opens in the system browser, where a signed-in person
//	approves with a POST. The client polls; while nobody has approved the
//	answer is pending, and once approved it carries the server, the login
//	name and an app password.
//
// The properties this file holds:
//
//  1. Two tokens that never meet. The login one crosses a browser, so it is
//     public by the time the flow is under way; the poll one never leaves
//     the client, and only it can collect anything.
//  2. Digests rather than tokens on disk, so a leaked table cannot complete
//     a flow that is still live.
//  3. Twenty minutes and a sweep, so an abandoned flow is not a standing
//     offer.
//  4. A poll interval on the answer a stranger could hammer for free.
//  5. Approval as a POST from a signed-in person holding a CSRF token. A
//     GET that approved would mint a credential for whoever could load an
//     image tag while somebody was logged in.
//  6. One credential per flow, and the delivery retrievable until expiry, so
//     a client that drops its one response can ask again rather than lose a
//     password it never saw.
//  7. Every URL from the origin the request arrived on, already checked.
package lifecycle

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/store/state"
)

// The flow's lifetimes.
const (
	// FlowTTL is how long a flow survives unapproved. Long enough for
	// somebody to find the browser window, short enough that an abandoned
	// one is not a standing invitation.
	FlowTTL = 20 * time.Minute
	// FlowPollInterval is the shortest gap between two polls on one token.
	FlowPollInterval = time.Second
)

// The flow's rejections. Distinct sentinels because the handler maps them to
// distinct answers, except unknown, which covers a token that never existed,
// one that expired and one already taken: telling them apart tells a prober
// which tokens exist.
var (
	ErrFlowUnknown = errors.New("no such login flow")
	ErrFlowPending = errors.New("the login flow is not approved yet")
	// ErrFlowApproved refuses a second approval. A flow can never mint more
	// than one credential, so replacing the first would be a second mint.
	ErrFlowApproved = errors.New("the login flow is already approved")
	// ErrFlowRateLimited is a poll arriving before the previous one's window
	// closed, which is what stops a stranger using a token as a database
	// query.
	ErrFlowRateLimited = errors.New("polling too fast")
)

// FlowTokens is what the begin answer hands a client: its own secret, the
// public one, and where to take each.
type FlowTokens struct {
	// PollToken is the client's own secret and never crosses a browser, so
	// it is the only one of the two that can collect anything.
	PollToken string
	// LoginToken is the one that does cross a browser, in a URL, so it is
	// treated as read by whoever was looking at the screen.
	LoginToken string
	// LoginURL and PollEndpoint are built from the origin the client used.
	LoginURL     string
	PollEndpoint string
}

// FlowRecord is one stored flow, as the durable half holds it.
//
// What is stored is derived: a digest of each token rather than either token
// itself, so reading this table says which sign-ins are under way without
// giving anyone the means to finish one.
// flowExpired reports whether the row's flow has run past the twenty minutes
// a client might reasonably take to walk from the app to a browser and back.
func flowExpired(row state.LoginFlowRow, nowNs int64) bool {
	return nowNs-row.CreatedNs >= int64(FlowTTL)
}

// FlowCredential mints the app password a delivered flow hands over. The
// plaintext exists for one response and is then forgotten by both sides.
type FlowCredential interface {
	// MintSyncCredential returns a new app password in plaintext, once.
	MintSyncCredential(ctx context.Context, user int64) (string, error)
}

// LoginFlow runs the device login against the state store.
type LoginFlow struct {
	state *state.DB
	cred  FlowCredential
	now   func() int64
}

// NewLoginFlow builds the flow. A nil credential leaves approval working and
// delivery failing, which is a broken wiring rather than a state this file
// papers over: construction with one is refused.
func NewLoginFlow(st *state.DB, cred FlowCredential, now func() int64) (*LoginFlow, error) {
	if st == nil {
		return nil, errors.New("the login flow needs a state store")
	}
	if cred == nil {
		return nil, errors.New("the login flow needs a credential source")
	}
	if now == nil {
		now = func() int64 { return time.Now().UnixNano() }
	}
	return &LoginFlow{state: st, cred: cred, now: now}, nil
}

// flowToken mints one token: 32 bytes from the CSPRNG, URL-safe. These are
// the only thing standing between a stranger and an account's credential.
func flowToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("minting a login-flow token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// flowDigest is what is stored in place of a token.
func flowDigest(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// Begin opens a flow and hands back the pair of tokens and the two addresses
// the client works from.
//
// origin is the base URL the request arrived on. The host guard has already
// matched it against the declared names before any handler runs, so this is
// an operator's configuration rather than a caller's choice, and reading it
// back is how a deployment reached under several names sends each client
// home to the one it used.
func (f *LoginFlow) Begin(ctx context.Context, origin string) (FlowTokens, error) {
	pollTok, err := flowToken()
	if err != nil {
		return FlowTokens{}, err
	}
	loginTok, err := flowToken()
	if err != nil {
		return FlowTokens{}, err
	}

	if perr := f.state.PutLoginFlow(ctx, state.LoginFlowRow{
		PollDigest:  flowDigest(pollTok),
		LoginDigest: flowDigest(loginTok),
		CreatedNs:   f.now(),
	}); perr != nil {
		return FlowTokens{}, perr
	}

	return FlowTokens{
		PollToken:    pollTok,
		LoginToken:   loginTok,
		LoginURL:     origin + "/index.php/login/v2/flow/" + loginTok,
		PollEndpoint: origin + "/index.php/login/v2/poll",
	}, nil
}

// Approve writes down that a signed-in person said yes.
//
// What this file cannot check is everything that makes the yes mean anything:
// the method, the session and the CSRF token all belong to the router, which
// has ruled before a user id arrives here. Approving from here is deliberately
// the last step rather than the guarded one.
func (f *LoginFlow) Approve(ctx context.Context, loginToken string, user int64, login string) error {
	now := f.now()
	rec, err := f.state.LoginFlowByLogin(ctx, flowDigest(loginToken))
	if err != nil {
		return ErrFlowUnknown
	}
	if flowExpired(rec, now) {
		return ErrFlowUnknown
	}
	if rec.ApprovedUser != nil {
		return ErrFlowApproved
	}
	if aerr := f.state.ApproveLoginFlow(ctx, flowDigest(loginToken), user, login); aerr != nil {
		return flowError(aerr)
	}
	return nil
}

// FlowDelivery is what a successful poll hands over.
type FlowDelivery struct {
	Server      string
	LoginName   string
	AppPassword string
}

// Poll hands over the credential, when a person has said yes.
//
// The mint happens on this call rather than at approval, so the plaintext
// crosses the wire once and is never written down. Dropping the flow after a
// successful delivery is what makes the mint single-use: a second poll on
// the same token finds nothing to deliver.
func (f *LoginFlow) Poll(ctx context.Context, pollToken, origin string) (FlowDelivery, error) {
	now := f.now()
	d := flowDigest(pollToken)

	rec, err := f.state.LoginFlowByPoll(ctx, d)
	if err != nil {
		return FlowDelivery{}, ErrFlowUnknown
	}
	if flowExpired(rec, now) {
		return FlowDelivery{}, ErrFlowUnknown
	}

	// Constant time, though the lookup already keyed on this digest. The
	// store's index could change under it someday, and the cheap defence is
	// to keep the comparison honest regardless of how the row was found.
	if subtle.ConstantTimeCompare(rec.PollDigest, d) != 1 {
		return FlowDelivery{}, ErrFlowUnknown
	}

	// The rate limit applies to the pending answer only. An approved flow has
	// nothing left to guess: its credential is minted once and the flow goes
	// with it, so delivering past the limit cannot help a prober and does
	// help the client that polled while the person was still finding the
	// button. That client's next poll would otherwise land inside the window
	// the refused poll had just opened, and every retry would push it further
	// out: an app hanging on a flow that was ready to deliver.
	if rec.ApprovedUser == nil {
		if terr := f.state.TouchLoginFlowPoll(ctx, d, now, int64(FlowPollInterval)); terr != nil {
			return FlowDelivery{}, flowError(terr)
		}
		return FlowDelivery{}, ErrFlowPending
	}

	password, merr := f.cred.MintSyncCredential(ctx, *rec.ApprovedUser)
	if merr != nil {
		return FlowDelivery{}, merr
	}
	// The flow goes after the mint succeeds. A failure to drop is reported
	// rather than swallowed, because a flow left standing could mint a second
	// credential the first delivery's record knows nothing about.
	if derr := f.state.DropLoginFlow(ctx, d); derr != nil {
		return FlowDelivery{}, fmt.Errorf("the delivered flow could not be closed: %w", derr)
	}

	return FlowDelivery{
		Server:      origin,
		LoginName:   rec.ApprovedLogin,
		AppPassword: password,
	}, nil
}

// SweepFlow removes expired flows, returning how many went.
func (f *LoginFlow) SweepFlow(ctx context.Context) (int64, error) {
	return f.state.SweepLoginFlows(ctx, f.now()-int64(FlowTTL))
}

// flowError maps the store's sentinels onto the flow's.
//
// The store speaks about rows; this file speaks about flows. Keeping the
// mapping in one function is what stops a new store sentinel from reaching a
// handler unrecognised and answering 500.
func flowError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, state.ErrLoginFlowUnknown):
		return ErrFlowUnknown
	case errors.Is(err, state.ErrLoginFlowApproved):
		return ErrFlowApproved
	case errors.Is(err, state.ErrLoginFlowTooSoon):
		return ErrFlowRateLimited
	}
	return err
}
