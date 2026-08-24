//go:build linux && compat_nc

package ncwire

import (
	"context"
	"errors"
	"net/http"

	"github.com/heavycaffeiner/stowcloud/go/internal/auth"
	"github.com/heavycaffeiner/stowcloud/go/internal/compat/nc"
	"github.com/heavycaffeiner/stowcloud/go/internal/httpapi/mw"
	"github.com/heavycaffeiner/stowcloud/go/internal/num"
	"github.com/heavycaffeiner/stowcloud/go/internal/store/state"
)

// The two ports the device login needs, satisfied from the server's own
// pieces. They live here for the same reason the rest of this package does:
// the layer may not import the store or the auth service, so it states what it
// needs and this supplies it.

// flowStore adapts the state database to the layer's port.
//
// The store's refusals are its own sentinels and the layer matches on its own,
// so each is translated. A refusal that arrived as a bare error would be read
// as a server fault and answered with a 500, which is the wrong answer for a
// client polling too fast or opening a login link twice.
type flowStore struct{ db *state.DB }

func (f flowStore) PutFlow(ctx context.Context, rec nc.FlowRecord) error {
	return f.db.PutLoginFlow(ctx, state.LoginFlowRow{
		PollDigest:  rec.PollDigest,
		LoginDigest: rec.LoginDigest,
		CreatedNs:   rec.CreatedNs,
	})
}

func (f flowStore) FlowByPoll(ctx context.Context, digest []byte) (nc.FlowRecord, error) {
	row, err := f.db.LoginFlowByPoll(ctx, digest)
	return flowRecordOf(row), flowErr(err)
}

func (f flowStore) FlowByLogin(ctx context.Context, digest []byte) (nc.FlowRecord, error) {
	row, err := f.db.LoginFlowByLogin(ctx, digest)
	return flowRecordOf(row), flowErr(err)
}

func (f flowStore) ApproveFlow(ctx context.Context, loginDigest []byte, user int64, login string, nowNs int64) error {
	return flowErr(f.db.ApproveLoginFlow(ctx, loginDigest, user, login, nowNs))
}

func (f flowStore) TouchPoll(ctx context.Context, pollDigest []byte, nowNs int64) error {
	// The interval is the layer's constant. It is passed in rather than
	// duplicated in the store, so the two cannot disagree about how fast a
	// client may poll.
	return flowErr(f.db.TouchLoginFlowPoll(ctx, pollDigest, nowNs, int64(nc.PollInterval)))
}

func (f flowStore) DropFlow(ctx context.Context, pollDigest []byte) error {
	return f.db.DropLoginFlow(ctx, pollDigest)
}

func (f flowStore) SweepFlows(ctx context.Context, nowNs int64) (int, error) {
	// The layer hands the current time and owns the lifetime, so the cut-off
	// is computed here rather than stored as a second copy of the TTL.
	return f.db.SweepLoginFlows(ctx, nowNs-int64(nc.FlowTTL))
}

func flowRecordOf(row state.LoginFlowRow) nc.FlowRecord {
	return nc.FlowRecord{
		PollDigest:    row.PollDigest,
		LoginDigest:   row.LoginDigest,
		CreatedNs:     row.CreatedNs,
		ApprovedUser:  row.ApprovedUser,
		ApprovedLogin: row.ApprovedLogin,
		LastPollNs:    row.LastPollNs,
	}
}

// flowErr maps the store's sentinels onto the layer's.
func flowErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, state.ErrLoginFlowUnknown):
		return nc.ErrFlowUnknown
	case errors.Is(err, state.ErrLoginFlowApproved):
		return nc.ErrFlowAlreadyApproved
	case errors.Is(err, state.ErrLoginFlowTooSoon):
		return nc.ErrFlowRateLimited
	}
	return err
}

// authPort mints the credential the flow delivers.
type authPort struct{ svc *auth.Service }

// MintAppPassword issues one for the account that approved.
//
// Full scope and no expiry, which is what a sync client needs: it is a
// filesystem credential for a device, and one that expires silently is a client
// that stops syncing without saying why. Revoking it is the account screen's
// job, where the device is listed by the name given here.
func (a authPort) MintAppPassword(ctx context.Context, user int64, name string) (string, error) {
	return a.svc.CreateAppPassword(ctx, user, name, auth.Scope{Perms: mw.ScopeFull}, 0)
}

// Authenticator resolves a request to a principal using the chain's own
// result.
//
// The compat mounts sit inside the server's middleware, so the principal is
// already in the context by the time a handler runs. Reading it back is what
// keeps "who is this request from" with one answer: a mount that verified a
// credential itself would be a second implementation of the question.
func Authenticator(r *http.Request) (nc.Principal, bool) {
	p, ok := mw.PrincipalFrom(r.Context())
	if !ok || p.Disabled {
		return nc.Principal{}, false
	}
	// The account id is uint32 across this seam, so a value that does not fit
	// is refused rather than truncated into somebody else's account.
	id, nerr := num.Narrow[uint32](p.UserID)
	if nerr != nil {
		return nc.Principal{}, false
	}
	// CredentialID stays zero. The chain resolves which app password a request
	// carried but does not publish its row id, and the one surface that wants
	// it is account removal, which is not mounted here.
	//
	// The account's own name, which is what a client stores as the account it
	// signed in as rather than whatever label was set for display.
	return nc.Principal{User: id, Login: p.Login}, true
}
