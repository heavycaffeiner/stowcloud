//go:build linux

package lifecycle_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/lifecycle"
)

// flowNowBase is where every test clock in this file starts. It is a real
// epoch-scale nanosecond reading rather than a small number, because the
// store's poll-interval comparison is against now minus a second: a clock
// near zero puts the first poll inside an interval nothing began.
const flowNowBase = int64(1_770_000_000) * int64(time.Second)

// The device login, driven against the real state store.
//
// What matters here is the shape the security properties take on the wire:
// two tokens that never meet, an approval that cannot repeat, a delivery that
// mints once, and a poll rate that a stranger cannot ride.

// mintingSource counts what it mints, which is what shows a flow delivered
// exactly once.
type mintingSource struct {
	minted int
}

func (m *mintingSource) MintSyncCredential(_ context.Context, user int64) (string, error) {
	m.minted++
	return "token-for-" + itoa64(user), nil
}

func itoa64(v int64) string {
	if v == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	neg := v < 0
	if neg {
		v = -v
	}
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// newFlow builds one over the fixture's state store, with a fixed clock so
// expiry is something a test moves rather than waits for.
func newFlow(t *testing.T, f *fixture, now func() int64) (*lifecycle.LoginFlow, *mintingSource) {
	t.Helper()
	src := &mintingSource{}
	flow, err := lifecycle.NewLoginFlow(f.state, src, now)
	if err != nil {
		t.Fatalf("building the flow: %v", err)
	}
	return flow, src
}

const flowOrigin = "https://stow.example"

// A begun flow returns two different tokens and URLs built from the origin.
func TestABegunFlowNamesTheOriginItWasGiven(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	now := func() int64 { return flowNowBase }
	flow, _ := newFlow(t, f, now)

	tokens, err := flow.Begin(context.Background(), flowOrigin)
	if err != nil {
		t.Fatalf("beginning: %v", err)
	}

	if tokens.PollToken == tokens.LoginToken {
		t.Error("the two tokens are the same value")
	}
	if !strings.HasPrefix(tokens.LoginURL, flowOrigin+"/index.php/login/v2/flow/") {
		t.Errorf("the login URL is %q", tokens.LoginURL)
	}
	if tokens.PollEndpoint != flowOrigin+"/index.php/login/v2/poll" {
		t.Errorf("the poll endpoint is %q", tokens.PollEndpoint)
	}
}

// A poll before approval is pending, not an error a client would treat as
// failure. The client keeps polling on this answer.
func TestAnUnapprovedFlowIsPending(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	now := func() int64 { return flowNowBase }
	flow, _ := newFlow(t, f, now)
	ctx := context.Background()

	tokens, err := flow.Begin(ctx, flowOrigin)
	if err != nil {
		t.Fatalf("beginning: %v", err)
	}

	if _, err := flow.Poll(ctx, tokens.PollToken, flowOrigin); !errors.Is(err, lifecycle.ErrFlowPending) {
		t.Errorf("polling an unapproved flow answered %v, want pending", err)
	}
}

// Delivery after approval mints exactly one credential, and the login name is
// the one the approval recorded.
func TestDeliveryMintsOnceWithTheApprovedName(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	now := func() int64 { return flowNowBase }
	flow, src := newFlow(t, f, now)
	ctx := context.Background()

	tokens, err := flow.Begin(ctx, flowOrigin)
	if err != nil {
		t.Fatalf("beginning: %v", err)
	}
	if aerr := flow.Approve(ctx, tokens.LoginToken, 7, "alice"); aerr != nil {
		t.Fatalf("approving: %v", aerr)
	}

	got, err := flow.Poll(ctx, tokens.PollToken, flowOrigin)
	if err != nil {
		t.Fatalf("polling: %v", err)
	}
	if got.LoginName != "alice" {
		t.Errorf("the delivery names %q", got.LoginName)
	}
	if got.AppPassword == "" {
		t.Error("the delivery carries no credential")
	}
	if src.minted != 1 {
		t.Errorf("%d credentials minted, want 1", src.minted)
	}
}

// A second poll after delivery finds nothing. The mint is single-use: a flow
// that could mint twice would hand out a credential nobody can revoke through
// the first one's record.
func TestASecondPollFindsNothing(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	now := func() int64 { return flowNowBase }
	flow, src := newFlow(t, f, now)
	ctx := context.Background()

	tokens, err := flow.Begin(ctx, flowOrigin)
	if err != nil {
		t.Fatalf("beginning: %v", err)
	}
	if err := flow.Approve(ctx, tokens.LoginToken, 7, "alice"); err != nil {
		t.Fatalf("approving: %v", err)
	}
	if _, err := flow.Poll(ctx, tokens.PollToken, flowOrigin); err != nil {
		t.Fatalf("the first poll: %v", err)
	}

	if _, err := flow.Poll(ctx, tokens.PollToken, flowOrigin); !errors.Is(err, lifecycle.ErrFlowUnknown) {
		t.Errorf("the second poll answered %v, want unknown", err)
	}
	if src.minted != 1 {
		t.Errorf("%d credentials minted across two polls, want 1", src.minted)
	}
}

// The login token must not be able to poll. It travels a browser address bar,
// so it is assumed public: knowing it must not deliver a credential.
func TestTheLoginTokenCannotPoll(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	now := func() int64 { return flowNowBase }
	flow, _ := newFlow(t, f, now)
	ctx := context.Background()

	tokens, err := flow.Begin(ctx, flowOrigin)
	if err != nil {
		t.Fatalf("beginning: %v", err)
	}
	if err := flow.Approve(ctx, tokens.LoginToken, 7, "alice"); err != nil {
		t.Fatalf("approving: %v", err)
	}

	if _, err := flow.Poll(ctx, tokens.LoginToken, flowOrigin); !errors.Is(err, lifecycle.ErrFlowUnknown) {
		t.Errorf("polling with the login token answered %v, want unknown", err)
	}
}

// A second approval is refused. A flow that replaced its approver would mint
// again with the second one's account.
func TestASecondApprovalIsRefused(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	now := func() int64 { return flowNowBase }
	flow, src := newFlow(t, f, now)
	ctx := context.Background()

	tokens, err := flow.Begin(ctx, flowOrigin)
	if err != nil {
		t.Fatalf("beginning: %v", err)
	}
	if err := flow.Approve(ctx, tokens.LoginToken, 7, "alice"); err != nil {
		t.Fatalf("the first approval: %v", err)
	}
	if err := flow.Approve(ctx, tokens.LoginToken, 8, "mallory"); !errors.Is(err, lifecycle.ErrFlowApproved) {
		t.Errorf("the second approval answered %v, want approved", err)
	}

	// The refusal held: delivery still names the first account.
	if _, err := flow.Poll(ctx, tokens.PollToken, flowOrigin); err != nil {
		t.Fatalf("polling: %v", err)
	}
	if src.minted != 1 {
		t.Fatalf("%d credentials minted", src.minted)
	}
}

// An expired flow is unknown, both to poll and to approve. An abandoned flow
// must not be a standing invitation.
func TestAnExpiredFlowIsUnknown(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	nowNs := flowNowBase
	now := func() int64 { return nowNs }
	flow, _ := newFlow(t, f, now)
	ctx := context.Background()

	tokens, err := flow.Begin(ctx, flowOrigin)
	if err != nil {
		t.Fatalf("beginning: %v", err)
	}

	nowNs += int64(lifecycle.FlowTTL) + 1

	if _, err := flow.Poll(ctx, tokens.PollToken, flowOrigin); !errors.Is(err, lifecycle.ErrFlowUnknown) {
		t.Errorf("polling an expired flow answered %v, want unknown", err)
	}
	if err := flow.Approve(ctx, tokens.LoginToken, 7, "alice"); !errors.Is(err, lifecycle.ErrFlowUnknown) {
		t.Errorf("approving an expired flow answered %v, want unknown", err)
	}
}

// Polling an unapproved flow faster than the interval is refused, and polling
// after the interval is accepted. The limit exists to stop a stranger
// hammering a token that is not theirs.
func TestPollingTooSoonIsRefusedAndLaterAccepted(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	nowNs := flowNowBase
	now := func() int64 { return nowNs }
	flow, _ := newFlow(t, f, now)
	ctx := context.Background()

	tokens, err := flow.Begin(ctx, flowOrigin)
	if err != nil {
		t.Fatalf("beginning: %v", err)
	}

	if _, err := flow.Poll(ctx, tokens.PollToken, flowOrigin); !errors.Is(err, lifecycle.ErrFlowPending) {
		t.Fatalf("the first poll answered %v, want pending", err)
	}
	// The same instant: inside the interval.
	if _, err := flow.Poll(ctx, tokens.PollToken, flowOrigin); !errors.Is(err, lifecycle.ErrFlowRateLimited) {
		t.Errorf("an immediate second poll answered %v, want rate limited", err)
	}

	// Past the interval.
	nowNs += int64(lifecycle.FlowPollInterval) + 1
	if _, err := flow.Poll(ctx, tokens.PollToken, flowOrigin); !errors.Is(err, lifecycle.ErrFlowPending) {
		t.Errorf("a poll after the interval answered %v, want pending", err)
	}
}

// An approved flow is delivered whatever the poll rate. The limit exists for
// strangers, not for the client whose flow became ready: a client that polled
// while the human was approving must not find itself locked out of the answer.
func TestAnApprovedFlowDeliversPastTheLimit(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	nowNs := flowNowBase
	now := func() int64 { return nowNs }
	flow, _ := newFlow(t, f, now)
	ctx := context.Background()

	tokens, err := flow.Begin(ctx, flowOrigin)
	if err != nil {
		t.Fatalf("beginning: %v", err)
	}

	// Two immediate polls on the unapproved flow: the first is pending, which
	// is the answer the client keeps polling on, and the second is refused.
	_, err = flow.Poll(ctx, tokens.PollToken, flowOrigin)
	if !errors.Is(err, lifecycle.ErrFlowPending) {
		t.Fatalf("the first poll answered %v, want pending", err)
	}
	if _, perr := flow.Poll(ctx, tokens.PollToken, flowOrigin); !errors.Is(perr, lifecycle.ErrFlowRateLimited) {
		t.Fatalf("the second poll answered %v, want rate limited", perr)
	}

	// Approval lands between polls. The next one is immediate and must deliver.
	if aerr := flow.Approve(ctx, tokens.LoginToken, 7, "alice"); aerr != nil {
		t.Fatalf("approving: %v", aerr)
	}
	got, err := flow.Poll(ctx, tokens.PollToken, flowOrigin)
	if err != nil {
		t.Fatalf("the poll after approval: %v", err)
	}
	if got.AppPassword == "" {
		t.Error("the delivery carries no credential")
	}
}

// An unknown poll token is the same answer as a taken one, and neither is the
// pending answer. Telling them apart would tell a prober which tokens exist.
func TestAnUnknownTokenIsUnknown(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	now := func() int64 { return flowNowBase }
	flow, _ := newFlow(t, f, now)
	ctx := context.Background()

	if _, err := flow.Poll(ctx, "never-minted", flowOrigin); !errors.Is(err, lifecycle.ErrFlowUnknown) {
		t.Errorf("an unknown token answered %v, want unknown", err)
	}
}

// The sweep removes what expired and nothing else.
func TestTheSweepRemovesExpiredFlows(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	nowNs := flowNowBase
	now := func() int64 { return nowNs }
	flow, _ := newFlow(t, f, now)
	ctx := context.Background()

	old, err := flow.Begin(ctx, flowOrigin)
	if err != nil {
		t.Fatalf("beginning the old flow: %v", err)
	}

	// A newer flow, begun after time has moved. The sweep must leave it.
	nowNs += int64(lifecycle.FlowTTL) / 2
	fresh, err := flow.Begin(ctx, flowOrigin)
	if err != nil {
		t.Fatalf("beginning the fresh flow: %v", err)
	}

	nowNs += int64(lifecycle.FlowTTL)/2 + 1
	removed, err := flow.SweepFlow(ctx)
	if err != nil {
		t.Fatalf("sweeping: %v", err)
	}
	if removed != 1 {
		t.Errorf("the sweep removed %d flows, want 1", removed)
	}

	// The old one is gone; the fresh one is still a live flow.
	if _, err := flow.Poll(ctx, old.PollToken, flowOrigin); !errors.Is(err, lifecycle.ErrFlowUnknown) {
		t.Errorf("a swept flow answered %v, want unknown", err)
	}
	if _, err := flow.Poll(ctx, fresh.PollToken, flowOrigin); !errors.Is(err, lifecycle.ErrFlowPending) {
		t.Errorf("the fresh flow answered %v, want pending, which is alive", err)
	}
}

// Construction without a credential source is refused rather than built into
// something that approves and then cannot deliver.
func TestTheFlowNeedsACredentialSource(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	if _, err := lifecycle.NewLoginFlow(f.state, nil, nil); err == nil {
		t.Error("construction without a credential source succeeded")
	}
}

// The store's own sentinels arrive mapped. A raw store error reaching a
// handler would answer 500 for an answer the client has a status for.
func TestAStoreSentinelArrivesMapped(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	now := func() int64 { return flowNowBase }
	flow, _ := newFlow(t, f, now)
	ctx := context.Background()

	// A poll on a token whose row was never stored reaches the store, which
	// answers its own unknown sentinel; the flow must translate it into its
	// own, so a handler mapping on the flow's sentinels keeps working.
	if _, err := flow.Poll(ctx, "absent", flowOrigin); !errors.Is(err, lifecycle.ErrFlowUnknown) {
		t.Errorf("an absent flow answered %v, want the flow's own unknown", err)
	}
}
