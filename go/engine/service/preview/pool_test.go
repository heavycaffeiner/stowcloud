//go:build linux

package preview

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/task"
)

// buildTestWorker compiles the unjailed worker once per run. The jail is
// proved separately against a real kernel; these tests are about what the
// parent does when a worker dies or stops answering.
func buildTestWorker(t *testing.T) string {
	t.Helper()
	testWorkerOnce.Do(func() {
		dir, err := os.MkdirTemp("", "previewworker")
		if err != nil {
			testWorkerErr = err
			return
		}
		bin := filepath.Join(dir, "testworker")
		//nolint:gosec // G204: the arguments are this test's own constants.
		cmd := exec.Command("go", "build", "-o", bin,
			"github.com/heavycaffeiner/stowcloud/go/engine/service/preview/worker/testworker")
		if out, berr := cmd.CombinedOutput(); berr != nil {
			testWorkerErr = errors.New(string(out))
			return
		}
		testWorkerBin = bin
	})
	if testWorkerErr != nil {
		t.Skipf("the test worker could not be built: %v", testWorkerErr)
	}
	return testWorkerBin
}

//nolint:gochecknoglobals // one build per test binary, which is what sync.Once is for.
var (
	testWorkerOnce sync.Once
	testWorkerBin  string
	testWorkerErr  error
)

// newPool builds a pool over the unjailed worker. mode drives the two failure
// modes the parent's reap and deadline paths exist to handle.
func newPool(t *testing.T, workers int, mode string) *Pool {
	t.Helper()
	env := []string{}
	if mode != "" {
		env = append(env, "HELPER_MODE="+mode)
	}
	p, err := NewPool(PoolOptions{
		Workers: workers,
		Exe:     buildTestWorker(t),
		Args:    []string{},
		Env:     env,
	})
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(func() {
		if cerr := p.Close(); cerr != nil {
			t.Errorf("closing the pool: %v", cerr)
		}
	})
	return p
}

// sourceFile writes an image and opens it, which is what the parent hands the
// worker: a descriptor, never a path.
func sourceFile(t *testing.T, w, h int) *os.File {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 0x80, A: 0xff})
		}
	}
	path := filepath.Join(t.TempDir(), "src.png")
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encoding the source: %v", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatalf("writing the source: %v", err)
	}
	f, err := os.Open(path) //nolint:gosec // G304: this test's own TempDir and a fixed name.
	if err != nil {
		t.Fatalf("opening the source: %v", err)
	}
	t.Cleanup(func() {
		if cerr := f.Close(); cerr != nil && !errors.Is(cerr, os.ErrClosed) {
			t.Errorf("closing the source: %v", cerr)
		}
	})
	return f
}

func outputFile(t *testing.T) *os.File {
	t.Helper()
	f, err := os.Create(filepath.Join(t.TempDir(), "out.png")) //nolint:gosec // G304: as above.
	if err != nil {
		t.Fatalf("creating the output: %v", err)
	}
	t.Cleanup(func() {
		if cerr := f.Close(); cerr != nil && !errors.Is(cerr, os.ErrClosed) {
			t.Errorf("closing the output: %v", cerr)
		}
	})
	return f
}

// A real image through the pool and a real worker produces a thumbnail on the
// output descriptor.
func TestAJobProducesAThumbnail(t *testing.T) {
	p := newPool(t, 1, "")
	in, out := sourceFile(t, 400, 200), outputFile(t)

	resp, err := p.Generate(t.Context(), Request{
		Kind:      JobImage,
		Preset:    PresetSmall,
		Flags:     FlagStripEXIF,
		MaxPixels: maxPixelsFor(),
	}, PlainSource{F: in}, out)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if resp.Status != StatusOK {
		t.Fatalf("status %v: %s", resp.Status, resp.Err)
	}
	if resp.Width != 256 || resp.Height != 128 {
		t.Errorf("the worker reported %dx%d, want 256x128", resp.Width, resp.Height)
	}
	if resp.Bytes == 0 {
		t.Error("the worker reported writing nothing")
	}

	// The bytes really landed, and they are a PNG.
	written, err := os.ReadFile(out.Name()) //nolint:gosec // G304: this test's own output path.
	if err != nil {
		t.Fatalf("reading the output: %v", err)
	}
	if Sniff(written) != FormatPNG {
		t.Errorf("the output is not a PNG: %d bytes", len(written))
	}
}

// The exact box is honoured rather than the preset's, which is what the
// compatibility content route needs.
func TestAnExactSizeRequestIsHonoured(t *testing.T) {
	p := newPool(t, 1, "")
	in, out := sourceFile(t, 400, 200), outputFile(t)

	resp, err := p.Generate(t.Context(), Request{
		Kind:      JobImage,
		Preset:    PresetLarge,
		Flags:     FlagStripEXIF,
		MaxPixels: maxPixelsFor(),
		Width:     100,
		Height:    100,
	}, PlainSource{F: in}, out)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if resp.Status != StatusOK {
		t.Fatalf("status %v: %s", resp.Status, resp.Err)
	}
	// 2:1 into a 100 box.
	if resp.Width != 100 || resp.Height != 50 {
		t.Errorf("got %dx%d, want 100x50", resp.Width, resp.Height)
	}
}

// A request can lower the compiled-in ceiling and never raise it: a compromised
// parent must not be able to widen its worker.
func TestARequestCanOnlyLowerTheCeiling(t *testing.T) {
	p := newPool(t, 1, "")

	// Well under the compiled bound, and under the fixture's own pixel count.
	in, out := sourceFile(t, 400, 200), outputFile(t)
	resp, err := p.Generate(t.Context(), Request{
		Kind:      JobImage,
		Preset:    PresetSmall,
		MaxPixels: 100,
	}, PlainSource{F: in}, out)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if resp.Status != StatusTooLarge {
		t.Errorf("status %v, want TooLarge for a lowered ceiling", resp.Status)
	}

	// Asking for more than the compiled bound does not widen anything: the
	// same source still decodes under the compiled limit rather than a raised
	// one, so the job succeeds exactly as it would have without the request.
	in2, out2 := sourceFile(t, 400, 200), outputFile(t)
	resp, err = p.Generate(t.Context(), Request{
		Kind:      JobImage,
		Preset:    PresetSmall,
		MaxPixels: ^uint32(0),
	}, PlainSource{F: in2}, out2)
	if err != nil {
		t.Fatalf("Generate with a huge ceiling: %v", err)
	}
	if resp.Status != StatusOK {
		t.Errorf("status %v: %s", resp.Status, resp.Err)
	}
}

// Video is refused honestly over the wire, so a client gets something it can
// act on rather than a generic failure.
func TestVideoIsRefusedHonestly(t *testing.T) {
	p := newPool(t, 1, "")
	in, out := sourceFile(t, 10, 10), outputFile(t)

	resp, err := p.Generate(t.Context(), Request{Kind: JobVideo, Preset: PresetSmall},
		PlainSource{F: in}, out)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if resp.Status != StatusNotImplemented {
		t.Errorf("status %v, want NotImplemented", resp.Status)
	}
	if !errors.Is(statusError(resp), ErrNotImplemented) {
		t.Error("the status does not map onto ErrNotImplemented")
	}
}

// A file that is not an image is a decode failure the worker survives, which
// is the whole reason the graceful limit exists.
func TestACorruptSourceIsADecodeFailureTheWorkerSurvives(t *testing.T) {
	p := newPool(t, 1, "")

	path := filepath.Join(t.TempDir(), "junk.png")
	if err := os.WriteFile(path, []byte("\x89PNG\r\n\x1a\nnot really a png"), 0o600); err != nil {
		t.Fatalf("writing the junk: %v", err)
	}
	in, err := os.Open(path) //nolint:gosec // G304: this test's own TempDir.
	if err != nil {
		t.Fatalf("opening the junk: %v", err)
	}
	defer func() {
		if cerr := in.Close(); cerr != nil {
			t.Errorf("close: %v", cerr)
		}
	}()

	resp, err := p.Generate(t.Context(), Request{Kind: JobImage, Preset: PresetSmall},
		PlainSource{F: in}, outputFile(t))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if resp.Status != StatusDecodeFailed {
		t.Errorf("status %v, want DecodeFailed", resp.Status)
	}

	// The worker survived: the next job on the same slot works.
	in2, out2 := sourceFile(t, 40, 40), outputFile(t)
	next, err := p.Generate(t.Context(), Request{Kind: JobImage, Preset: PresetSmall},
		PlainSource{F: in2}, out2)
	if err != nil || next.Status != StatusOK {
		t.Errorf("the worker did not survive a corrupt source: %v, %v", err, next.Status)
	}
}

// A caller finding no free slot is refused rather than queued, at the cost of
// a channel receive.
func TestABusyPoolRefuses(t *testing.T) {
	p := newPool(t, 1, "")

	// Take the only slot and hold it.
	<-p.free
	defer func() { p.free <- 0 }()

	in, out := sourceFile(t, 10, 10), outputFile(t)
	_, err := p.Generate(t.Context(), Request{Kind: JobImage, Preset: PresetSmall},
		PlainSource{F: in}, out)
	if !errors.Is(err, ErrWorkerBusy) {
		t.Errorf("got %v, want ErrWorkerBusy", err)
	}
}

// A worker that dies mid-job is reaped and replaced on the next job for that
// slot. Replacement is lazy, or a source that reliably kills workers becomes a
// fork bomb.
func TestAKilledWorkerIsReplacedOnTheNextJob(t *testing.T) {
	p := newPool(t, 1, ModeDie)
	in, out := sourceFile(t, 40, 40), outputFile(t)

	_, err := p.Generate(t.Context(), Request{Kind: JobImage, Preset: PresetSmall},
		PlainSource{F: in}, out)
	if !errors.Is(err, ErrWorkerDied) {
		t.Fatalf("got %v, want ErrWorkerDied", err)
	}
	// The slot was emptied, so the next job starts a replacement rather than
	// writing to a dead process.
	p.slots[0].mu.Lock()
	empty := p.slots[0].sock == nil && p.slots[0].proc == nil
	p.slots[0].mu.Unlock()
	if !empty {
		t.Error("the slot still holds a dead worker")
	}

	in2, out2 := sourceFile(t, 40, 40), outputFile(t)
	if _, err := p.Generate(t.Context(), Request{Kind: JobImage, Preset: PresetSmall},
		PlainSource{F: in2}, out2); !errors.Is(err, ErrWorkerDied) {
		// The helper dies on every job, so a replacement was started and died
		// too. What matters is that a replacement was attempted at all.
		t.Errorf("the second job gave %v, want another worker death", err)
	}
}

// A hung worker dies at its deadline rather than holding the slot forever.
func TestAHungWorkerDiesAtItsDeadline(t *testing.T) {
	p := newPool(t, 1, ModeHang)
	in, out := sourceFile(t, 40, 40), outputFile(t)

	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := p.Generate(ctx, Request{Kind: JobImage, Preset: PresetSmall},
		PlainSource{F: in}, out)
	if !errors.Is(err, ErrWorkerDied) {
		t.Fatalf("got %v, want ErrWorkerDied", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("the deadline took %v to fire", elapsed)
	}
}

// A closed pool refuses rather than starting a process nobody will reap.
func TestAClosedPoolRefuses(t *testing.T) {
	p := newPool(t, 1, "")
	if err := p.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Close is idempotent.
	if err := p.Close(); err != nil {
		t.Errorf("a second Close: %v", err)
	}

	in, out := sourceFile(t, 10, 10), outputFile(t)
	_, err := p.Generate(t.Context(), Request{Kind: JobImage, Preset: PresetSmall},
		PlainSource{F: in}, out)
	if !errors.Is(err, ErrPoolClosed) {
		t.Errorf("got %v, want ErrPoolClosed", err)
	}
}

// Worker deaths must not leak descriptors in the parent: a pool that leaks one
// per death runs out and starts failing every job.
func TestNoDescriptorLeakAcrossWorkerDeaths(t *testing.T) {
	p := newPool(t, 1, ModeDie)

	before := openDescriptors(t)
	for range 8 {
		in, out := sourceFile(t, 20, 20), outputFile(t)
		//nolint:errcheck // every job is expected to fail; the count is what is under test.
		_, _ = p.Generate(t.Context(), Request{Kind: JobImage, Preset: PresetSmall},
			PlainSource{F: in}, out)
	}
	after := openDescriptors(t)

	// Each iteration opens its own source and output, which the cleanup closes
	// at the end of the test rather than now, so the slack covers those.
	if after > before+24 {
		t.Errorf("descriptors grew from %d to %d across eight worker deaths", before, after)
	}
}

// openDescriptors counts this process's open files.
func openDescriptors(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skipf("/proc is not readable: %v", err)
	}
	return len(entries)
}

// Several workers run jobs at once without stepping on each other. Under -race
// this is what proves the per-slot locking holds.
func TestConcurrentJobsAcrossSlots(t *testing.T) {
	p := newPool(t, 3, "")

	var wg sync.WaitGroup
	errs := make(chan error, 9)
	for range 9 {
		wg.Add(1)
		task.Go(t.Context(), "preview: concurrent pool job", func() {
			defer wg.Done()
			in, out := sourceFile(t, 60, 60), outputFile(t)
			resp, err := p.Generate(t.Context(), Request{
				Kind: JobImage, Preset: PresetSmall, MaxPixels: maxPixelsFor(),
			}, PlainSource{F: in}, out)
			switch {
			case errors.Is(err, ErrWorkerBusy):
				// A full pool refuses rather than queueing, which is the
				// designed answer and not a failure.
			case err != nil:
				errs <- err
			case resp.Status != StatusOK:
				errs <- errors.New("status " + resp.Status.String() + ": " + resp.Err)
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("a concurrent job failed: %v", err)
	}
}
