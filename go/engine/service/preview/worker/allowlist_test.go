//go:build linux

package worker_test

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/service/preview"
)

// The allow list covers a real decode, which the shipped filter cannot report.
//
// A kill prints nothing: a worker missing a syscall dies exactly as a crashed
// decoder does, so the filter is checked here under SIGSYS instead, where an
// unlisted call names itself. The trap is a diagnostic and never a deployment:
// a process that survives its own filter is not sandboxed. What ships is the
// kill, proved by the tests around this one.
//
// Built with the race detector, which the shipped worker is not. That runtime
// issues strictly more, so a decode clean under it is clean under the plain
// one, and the growing sizes are what push the heap into whatever the list is
// missing.
func TestTheAllowListCoversARealDecode(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "raceworker")
	//nolint:gosec // G204: the arguments are this test's own constants.
	build := exec.Command("go", "build", "-race", "-o", bin,
		"github.com/heavycaffeiner/stowcloud/go/engine/service/preview/worker/jailedworker")
	if out, berr := build.CombinedOutput(); berr != nil {
		t.Skipf("the race worker could not be built: %s", out)
	}

	p, err := preview.NewPool(preview.PoolOptions{
		Workers: 1,
		Exe:     bin,
		Args:    []string{},
		Env:     []string{"SC_PREVIEW_TRAP=1"},
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
			t.Fatalf("decoding %dx%d: %v", size, size, gerr)
		}
		if resp.Status != preview.StatusOK {
			t.Fatalf("decoding %dx%d: status %v: %s", size, size, resp.Status, resp.Err)
		}
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
