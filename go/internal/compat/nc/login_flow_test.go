//go:build linux && compat_nc

package nc

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// The login flow mints a credential for an account, so what is tested here is
// the set of properties that stop it minting one for the wrong person, or
// leaving one lying around for nobody.

// memFlows is an in-memory store, which is what the wiring package provides
// durably. A fake here is right: what is being tested is the flow's rules, and
// a database would test the wiring's SQL instead.
type memFlows struct {
	mu    sync.Mutex
	flows []FlowRecord
}

func (m *memFlows) PutFlow(_ context.Context, rec FlowRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.flows = append(m.flows, rec)
	return nil
}

func (m *memFlows) find(by func(FlowRecord) bool) (int, bool) {
	for i, f := range m.flows {
		if by(f) {
			return i, true
		}
	}
	return 0, false
}

func (m *memFlows) FlowByPoll(_ context.Context, d []byte) (FlowRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	i, ok := m.find(func(f FlowRecord) bool { return bytes.Equal(f.PollDigest, d) })
	if !ok {
		return FlowRecord{}, ErrFlowUnknown
	}
	return m.flows[i], nil
}

func (m *memFlows) FlowByLogin(_ context.Context, d []byte) (FlowRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	i, ok := m.find(func(f FlowRecord) bool { return bytes.Equal(f.LoginDigest, d) })
	if !ok {
		return FlowRecord{}, ErrFlowUnknown
	}
	return m.flows[i], nil
}

func (m *memFlows) ApproveFlow(_ context.Context, d []byte, user int64, login string, _ int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	i, ok := m.find(func(f FlowRecord) bool { return bytes.Equal(f.LoginDigest, d) })
	if !ok {
		return ErrFlowUnknown
	}
	if m.flows[i].ApprovedUser != nil {
		return ErrFlowAlreadyApproved
	}
	u := user
	m.flows[i].ApprovedUser, m.flows[i].ApprovedLogin = &u, login
	return nil
}

func (m *memFlows) TouchPoll(_ context.Context, d []byte, nowNs int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	i, ok := m.find(func(f FlowRecord) bool { return bytes.Equal(f.PollDigest, d) })
	if !ok {
		return ErrFlowUnknown
	}
	if last := m.flows[i].LastPollNs; last != 0 && nowNs-last < int64(PollInterval) {
		return ErrFlowRateLimited
	}
	m.flows[i].LastPollNs = nowNs
	return nil
}

func (m *memFlows) DropFlow(_ context.Context, d []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	i, ok := m.find(func(f FlowRecord) bool { return bytes.Equal(f.PollDigest, d) })
	if !ok {
		return nil
	}
	m.flows = append(m.flows[:i], m.flows[i+1:]...)
	return nil
}

func (m *memFlows) SweepFlows(_ context.Context, nowNs int64) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	kept := m.flows[:0]
	dropped := 0
	for _, f := range m.flows {
		if f.Expired(nowNs) {
			dropped++
			continue
		}
		kept = append(kept, f)
	}
	m.flows = kept
	return dropped, nil
}

// countingAuth records how many credentials were minted, which is the number
// that matters: a flow must never mint more than one.
type countingAuth struct {
	mu     sync.Mutex
	minted int
	users  []int64
}

func (a *countingAuth) MintAppPassword(_ context.Context, user int64, _ string) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.minted++
	a.users = append(a.users, user)
	return "password-for-user", nil
}

type flowFixture struct {
	flow  *LoginFlow
	store *memFlows
	auth  *countingAuth
	nowNs *int64
}

func newFlow(t *testing.T) *flowFixture {
	t.Helper()
	store, auth := &memFlows{}, &countingAuth{}
	now := int64(1_700_000_000_000_000_000)
	f := &flowFixture{store: store, auth: auth, nowNs: &now}
	f.flow = NewLoginFlow(store, auth, "https://files.example", func() int64 { return *f.nowNs })
	return f
}

func TestAFlowBeginsWithTwoIndependentTokens(t *testing.T) {
	f := newFlow(t)
	tok, err := f.flow.Begin(context.Background(), "")
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}

	// The flow token travels through a browser address bar, so knowing it must
	// not let anyone poll for the resulting password.
	if tok.PollToken == tok.LoginToken {
		t.Fatal("the two tokens are the same, so the browser one can poll")
	}
	if len(tok.PollToken) < 40 || len(tok.LoginToken) < 40 {
		t.Fatalf("a token is short: %d and %d bytes", len(tok.PollToken), len(tok.LoginToken))
	}
	// The login URL carries the browser token and never the poll one.
	if !strings.Contains(tok.LoginURL, tok.LoginToken) {
		t.Fatalf("the login URL does not carry its token: %s", tok.LoginURL)
	}
	if strings.Contains(tok.LoginURL, tok.PollToken) {
		t.Fatal("the poll token is in a URL a browser opens")
	}
}

// Two flows must not collide, which is what makes a token a secret rather than
// a name.
func TestTokensAreDistinctAcrossFlows(t *testing.T) {
	f := newFlow(t)
	seen := map[string]bool{}
	for i := 0; i < 32; i++ {
		tok, err := f.flow.Begin(context.Background(), "")
		if err != nil {
			t.Fatalf("Begin: %v", err)
		}
		for _, s := range []string{tok.PollToken, tok.LoginToken} {
			if seen[s] {
				t.Fatalf("a token repeated after %d flows", i)
			}
			seen[s] = true
		}
	}
}

// A leak of the stored rows must not be replayable against a live flow.
func TestOnlyDigestsAreStored(t *testing.T) {
	f := newFlow(t)
	tok, err := f.flow.Begin(context.Background(), "")
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	rec := f.store.flows[0]
	if string(rec.PollDigest) == tok.PollToken || string(rec.LoginDigest) == tok.LoginToken {
		t.Fatal("a token is stored in the clear")
	}
	if bytes.Contains(rec.PollDigest, []byte(tok.PollToken)) {
		t.Fatal("the stored digest contains its token")
	}
}

// Polling before anyone has approved is pending, not a credential.
func TestPollingAnUnapprovedFlowIsPending(t *testing.T) {
	f := newFlow(t)
	tok, err := f.flow.Begin(context.Background(), "")
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, perr := f.flow.Poll(context.Background(), tok.PollToken, ""); !errors.Is(perr, ErrFlowPending) {
		t.Fatalf("err = %v, want ErrFlowPending", perr)
	}
	if f.auth.minted != 0 {
		t.Fatalf("%d credentials were minted for an unapproved flow", f.auth.minted)
	}
}

// The whole point: a human approves, and then the credential is delivered.
func TestAnApprovedFlowDeliversOnce(t *testing.T) {
	f := newFlow(t)
	tok, err := f.flow.Begin(context.Background(), "")
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if aerr := f.flow.Approve(context.Background(), tok.LoginToken, 42, "alice"); aerr != nil {
		t.Fatalf("Approve: %v", aerr)
	}

	res, perr := f.flow.Poll(context.Background(), tok.PollToken, "")
	if perr != nil {
		t.Fatalf("Poll: %v", perr)
	}
	if res.AppPassword == "" || res.LoginName != "alice" {
		t.Fatalf("the delivered result is %+v", res)
	}
	if f.auth.minted != 1 || f.auth.users[0] != 42 {
		t.Fatalf("minted %d credentials for %v", f.auth.minted, f.auth.users)
	}
}

// The credential is minted at delivery rather than at approval, so an
// abandoned flow leaves no live credential behind. This is the one place this
// build deliberately differs from the shape it replaces.
func TestApprovalMintsNothingUntilItIsCollected(t *testing.T) {
	f := newFlow(t)
	tok, err := f.flow.Begin(context.Background(), "")
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if aerr := f.flow.Approve(context.Background(), tok.LoginToken, 42, "alice"); aerr != nil {
		t.Fatalf("Approve: %v", aerr)
	}

	// Approved and never collected.
	if f.auth.minted != 0 {
		t.Fatalf("approval minted %d credentials; an abandoned flow would leave one live",
			f.auth.minted)
	}
	// And the stored row carries no credential to leak.
	rec := f.store.flows[0]
	if rec.ApprovedUser == nil {
		t.Fatal("approval recorded nothing")
	}
	// Expiring it now leaves nothing behind.
	*f.nowNs += int64(FlowTTL) + 1
	dropped, serr := f.flow.Sweep(context.Background())
	if serr != nil || dropped != 1 {
		t.Fatalf("Sweep dropped %d, %v", dropped, serr)
	}
	if f.auth.minted != 0 {
		t.Fatalf("an abandoned flow ended up minting %d credentials", f.auth.minted)
	}
}

// A flow can never mint more than one password, which is what makes a leaked
// login token cost at most one credential.
func TestASecondApprovalIsRefused(t *testing.T) {
	f := newFlow(t)
	tok, err := f.flow.Begin(context.Background(), "")
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if aerr := f.flow.Approve(context.Background(), tok.LoginToken, 42, "alice"); aerr != nil {
		t.Fatalf("Approve: %v", aerr)
	}
	if aerr := f.flow.Approve(context.Background(), tok.LoginToken, 99, "mallory"); !errors.Is(aerr, ErrFlowAlreadyApproved) {
		t.Fatalf("a second approval returned %v, want a refusal", aerr)
	}

	res, perr := f.flow.Poll(context.Background(), tok.PollToken, "")
	if perr != nil {
		t.Fatalf("Poll: %v", perr)
	}
	if res.LoginName != "alice" {
		t.Fatalf("the second approval changed the account to %q", res.LoginName)
	}
	if f.auth.minted != 1 {
		t.Fatalf("minted %d credentials", f.auth.minted)
	}
}

// After delivery the flow is gone, so a leaked poll token cannot mint a second
// credential later.
func TestASecondPollAfterDeliveryFindsNothing(t *testing.T) {
	f := newFlow(t)
	tok, err := f.flow.Begin(context.Background(), "")
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if aerr := f.flow.Approve(context.Background(), tok.LoginToken, 42, "alice"); aerr != nil {
		t.Fatalf("Approve: %v", aerr)
	}
	if _, perr := f.flow.Poll(context.Background(), tok.PollToken, ""); perr != nil {
		t.Fatalf("Poll: %v", perr)
	}

	*f.nowNs += int64(PollInterval) * 2
	if _, perr := f.flow.Poll(context.Background(), tok.PollToken, ""); !errors.Is(perr, ErrFlowUnknown) {
		t.Fatalf("a second poll returned %v, want ErrFlowUnknown", perr)
	}
	if f.auth.minted != 1 {
		t.Fatalf("a second poll minted another credential: %d total", f.auth.minted)
	}
}

// Knowing the browser token must not let anyone poll.
func TestTheLoginTokenCannotPoll(t *testing.T) {
	f := newFlow(t)
	tok, err := f.flow.Begin(context.Background(), "")
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if aerr := f.flow.Approve(context.Background(), tok.LoginToken, 42, "alice"); aerr != nil {
		t.Fatalf("Approve: %v", aerr)
	}
	if _, perr := f.flow.Poll(context.Background(), tok.LoginToken, ""); !errors.Is(perr, ErrFlowUnknown) {
		t.Fatalf("the login token polled successfully: %v", perr)
	}
	if f.auth.minted != 0 {
		t.Fatalf("the browser token minted %d credentials", f.auth.minted)
	}
}

// And the poll token cannot approve, so a client cannot approve its own flow.
func TestThePollTokenCannotApprove(t *testing.T) {
	f := newFlow(t)
	tok, err := f.flow.Begin(context.Background(), "")
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if aerr := f.flow.Approve(context.Background(), tok.PollToken, 42, "alice"); !errors.Is(aerr, ErrFlowUnknown) {
		t.Fatalf("the poll token approved its own flow: %v", aerr)
	}
}

// An unknown, an expired and an already-taken token are one answer, because
// telling them apart tells a prober which tokens exist.
func TestUnknownAndExpiredAreTheSameAnswer(t *testing.T) {
	f := newFlow(t)
	tok, err := f.flow.Begin(context.Background(), "")
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}

	if _, perr := f.flow.Poll(context.Background(), "a-token-nobody-minted", ""); !errors.Is(perr, ErrFlowUnknown) {
		t.Fatalf("an unknown token returned %v", perr)
	}

	*f.nowNs += int64(FlowTTL) + 1
	if _, perr := f.flow.Poll(context.Background(), tok.PollToken, ""); !errors.Is(perr, ErrFlowUnknown) {
		t.Fatalf("an expired token returned %v, want the same answer as unknown", perr)
	}
}

// An expired flow cannot be approved either, or a stale browser tab would mint
// a credential long after the user forgot about it.
func TestAnExpiredFlowCannotBeApproved(t *testing.T) {
	f := newFlow(t)
	tok, err := f.flow.Begin(context.Background(), "")
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	*f.nowNs += int64(FlowTTL) + 1
	if aerr := f.flow.Approve(context.Background(), tok.LoginToken, 42, "alice"); !errors.Is(aerr, ErrFlowUnknown) {
		t.Fatalf("an expired flow was approved: %v", aerr)
	}
}

// Unbounded polling is a database scan somebody else pays for.
func TestPollingIsRateLimited(t *testing.T) {
	f := newFlow(t)
	tok, err := f.flow.Begin(context.Background(), "")
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}

	if _, perr := f.flow.Poll(context.Background(), tok.PollToken, ""); !errors.Is(perr, ErrFlowPending) {
		t.Fatalf("the first poll returned %v", perr)
	}
	// Immediately again, inside the interval.
	if _, perr := f.flow.Poll(context.Background(), tok.PollToken, ""); !errors.Is(perr, ErrFlowRateLimited) {
		t.Fatalf("a poll inside the interval returned %v, want the limit", perr)
	}
	// And once the interval has passed it works again.
	*f.nowNs += int64(PollInterval) + 1
	if _, perr := f.flow.Poll(context.Background(), tok.PollToken, ""); !errors.Is(perr, ErrFlowPending) {
		t.Fatalf("a poll after the interval returned %v", perr)
	}
}

// The URLs come from configuration, so the set of hosts this can name is fixed
// by an administrator rather than by whoever sent the request.
func TestURLsComeFromTheConfiguredOrigin(t *testing.T) {
	f := newFlow(t)
	tok, err := f.flow.Begin(context.Background(), "")
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	for _, u := range []string{tok.LoginURL, tok.PollEndpoint} {
		if !strings.HasPrefix(u, "https://files.example/") {
			t.Fatalf("%s is not under the configured origin", u)
		}
	}
}

func TestTheBeginPayloadHasTheShapeTheClientReads(t *testing.T) {
	f := newFlow(t)
	tok, err := f.flow.Begin(context.Background(), "")
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	v := tok.BeginJSON()
	if got, ok := v.Path("poll.token"); !ok || got.Str != tok.PollToken {
		t.Fatalf("poll.token = %v", got)
	}
	if got, ok := v.Path("poll.endpoint"); !ok || got.Str != tok.PollEndpoint {
		t.Fatalf("poll.endpoint = %v", got)
	}
	if got, ok := v.Get("login"); !ok || got.Str != tok.LoginURL {
		t.Fatalf("login = %v", got)
	}
}

func TestFlowExpiryIsTwentyMinutes(t *testing.T) {
	if FlowTTL != 20*time.Minute {
		t.Fatalf("FlowTTL = %v, want 20m", FlowTTL)
	}
	if PollInterval != time.Second {
		t.Fatalf("PollInterval = %v, want 1s", PollInterval)
	}
}
