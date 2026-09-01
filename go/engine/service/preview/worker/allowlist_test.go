//go:build linux

package worker_test

import (
	"os/exec"
	"path/filepath"
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
