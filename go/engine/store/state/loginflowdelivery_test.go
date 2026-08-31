package state_test

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"sync"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/task"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/state"
)

// startFlow inserts an approved flow and returns its poll digest.
func startLoginFlow(t *testing.T, d *state.DB, name string, approve bool) []byte {
	t.Helper()
	ctx := context.Background()

	poll := []byte("poll-" + name)
	login := []byte("login-" + name)
	if err := d.PutLoginFlow(ctx, state.LoginFlowRow{
		PollDigest: poll, LoginDigest: login, CreatedNs: 1000,
	}); err != nil {
		t.Fatalf("starting a flow: %v", err)
	}
	if approve {
		if err := d.ApproveLoginFlow(ctx, login, 1, "alice"); err != nil {
			t.Fatalf("approving: %v", err)
		}
	}
	return poll
}

// Exactly one poll may mint. Two approved polls arriving together would both
// pass a check made before either wrote, and the client would end up holding
// two credentials with no way to know the second exists.
func TestExactlyOnePollClaimsDelivery(t *testing.T) {
	d, _ := open(t)
	ctx := context.Background()
	seedUser(t, d, 1, "alice")
	poll := startLoginFlow(t, d, "race", true)

	const racers = 200
	var (
		start   sync.WaitGroup
		done    sync.WaitGroup
		mu      sync.Mutex
		claimed int
		others  []error
	)
	start.Add(1)
	done.Add(racers)

	for i := 0; i < racers; i++ {
		task.Go(ctx, "delivery claimer", func() {
			defer done.Done()

			start.Wait()
			err := d.ClaimLoginFlowDelivery(ctx, poll, 2000)

			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				claimed++
			case errors.Is(err, state.ErrLoginFlowClaimed):
			default:
				others = append(others, err)
			}
		})
	}

	start.Done()
	done.Wait()

	for _, err := range others {
		t.Errorf("a claim failed for a reason other than the race: %v", err)
	}
	if claimed != 1 {
		t.Fatalf("%d of %d polls claimed delivery, want exactly 1", claimed, racers)
	}
}

// The claim guard is inside the statement, checked structurally because the
// database's write path serializes and a timing test cannot separate one
// statement from a read followed by a write.
func TestTheDeliveryClaimIsOneStatement(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "loginflow_sql.go", nil, 0)
	if err != nil {
		t.Fatalf("parsing the SQL: %v", err)
	}

	var claim string
	ast.Inspect(file, func(n ast.Node) bool {
		spec, ok := n.(*ast.ValueSpec)
		if !ok || len(spec.Names) == 0 || spec.Names[0].Name != "sqlClaimLoginFlowDelivery" {
			return true
		}
		if len(spec.Values) == 1 {
			if lit, ok := spec.Values[0].(*ast.BasicLit); ok {
				claim = lit.Value
			}
		}
		return false
	})

	if claim == "" {
		t.Fatal("sqlClaimLoginFlowDelivery is gone; if it was renamed, this check watches nothing")
	}
	for _, want := range []string{"claimed_ns = 0", "approved_user IS NOT NULL"} {
		if !contains(claim, want) {
			t.Errorf("the claim statement does not carry %q: %s", want, claim)
		}
	}
}

// contains reports substring presence without importing strings for one use.
func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

// An unapproved flow cannot be claimed. Minting before a person approved would
// hand a credential to whoever started the flow.
func TestAnUnapprovedFlowCannotBeClaimed(t *testing.T) {
	d, _ := open(t)
	ctx := context.Background()
	seedUser(t, d, 1, "alice")
	poll := startLoginFlow(t, d, "pending", false)

	if err := d.ClaimLoginFlowDelivery(ctx, poll, 2000); !errors.Is(err, state.ErrLoginFlowNotApproved) {
		t.Errorf("an unapproved flow was claimed: %v", err)
	}
}

// A flow nobody started is unknown, and that answer is the same one an expired
// or consumed flow gets: a caller cannot tell them apart by the error.
func TestAnAbsentFlowIsUnknown(t *testing.T) {
	d, _ := open(t)
	ctx := context.Background()

	if err := d.ClaimLoginFlowDelivery(ctx, []byte("nothing"), 2000); !errors.Is(err, state.ErrLoginFlowUnknown) {
		t.Errorf("an absent flow gave %v", err)
	}
	if _, err := d.LoginFlowDeliveryState(ctx, []byte("nothing")); !errors.Is(err, state.ErrLoginFlowUnknown) {
		t.Errorf("reading an absent flow gave %v", err)
	}
}

// The sealed result survives being read, so the same poll token collects the
// same credential rather than minting a second one.
func TestASealedResultIsCollectedRepeatedly(t *testing.T) {
	d, _ := open(t)
	ctx := context.Background()
	seedUser(t, d, 1, "alice")
	poll := startLoginFlow(t, d, "deliver", true)

	if err := d.ClaimLoginFlowDelivery(ctx, poll, 2000); err != nil {
		t.Fatal(err)
	}

	sealed := []byte("sealed-ciphertext")
	if err := d.StoreLoginFlowDelivery(ctx, poll, sealed, 7, 42); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 5; i++ {
		got, err := d.LoginFlowDeliveryState(ctx, poll)
		if err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
		if !got.HasResult() {
			t.Fatalf("read %d found no result", i)
		}
		if string(got.Sealed) != string(sealed) {
			t.Errorf("read %d gave %q", i, got.Sealed)
		}
		if got.SealedKeyVer != 7 {
			t.Errorf("read %d gave key version %d", i, got.SealedKeyVer)
		}
		if got.CredentialID == nil || *got.CredentialID != 42 {
			t.Errorf("read %d gave credential %v", i, got.CredentialID)
		}
	}
}

// Marking a delivery does not clear the sealed result. A connection lost after
// the server wrote its response is exactly the case redelivery exists for, and
// clearing here makes the client's retry mint a second credential.
func TestMarkingDeliveredKeepsTheResult(t *testing.T) {
	d, _ := open(t)
	ctx := context.Background()
	seedUser(t, d, 1, "alice")
	poll := startLoginFlow(t, d, "retry", true)

	if err := d.ClaimLoginFlowDelivery(ctx, poll, 2000); err != nil {
		t.Fatal(err)
	}
	if err := d.StoreLoginFlowDelivery(ctx, poll, []byte("ct"), 1, 9); err != nil {
		t.Fatal(err)
	}
	if err := d.MarkLoginFlowDelivered(ctx, poll, 3000); err != nil {
		t.Fatal(err)
	}

	got, err := d.LoginFlowDeliveryState(ctx, poll)
	if err != nil {
		t.Fatal(err)
	}
	if !got.HasResult() {
		t.Error("marking delivered destroyed the credential a retry needs")
	}
	if got.DeliveredNs != 3000 {
		t.Errorf("delivered at %d", got.DeliveredNs)
	}
}

// The key version is recorded, so a rotation does not strand a flow that is
// already deliverable.
func TestTheKeyVersionIsRecordedWithTheCiphertext(t *testing.T) {
	d, _ := open(t)
	ctx := context.Background()
	seedUser(t, d, 1, "alice")

	for _, ver := range []uint32{0, 1, 65535, 4294967295} {
		poll := startLoginFlow(t, d, fmt.Sprintf("ver%d", ver), true)
		if err := d.ClaimLoginFlowDelivery(ctx, poll, 2000); err != nil {
			t.Fatal(err)
		}
		if err := d.StoreLoginFlowDelivery(ctx, poll, []byte("ct"), ver, 1); err != nil {
			t.Fatal(err)
		}

		got, err := d.LoginFlowDeliveryState(ctx, poll)
		if err != nil {
			t.Fatal(err)
		}
		if got.SealedKeyVer != ver {
			t.Errorf("key version %d came back as %d", ver, got.SealedKeyVer)
		}
	}
}

// Sweep removes the server's copy of the secret and leaves the row. Dropping
// the row would lose the record that a credential was minted, and the
// credential itself belongs to the client now.
func TestSweepClearsTheCiphertextAndKeepsTheRow(t *testing.T) {
	d, _ := open(t)
	ctx := context.Background()
	seedUser(t, d, 1, "alice")
	poll := startLoginFlow(t, d, "sweep", true)

	if err := d.ClaimLoginFlowDelivery(ctx, poll, 2000); err != nil {
		t.Fatal(err)
	}
	if err := d.StoreLoginFlowDelivery(ctx, poll, []byte("ct"), 3, 11); err != nil {
		t.Fatal(err)
	}

	n, err := d.SweepLoginFlowMaterial(ctx, 5000)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("swept %d rows, want 1", n)
	}

	got, err := d.LoginFlowDeliveryState(ctx, poll)
	if err != nil {
		t.Fatalf("the row is gone: %v", err)
	}
	if got.HasResult() {
		t.Error("the ciphertext survived the sweep")
	}
	if got.CredentialID == nil || *got.CredentialID != 11 {
		t.Errorf("the credential record was lost: %v", got.CredentialID)
	}
}

// A flow newer than the cutoff keeps its material.
func TestSweepLeavesALiveFlowAlone(t *testing.T) {
	d, _ := open(t)
	ctx := context.Background()
	seedUser(t, d, 1, "alice")
	poll := startLoginFlow(t, d, "live", true)

	if err := d.ClaimLoginFlowDelivery(ctx, poll, 2000); err != nil {
		t.Fatal(err)
	}
	if err := d.StoreLoginFlowDelivery(ctx, poll, []byte("ct"), 1, 5); err != nil {
		t.Fatal(err)
	}

	if _, err := d.SweepLoginFlowMaterial(ctx, 500); err != nil {
		t.Fatal(err)
	}

	got, err := d.LoginFlowDeliveryState(ctx, poll)
	if err != nil {
		t.Fatal(err)
	}
	if !got.HasResult() {
		t.Error("a live flow lost its material")
	}
}

// Storing against a flow that does not exist is refused rather than silently
// creating one.
func TestStoringAgainstAnAbsentFlowIsRefused(t *testing.T) {
	d, _ := open(t)

	err := d.StoreLoginFlowDelivery(context.Background(), []byte("nothing"), []byte("ct"), 1, 1)
	if !errors.Is(err, state.ErrLoginFlowUnknown) {
		t.Errorf("storing against an absent flow gave %v", err)
	}
}

// The database holds no plaintext. What goes in sealed comes out sealed, and
// nothing writes a password column: a read of this table says which sign-ins
// are underway without providing any means to complete one.
func TestNoPlaintextReachesTheTable(t *testing.T) {
	d, f := open(t)
	ctx := context.Background()
	seedUser(t, d, 1, "alice")
	poll := startLoginFlow(t, d, "plain", true)

	if err := d.ClaimLoginFlowDelivery(ctx, poll, 2000); err != nil {
		t.Fatal(err)
	}
	if err := d.StoreLoginFlowDelivery(ctx, poll, []byte("sealed-bytes"), 1, 3); err != nil {
		t.Fatal(err)
	}

	// Walk the row's every text and blob column looking for a marker that
	// would only appear if something stored a secret unsealed.
	rows, err := f.SQL().QueryContext(ctx, `SELECT * FROM compat_login_flow`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	}()

	cols, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range cols {
		switch name {
		case "app_password", "password", "token", "plaintext", "secret":
			t.Errorf("the table has a %q column", name)
		}
	}

	for rows.Next() {
		cells := make([]any, len(cols))
		for i := range cells {
			cells[i] = new(any)
		}
		if err := rows.Scan(cells...); err != nil {
			t.Fatal(err)
		}
		for i, cell := range cells {
			var text string
			switch v := (*(cell.(*any))).(type) {
			case string:
				text = v
			case []byte:
				text = string(v)
			default:
				continue
			}
			if cols[i] != "sealed_result" && contains(text, "sealed-bytes") {
				t.Errorf("column %q carries the delivery material", cols[i])
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}
