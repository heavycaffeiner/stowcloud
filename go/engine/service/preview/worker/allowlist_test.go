//go:build linux

package worker_test

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/service/preview"
)

// saidBuffer collects a worker's stderr.
//
// os/exec copies a child's stderr on a goroutine of its own, so a test reading
// what the worker said while it is still running races that writer. The lock
// is what lets a failure message quote the output at the moment it fails,
// rather than only after the pool is closed.
type saidBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *saidBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *saidBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// The allow list covers a real decode, which the shipped filter cannot report.
//
// A kill prints nothing: a worker missing a syscall dies exactly as a crashed
// decoder does. The worker runs here with the refusal turned into ENOSYS
// instead, which the runtime reports through its ordinary error paths, so the
// failure arrives as an exit the parent reads rather than as a stack on a
// stderr the harness may drop. That is not hypothetical: the SIGSYS variant
// named a missing syscall locally and printed nothing at all in CI, where the
// same failure read "bad system call" and no more.
//
// Neither mode ships. What ships is the kill, proved by the tests around this
// one; a worker that survives its own filter is not sandboxed.
//
// The worker is built the way it ships. An earlier version built it with the
// race detector, reasoning that a runtime issuing strictly more makes a
// stricter proof. It makes a false one: the detector's shadow memory calls
// mprotect, which the shipped worker never issues, so the test failed over a
// syscall no deployment can reach. Satisfying it would have widened the real
// filter to admit a call only a test needed.
func TestTheAllowListCoversARealDecode(t *testing.T) {
	// Captured rather than inherited: a worker refused a syscall names it on
	// its own stderr, and that is the whole diagnostic. Left on the parent's
	// it reached the terminal locally and vanished in CI.
	var said saidBuffer
	p, err := preview.NewPool(preview.PoolOptions{
		Workers: 1,
		Exe:     buildJailedWorker(t),
		Args:    []string{},
		Env:     []string{"SC_PREVIEW_ERRNO=1"},
		Stderr:  &said,
	})
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(func() {
		if cerr := p.Close(); cerr != nil {
			t.Errorf("closing the pool: %v", cerr)
		}
	})

	// Sizes that grow the heap, which is where an unlisted call surfaces.
	for _, size := range []int{32, 256, 1024, 2048} {
		in, out := sourceFile(t, size, size), outputFile(t)
		resp, gerr := p.Generate(t.Context(), preview.Request{
			Kind:      preview.JobImage,
			Preset:    preview.PresetSmall,
			Flags:     preview.FlagStripEXIF,
			MaxPixels: 1 << 22,
		}, preview.PlainSource{F: in}, out)
		if gerr != nil {
			t.Fatalf("decoding %dx%d: %v\nthe worker said:\n%s", size, size, gerr, said.String())
		}
		if resp.Status != preview.StatusOK {
			t.Fatalf("decoding %dx%d: status %v: %s\nthe worker said:\n%s",
				size, size, resp.Status, resp.Err, said.String())
		}
	}
}

// The capture that carries the diagnostic works.
//
// The proof above reports what a dying worker said, and reports nothing if the
// pipe between them breaks. That failure is silent: the test still fails, on
// "exit status 2" with no cause, which is the state this whole diagnostic
// exists to leave behind. So the pipe is exercised directly, with the runtime
// asked to write a line every worker produces regardless of the decode.
func TestAWorkerSaysWhyItDied(t *testing.T) {
	var said saidBuffer
	p, err := preview.NewPool(preview.PoolOptions{
		Workers: 1,
		Exe:     buildJailedWorker(t),
		Args:    []string{},
		// schedtrace writes to stderr on a fixed interval, so the worker is
		// certain to produce something whether or not it dies.
		Env:    []string{"GODEBUG=schedtrace=1"},
		Stderr: &said,
	})
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(func() {
		if cerr := p.Close(); cerr != nil {
			t.Errorf("closing the pool: %v", cerr)
		}
	})

	in, out := sourceFile(t, 64, 64), outputFile(t)
	if _, gerr := p.Generate(t.Context(), preview.Request{
		Kind:      preview.JobImage,
		Preset:    preview.PresetSmall,
		Flags:     preview.FlagStripEXIF,
		MaxPixels: 1 << 20,
	}, preview.PlainSource{F: in}, out); gerr != nil {
		t.Fatalf("decoding: %v", gerr)
	}

	if !strings.Contains(said.String(), "SCHED") {
		t.Errorf("nothing the worker wrote reached the capture, so a refused"+
			" syscall would report no cause; got %q", said.String())
	}
}

// The runtime still starts threads with clone rather than clone3.
//
// The filter admits clone by inspecting CLONE_THREAD in its arguments. clone3
// cannot be gated that way at all: its flags live in a struct in userspace,
// which seccomp cannot read, so a filter can only take the number or leave it.
// Taking it would admit fork, which is the whole property; leaving it kills a
// worker whose runtime has switched.
//
// So a toolchain that moves the scheduler to clone3 breaks the jail, and it
// breaks it the way the last one did: a worker that dies mid-decode with
// nothing to read. This reads the number out of the runtime's own thread
// spawner, so the upgrade that changes it fails here instead.
//
// Reading the binary rather than driving load until a thread starts. Load does
// not reliably start one: with 64 concurrent jobs against a single worker,
// measured over ten runs, two peaked below the six threads the worker already
// had and never cloned at all. A test that catches the switch eight times in
// ten is not one to put a sandbox behind. It also costs 0.3s against the trap
// test's 1.9s, because it compiles once and never decodes.
func TestTheRuntimeStartsThreadsWithClone(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "worker")
	//nolint:gosec // G204: the arguments are this test's own constants.
	build := exec.Command("go", "build", "-o", bin,
		"github.com/heavycaffeiner/stowcloud/go/engine/service/preview/worker/jailedworker")
	if out, berr := build.CombinedOutput(); berr != nil {
		t.Skipf("the worker could not be built: %s", out)
	}

	//nolint:gosec // G204: the arguments are this test's own constants.
	dump := exec.Command("go", "tool", "objdump", "-s", "runtime.clone", bin)
	out, derr := dump.Output()
	if derr != nil {
		t.Skipf("this build cannot be disassembled: %v", derr)
	}
	// The number reaches the kernel in a register the ABI fixes, and the
	// disassembly names it differently per architecture: amd64 loads AX in
	// hex, arm64 loads R8 in decimal. Both spellings are here because the
	// filter ships on both.
	var wantClone, clone3 string
	switch runtime.GOARCH {
	case "amd64":
		wantClone, clone3 = "$0x38, AX", "$0x1b3, AX"
	case "arm64":
		wantClone, clone3 = "$220, R8", "$435, R8"
	default:
		t.Skipf("no known clone encoding for %s", runtime.GOARCH)
	}

	body := string(out)
	if strings.Contains(body, clone3) {
		t.Error("the runtime issues clone3, whose flags live in a userspace struct the filter cannot read")
	}
	if !strings.Contains(body, wantClone) {
		t.Errorf("runtime.clone does not issue clone; the filter's gate no longer describes it:\n%s", body)
	}
}
