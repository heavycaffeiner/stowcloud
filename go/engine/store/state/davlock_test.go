package state_test

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sync"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/task"

	"github.com/heavycaffeiner/stowcloud/go/engine/store/ident"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/state"
)

// lockAt builds a lock request.
func lockAt(token, path string, scope, depth int64, principal int64) state.DavLock {
	return state.DavLock{
		Token:     token,
		Ident:     ident.Ident{Share: 1, Dev: 1, Ino: 1},
		Path:      path,
		Principal: principal,
		Depth:     depth,
		Scope:     scope,
		ExpiresNs: 1 << 40,
		TimeoutS:  300,
	}
}

// Hundreds of conflicting exclusive LOCKs against one real database, arriving
// together, must leave exactly one lock.
//
// This runs against the real thing rather than a model, but note what it does
// and does not prove. The database's write path takes a mutex, so requests
// already serialize and this shape passes even with the scan and the insert in
// separate transactions: measured, that split failed here only about one run
// in ten. What this test does establish is that the conflict rule holds under
// real contention, with no lost or duplicated row and no error other than the
// conflict.
//
// The interleaving that a split admission actually loses to is the test below
// this one, which drives it directly.
func TestConcurrentExclusiveLocksAdmitExactlyOne(t *testing.T) {
	d, _ := open(t)
	ctx := context.Background()

	const racers = 800
	for i := 0; i < racers; i++ {
		seedUser(t, d, int64(i+1), fmt.Sprintf("u%d", i))
	}

	// Every goroutine is built and ready before any of them runs, so the
	// requests genuinely overlap rather than queueing behind each other's
	// setup work.
	var (
		start   sync.WaitGroup
		done    sync.WaitGroup
		mu      sync.Mutex
		granted []string
		others  []error
	)
	start.Add(1)
	done.Add(racers)

	for i := 0; i < racers; i++ {
		token := fmt.Sprintf("t%d", i)
		req := lockAt(token, "/contested", state.LockExclusive, state.LockDepthZero, int64(i+1))

		task.Go(ctx, "lock racer", func() {
			defer done.Done()

			start.Wait()
			err := d.AdmitDavLock(ctx, req, 0)

			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				granted = append(granted, token)
			case isConflict(err):
			default:
				others = append(others, err)
			}
		})
	}

	start.Done()
	done.Wait()

	for _, err := range others {
		t.Errorf("a request failed for a reason other than the conflict: %v", err)
	}
	if len(granted) != 1 {
		t.Fatalf("%d exclusive locks were granted on one path, want exactly 1: %v", len(granted), granted)
	}

	live, err := d.DavLocks(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 1 {
		t.Errorf("the table holds %d locks after the race, want 1", len(live))
	}
	if len(live) == 1 && live[0].Token != granted[0] {
		t.Errorf("the stored lock is %q but %q was told it won", live[0].Token, granted[0])
	}
}

// The decision and the write are one transaction, checked structurally
// because no timing test can establish it reliably.
//
// The database's write path holds a mutex for the whole callback, so any two
// Write calls are strictly serialized. That hides the difference: splitting
// the scan from the insert is a real defect, since another admission can slot
// its own scan into the gap, but it only loses when the scheduler arranges
// exactly that order. Measured against the 800-racer test above, a split
// admission is caught three runs in fifteen, which is coverage rather than
// proof.
//
// So the property is read off the source. AdmitDavLock calls Write once, and
// the conflict scan, the count and the insert are all inside that one call.
func TestAdmissionIsOneTransaction(t *testing.T) {
	src := filepath.Join("davlock.go")

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, src, nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", src, err)
	}

	var body *ast.BlockStmt
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Name.Name == "AdmitDavLock" {
			body = fn.Body
		}
	}
	if body == nil {
		t.Fatal("AdmitDavLock is not in davlock.go; if it was renamed, this check watches nothing")
	}

	// Every call, with the Write calls marked, so the assertions below can
	// speak about position as well as count.
	var writes []*ast.CallExpr
	inside := map[string]bool{}

	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if name := calleeName(call); name == "d.Write" {
			writes = append(writes, call)
		}
		return true
	})

	if len(writes) != 1 {
		t.Fatalf("AdmitDavLock makes %d Write calls, want exactly 1: a second one is a window another admission can commit into", len(writes))
	}

	// And the three steps are inside it. A scan that ran outside the
	// transaction would decide against a table the insert never rechecks.
	ast.Inspect(writes[0], func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			inside[calleeName(call)] = true
		}
		return true
	})

	for _, step := range []string{"liveLocksInShare", "tx.ExecContext", "tx.QueryRowContext"} {
		if !inside[step] {
			t.Errorf("%s does not run inside the transaction", step)
		}
	}
}

// calleeName renders a call's target as pkg.Func or Func.
func calleeName(call *ast.CallExpr) string {
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		return fn.Name
	case *ast.SelectorExpr:
		if x, ok := fn.X.(*ast.Ident); ok {
			return x.Name + "." + fn.Sel.Name
		}
		return fn.Sel.Name
	default:
		return ""
	}
}

// Concurrent shared locks on one path all succeed. If the admission were a
// blanket refusal of any second lock, this would fail, so the two tests
// together show the conflict rule and not just a lock counter.
func TestConcurrentSharedLocksAllCoexist(t *testing.T) {
	d, _ := open(t)
	ctx := context.Background()

	const racers = 50
	for i := 0; i < racers; i++ {
		seedUser(t, d, int64(i+1), fmt.Sprintf("u%d", i))
	}

	var (
		start sync.WaitGroup
		done  sync.WaitGroup
		mu    sync.Mutex
		fails []error
	)
	start.Add(1)
	done.Add(racers)

	for i := 0; i < racers; i++ {
		req := lockAt(fmt.Sprintf("s%d", i), "/shared", state.LockShared, state.LockDepthZero, int64(i+1))

		task.Go(ctx, "shared lock racer", func() {
			defer done.Done()

			start.Wait()
			if err := d.AdmitDavLock(ctx, req, 0); err != nil {
				mu.Lock()
				fails = append(fails, err)
				mu.Unlock()
			}
		})
	}

	start.Done()
	done.Wait()

	for _, err := range fails {
		t.Errorf("a shared lock was refused: %v", err)
	}

	live, err := d.DavLocks(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != racers {
		t.Errorf("%d shared locks coexist, want %d", len(live), racers)
	}
}

// The conflict matrix, over one database rather than the predicate alone.
func TestTheLockConflictMatrixOverTheDatabase(t *testing.T) {
	cases := []struct {
		name       string
		heldScope  int64
		wantScope  int64
		wantsAdmit bool
	}{
		{"shared against shared", state.LockShared, state.LockShared, true},
		{"exclusive against shared", state.LockShared, state.LockExclusive, false},
		{"shared against exclusive", state.LockExclusive, state.LockShared, false},
		{"exclusive against exclusive", state.LockExclusive, state.LockExclusive, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d, _ := open(t)
			ctx := context.Background()
			seedUser(t, d, 1, "a")
			seedUser(t, d, 2, "b")

			held := lockAt("held", "/p", c.heldScope, state.LockDepthZero, 1)
			if err := d.AdmitDavLock(ctx, held, 0); err != nil {
				t.Fatalf("the first lock was refused: %v", err)
			}

			want := lockAt("want", "/p", c.wantScope, state.LockDepthZero, 2)
			err := d.AdmitDavLock(ctx, want, 0)

			if c.wantsAdmit && err != nil {
				t.Errorf("the second lock was refused: %v", err)
			}
			if !c.wantsAdmit && !isConflict(err) {
				t.Errorf("the second lock gave %v, want a conflict", err)
			}
		})
	}
}

// Coverage: an ancestor with depth infinity blocks a descendant, a requested
// depth-infinity lock is blocked by a descendant, and a prefix that is not a
// path boundary blocks nothing.
func TestWhatALockCovers(t *testing.T) {
	cases := []struct {
		name      string
		heldPath  string
		heldDepth int64
		wantPath  string
		wantDepth int64
		conflicts bool
	}{
		{"the same path", "/a", state.LockDepthZero, "/a", state.LockDepthZero, true},
		{"a depth-infinity ancestor", "/a", state.LockDepthInfinity, "/a/b/c", state.LockDepthZero, true},
		{"a depth-zero ancestor covers nothing below", "/a", state.LockDepthZero, "/a/b", state.LockDepthZero, false},
		{"a descendant blocks a depth-infinity request", "/a/b", state.LockDepthZero, "/a", state.LockDepthInfinity, true},
		{"an unrelated path", "/a", state.LockDepthInfinity, "/b", state.LockDepthZero, false},
		{"a prefix that is not a boundary", "/a", state.LockDepthInfinity, "/ab", state.LockDepthZero, false},
		{"a longer prefix that is not a boundary", "/foo", state.LockDepthInfinity, "/foobar/x", state.LockDepthZero, false},
		{"the root covers everything", "/", state.LockDepthInfinity, "/anything/deep", state.LockDepthZero, true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d, _ := open(t)
			ctx := context.Background()
			seedUser(t, d, 1, "a")
			seedUser(t, d, 2, "b")

			held := lockAt("held", c.heldPath, state.LockExclusive, c.heldDepth, 1)
			if err := d.AdmitDavLock(ctx, held, 0); err != nil {
				t.Fatalf("the first lock was refused: %v", err)
			}

			want := lockAt("want", c.wantPath, state.LockExclusive, c.wantDepth, 2)
			err := d.AdmitDavLock(ctx, want, 0)

			if c.conflicts && !isConflict(err) {
				t.Errorf("%s: got %v, want a conflict", c.name, err)
			}
			if !c.conflicts && err != nil {
				t.Errorf("%s: got %v, want admission", c.name, err)
			}
		})
	}
}

// An expired lock blocks nothing, even before the periodic sweep runs. A
// deadline that passed is not a lock anyone is relying on.
func TestAnExpiredLockDoesNotBlock(t *testing.T) {
	d, _ := open(t)
	ctx := context.Background()
	seedUser(t, d, 1, "a")
	seedUser(t, d, 2, "b")

	old := lockAt("old", "/p", state.LockExclusive, state.LockDepthZero, 1)
	old.ExpiresNs = 1000
	if err := d.AdmitDavLock(ctx, old, 0); err != nil {
		t.Fatalf("the first lock was refused: %v", err)
	}

	// Now, past that deadline.
	fresh := lockAt("fresh", "/p", state.LockExclusive, state.LockDepthZero, 2)
	if err := d.AdmitDavLock(ctx, fresh, 2000); err != nil {
		t.Errorf("an expired lock blocked a new one: %v", err)
	}

	live, err := d.DavLocks(ctx, 2000)
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 1 || live[0].Token != "fresh" {
		t.Errorf("live locks are %+v, want only the fresh one", live)
	}
}

// A lock in another share does not block one here. Shares are separate trees
// and a path string means nothing across them.
func TestALockInAnotherShareDoesNotBlock(t *testing.T) {
	d, _ := open(t)
	ctx := context.Background()
	seedUser(t, d, 1, "a")
	seedUser(t, d, 2, "b")

	held := lockAt("held", "/p", state.LockExclusive, state.LockDepthInfinity, 1)
	if err := d.AdmitDavLock(ctx, held, 0); err != nil {
		t.Fatalf("the first lock was refused: %v", err)
	}

	other := lockAt("other", "/p", state.LockExclusive, state.LockDepthZero, 2)
	other.Ident = ident.Ident{Share: 2, Dev: 1, Ino: 1}
	if err := d.AdmitDavLock(ctx, other, 0); err != nil {
		t.Errorf("a lock in another share was refused: %v", err)
	}
}

// isConflict reports whether an error is the lock conflict.
func isConflict(err error) bool {
	return errors.Is(err, state.ErrLockConflict)
}
