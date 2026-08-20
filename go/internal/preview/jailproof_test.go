//go:build linux

package preview_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/internal/jail"
	"github.com/heavycaffeiner/stowcloud/go/internal/preview"
	"github.com/heavycaffeiner/stowcloud/go/internal/preview/worker"
)

// The jail proof.
//
// A security claim that cannot be executed is a comment, so this drives a real
// worker inside the real jail and asks it to attempt the things the jail is
// supposed to prevent. Every probe must come back refused or kill the worker.
// A probe reporting success is a test failure.
//
// This is the one test that needs the kernel features rather than the code, so
// it skips where they are absent and says which one was missing. On a kernel
// that has them it is required.

// jailedWorker builds the real product binary, whose preview-worker subcommand
// applies the jail. Not the test helper: that one deliberately skips the jail,
// and proving the jail against a worker that never entered it would prove
// nothing at all.
func jailedWorker(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("building the product binary is not a short test")
	}

	bin := filepath.Join(t.TempDir(), "stowcloud")
	//nolint:gosec // G204: constant arguments and a path under t.TempDir.
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/stowcloud")
	cmd.Dir = moduleRoot(t)
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building the product binary: %v\n%s", err, out)
	}
	return bin
}

func requireJail(t *testing.T) {
	t.Helper()
	abi, err := jail.ABIVersion()
	if err != nil || abi < 1 {
		t.Skipf("this kernel reports no usable Landlock ABI (%v, %d); "+
			"the jail proof needs one", err, abi)
	}
}

// probe sends one probe to a jailed worker and reports what came back.
//
// A worker killed by the probe answers with ErrWorkerDied, which is a pass:
// the kernel removed the process rather than letting the call through.
func probe(t *testing.T, p worker.Probe) (preview.Response, error) {
	t.Helper()

	pool, err := preview.NewPool(preview.PoolOptions{
		Workers: 1,
		Exe:     jailedWorker(t),
		Args:    []string{"preview-worker"},
		Env:     []string{"HOME=" + os.Getenv("HOME"), "PATH=" + os.Getenv("PATH")},
	})
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(func() {
		if cerr := pool.Close(); cerr != nil {
			t.Errorf("closing the pool: %v", cerr)
		}
	})

	in, out := job(t, []byte("unused by a probe"))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	return pool.Generate(ctx, preview.Request{
		Kind:   preview.JobProbe,
		Preset: preview.Preset(p),
	}, preview.PlainSource{F: in}, out)
}

// assertRefusedOrKilled is the whole contract. Succeeded is a failure.
func assertRefusedOrKilled(t *testing.T, p worker.Probe) {
	t.Helper()
	resp, err := probe(t, p)

	if err != nil {
		if errors.Is(err, preview.ErrWorkerDied) {
			// The kernel killed it, which is the strongest of the two passes.
			t.Logf("%v: the worker was killed", p)
			return
		}
		t.Fatalf("%v: %v", p, err)
	}

	outcome := worker.OutcomeFrom(resp.Width)
	switch outcome {
	case worker.OutcomeRefused:
		t.Logf("%v: refused (%s)", p, resp.Err)
	case worker.OutcomeSucceeded:
		t.Fatalf("%v SUCCEEDED inside the jail: %s\n"+
			"the jail did not prevent what it exists to prevent", p, resp.Err)
	default:
		t.Fatalf("%v: unexpected outcome %v (%s)", p, outcome, resp.Err)
	}
}

// The transport works, so a run where every probe was killed can be told from
// one where the socket was never connected. Without this, a completely broken
// harness would look like a perfect jail.
func TestTheProbeTransportWorks(t *testing.T) {
	requireJail(t)

	resp, err := probe(t, worker.ProbePing)
	if err != nil {
		t.Fatalf("the ping probe failed: %v\n"+
			"every other probe in this file is meaningless if this one does not pass", err)
	}
	if worker.OutcomeFrom(resp.Width) != worker.OutcomeCompleted {
		t.Fatalf("ping returned %v (%s), want completed",
			worker.OutcomeFrom(resp.Width), resp.Err)
	}
}

// The worker is never told a path, and openat is not on the allow-list, so a
// path-traversal bug in a decoder has nothing to traverse.
func TestTheJailedWorkerCannotOpenAFileByName(t *testing.T) {
	requireJail(t)
	assertRefusedOrKilled(t, worker.ProbeOpenEtcPasswd)
}

// No socket and no connect, so a decoder that is executing an attacker's code
// cannot reach the network with it.
func TestTheJailedWorkerCannotCreateASocket(t *testing.T) {
	requireJail(t)
	assertRefusedOrKilled(t, worker.ProbeCreateSocket)
}

// No clone and no execve, so there is no second process to escape into.
func TestTheJailedWorkerCannotFork(t *testing.T) {
	requireJail(t)
	assertRefusedOrKilled(t, worker.ProbeFork)
}

// A worker that will not stop is killed at the deadline, which is what makes a
// decoder stuck in a loop cost one thumbnail rather than a slot forever.
func TestASpinningJailedWorkerIsKilled(t *testing.T) {
	requireJail(t)

	pool, err := preview.NewPool(preview.PoolOptions{
		Workers: 1,
		Exe:     jailedWorker(t),
		Args:    []string{"preview-worker"},
		Env:     []string{"HOME=" + os.Getenv("HOME"), "PATH=" + os.Getenv("PATH")},
	})
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(func() {
		if cerr := pool.Close(); cerr != nil {
			t.Errorf("closing the pool: %v", cerr)
		}
	})

	in, out := job(t, []byte("unused by a probe"))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if _, err := pool.Generate(ctx, preview.Request{
		Kind:   preview.JobProbe,
		Preset: preview.Preset(worker.ProbeSpin),
	}, preview.PlainSource{F: in}, out); err == nil {
		t.Fatal("a spinning worker was allowed to finish, so the deadline does nothing")
	}

	// And the pool serves the next job, which is the whole point of killing
	// rather than waiting.
	in2, out2 := job(t, pngOf(t, 32, 32))
	ctx2, cancel2 := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel2()
	resp, gerr := pool.Generate(ctx2, preview.Request{
		Kind: preview.JobImage, Preset: preview.PresetSmall, Flags: preview.FlagStripEXIF,
	}, preview.PlainSource{F: in2}, out2)
	if gerr != nil {
		t.Fatalf("the job after a killed spin: %v", gerr)
	}
	if resp.Status != preview.StatusOK {
		t.Fatalf("status = %v (%s)", resp.Status, resp.Err)
	}
}

// The jail does not stop the worker doing its job, which is the other half of
// the claim: a sandbox that also prevented decoding would pass every probe
// above and be useless.
func TestTheJailedWorkerStillDecodes(t *testing.T) {
	requireJail(t)

	pool, err := preview.NewPool(preview.PoolOptions{
		Workers: 1,
		Exe:     jailedWorker(t),
		Args:    []string{"preview-worker"},
		Env:     []string{"HOME=" + os.Getenv("HOME"), "PATH=" + os.Getenv("PATH")},
	})
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(func() {
		if cerr := pool.Close(); cerr != nil {
			t.Errorf("closing the pool: %v", cerr)
		}
	})

	in, out := job(t, pngOf(t, 400, 300))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	resp, gerr := pool.Generate(ctx, preview.Request{
		Kind: preview.JobImage, Preset: preview.PresetSmall, Flags: preview.FlagStripEXIF,
	}, preview.PlainSource{F: in}, out)
	if gerr != nil {
		t.Fatalf("a jailed worker could not decode: %v\n"+
			"the seccomp allow-list is measured; a kill here means it is missing an entry", gerr)
	}
	if resp.Status != preview.StatusOK {
		t.Fatalf("status = %v (%s)", resp.Status, resp.Err)
	}
	if resp.Bytes == 0 {
		t.Fatal("the jailed worker wrote nothing")
	}
}
