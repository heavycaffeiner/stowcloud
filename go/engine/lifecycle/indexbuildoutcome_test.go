//go:build linux

package lifecycle

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/clock"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/search"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/search/index"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/search/svc"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/dbfile"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/state"
)

// buildEngine is the smallest engine runIndexBuild reads: a state database, a
// search service holding an index, and a corpus to walk.
//
// Driven directly rather than through the admin route, because what is under
// test is which state a stopped build records and a walk over a real corpus
// finishes before an HTTP client could stop it.
func buildEngine(t *testing.T) (*Engine, []search.Source) {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()

	stf, err := dbfile.Open(ctx, state.Spec(filepath.Join(root, "state.db")))
	if err != nil {
		t.Fatalf("opening the state database: %v", err)
	}
	t.Cleanup(func() {
		if cerr := stf.Close(); cerr != nil {
			t.Errorf("closing the state database: %v", cerr)
		}
	})
	st := state.New(stf)

	// The operation row references an account, so one has to exist before a
	// build can be recorded against it.
	if werr := st.Write(ctx, func(tx *sql.Tx) error {
		_, ierr := tx.ExecContext(ctx,
			`INSERT INTO user(id, name, pw_hash, created_ns) VALUES (1, 'alice', '', 0)`)
		return ierr
	}); werr != nil {
		t.Fatalf("seeding the account: %v", werr)
	}

	ix, opened := svc.OpenIndex(filepath.Join(root, "index"), index.DefaultConfig(), slog.Default())
	if ix == nil {
		t.Fatalf("opening the index answered %v", opened)
	}
	svcSearch := svc.New(svc.Options{Clock: clock.System()})
	svcSearch.SetIndex(ix)

	corpus := filepath.Join(root, "corpus")
	if merr := os.MkdirAll(corpus, 0o755); merr != nil {
		t.Fatal(merr)
	}
	for _, name := range []string{"a.txt", "b.txt"} {
		if werr := os.WriteFile(filepath.Join(corpus, name), []byte("x"), 0o600); werr != nil {
			t.Fatal(werr)
		}
	}
	shareRoot, _, rerr := vfs.RegisterShareRoot(vfs.ShareID(1), corpus, vfs.DefaultSharePolicy())
	if rerr != nil {
		t.Skipf("this host's temp directory is on a filesystem this build refuses: %v", rerr)
	}
	t.Cleanup(func() {
		if cerr := shareRoot.Close(); cerr != nil {
			t.Errorf("closing the share root: %v", cerr)
		}
	})

	jobsCtx, jobsStop := context.WithCancel(context.Background())
	t.Cleanup(jobsStop)
	e := &Engine{
		State:    st,
		Search:   svcSearch,
		clock:    clock.System(),
		logger:   slog.Default(),
		jobsCtx:  jobsCtx,
		jobsStop: jobsStop,
	}
	return e, []search.Source{{Share: 1, Root: shareRoot, Base: vfs.RootPath()}}
}

// A build a shutdown stopped reads as interrupted, not as a finished one.
//
// Every non-error exit used to record done, so a build the server walked away
// from halfway reported a complete index over a partial one and an operator
// had no reason to run it again.
func TestAnIndexBuildStoppedByAShutdownReadsInterrupted(t *testing.T) {
	t.Parallel()
	e, sources := buildEngine(t)
	ctx := context.Background()

	id, err := e.State.CreateOp(ctx, 1, state.OpIndexBuild, 0, 0, nil)
	if err != nil {
		t.Fatalf("creating the operation: %v", err)
	}

	e.jobsStop()
	e.runIndexBuild(ctx, id, sources)

	op, _, err := e.State.GetOp(ctx, id)
	if err != nil {
		t.Fatalf("reading the operation: %v", err)
	}
	if op.State != state.OpInterrupted {
		t.Errorf("a build the shutdown stopped reads %v, want interrupted", op.State)
	}
}

// A build the operator cancelled reads as cancelled.
func TestACancelledIndexBuildReadsCancelled(t *testing.T) {
	t.Parallel()
	e, sources := buildEngine(t)
	ctx := context.Background()

	id, err := e.State.CreateOp(ctx, 1, state.OpIndexBuild, 0, 0, nil)
	if err != nil {
		t.Fatalf("creating the operation: %v", err)
	}
	if cerr := e.State.RequestOpCancel(ctx, id); cerr != nil {
		t.Fatalf("requesting the cancellation: %v", cerr)
	}

	e.runIndexBuild(ctx, id, sources)

	op, _, err := e.State.GetOp(ctx, id)
	if err != nil {
		t.Fatalf("reading the operation: %v", err)
	}
	if op.State != state.OpCancelled {
		t.Errorf("a cancelled build reads %v, want cancelled", op.State)
	}
}

// A build nobody stopped reads as done.
func TestAnUninterruptedIndexBuildReadsDone(t *testing.T) {
	t.Parallel()
	e, sources := buildEngine(t)
	ctx := context.Background()

	id, err := e.State.CreateOp(ctx, 1, state.OpIndexBuild, 0, 0, nil)
	if err != nil {
		t.Fatalf("creating the operation: %v", err)
	}

	e.runIndexBuild(ctx, id, sources)

	op, _, err := e.State.GetOp(ctx, id)
	if err != nil {
		t.Fatalf("reading the operation: %v", err)
	}
	if op.State != state.OpDone {
		t.Errorf("a finished build reads %v, want done", op.State)
	}
	if op.Progress != 2 {
		t.Errorf("a build of two files recorded progress %d", op.Progress)
	}
}
