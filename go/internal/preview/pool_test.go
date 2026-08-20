//go:build linux

package preview_test

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/internal/preview"
)

// The pool is tested against a real worker process, because everything
// interesting about it is what happens when that process dies. A fake would
// prove the parent's bookkeeping and none of the behaviour the design is for.

// workerBinary builds a helper that is a worker without the jail.
//
// The jail is proved separately, on a real kernel, by the probe test. Applying
// it here would make these tests depend on a kernel feature that is allowed to
// be absent, and would hide the pool behaviour they exist to check.
func workerBinary(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("building a helper binary is not a short test")
	}

	// The helper lives inside the module, because a package under internal/
	// cannot be imported from outside it. It is built into a temp directory so
	// nothing is left in the tree.
	bin := filepath.Join(t.TempDir(), "worker")
	//nolint:gosec // G204: every argument is a constant except a path under t.TempDir.
	cmd := exec.Command("go", "build", "-o", bin, "./internal/preview/worker/testworker")
	cmd.Dir = moduleRoot(t)
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building the helper: %v\n%s", err, out)
	}
	return bin
}

// moduleRoot walks up to the directory holding go.mod, so the helper builds
// from a stable working directory whatever the test's own is.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, serr := os.Stat(filepath.Join(dir, "go.mod")); serr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test's working directory")
		}
		dir = parent
	}
}

func pngOf(t *testing.T, w, h int) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatalf("encoding: %v", err)
	}
	return b.Bytes()
}

// job writes src to a temp file and returns the two descriptors a job needs.
func job(t *testing.T, src []byte) (in, out *os.File) {
	t.Helper()
	dir := t.TempDir()

	inPath := filepath.Join(dir, "in")
	if err := os.WriteFile(inPath, src, 0o600); err != nil {
		t.Fatalf("writing the source: %v", err)
	}
	in, err := os.Open(inPath) //nolint:gosec // G304: a path this test just built under TempDir.
	if err != nil {
		t.Fatalf("opening the source: %v", err)
	}

	out, err = os.Create(filepath.Join(dir, "out")) //nolint:gosec // G304: as above.
	if err != nil {
		t.Fatalf("creating the output: %v", err)
	}
	t.Cleanup(func() {
		_ = in.Close()  //nolint:errcheck // a test fixture's descriptors.
		_ = out.Close() //nolint:errcheck // as above.
	})
	return in, out
}

func newPool(t *testing.T, workers int, mode string) *preview.Pool {
	t.Helper()
	env := []string{}
	if mode != "" {
		env = append(env, "HELPER_MODE="+mode)
	}
	// The helper needs a GOCACHE and a HOME to run under, which the parent's
	// environment supplies.
	env = append(env, "HOME="+os.Getenv("HOME"), "PATH="+os.Getenv("PATH"))

	p, err := preview.NewPool(preview.PoolOptions{
		Workers: workers,
		Exe:     workerBinary(t),
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

func TestAJobProducesAThumbnail(t *testing.T) {
	p := newPool(t, 1, "")
	in, out := job(t, pngOf(t, 800, 600))

	resp, err := p.Generate(context.Background(), preview.Request{
		Kind: preview.JobImage, Preset: preview.PresetSmall, Flags: preview.FlagStripEXIF,
	}, in, out)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if resp.Status != preview.StatusOK {
		t.Fatalf("status = %v (%s)", resp.Status, resp.Err)
	}
	if resp.Width == 0 || resp.Height == 0 || resp.Bytes == 0 {
		t.Fatalf("the response reports nothing: %+v", resp)
	}
	// The thumbnail fits the preset's box and is a real PNG.
	maxW, maxH := preview.PresetSmall.Bounds()
	if int(resp.Width) > maxW || int(resp.Height) > maxH {
		t.Fatalf("the thumbnail is %dx%d, past the %dx%d box",
			resp.Width, resp.Height, maxW, maxH)
	}
	written, rerr := os.ReadFile(out.Name()) //nolint:gosec // G304: this test's own output file.
	if rerr != nil {
		t.Fatalf("reading the output: %v", rerr)
	}
	if _, derr := png.Decode(bytes.NewReader(written)); derr != nil {
		t.Fatalf("the worker wrote something that is not a PNG: %v", derr)
	}
}

// Video is answered honestly rather than as a generic failure, and the answer
// comes over the wire so a client can act on it.
func TestVideoIsAnsweredNotImplemented(t *testing.T) {
	p := newPool(t, 1, "")
	in, out := job(t, []byte("not really a video"))

	resp, err := p.Generate(context.Background(), preview.Request{
		Kind: preview.JobVideo, Preset: preview.PresetSmall,
	}, in, out)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if resp.Status != preview.StatusNotImplemented {
		t.Fatalf("status = %v, want not implemented", resp.Status)
	}
	if !strings.Contains(resp.Err, "not implemented") {
		t.Fatalf("the answer does not say so: %q", resp.Err)
	}
}

// A decode limit refusing is the graceful stop: the worker says no and lives,
// which is the whole reason the limit is not left to RLIMIT_AS.
func TestATooLargeImageIsRefusedAndTheWorkerSurvives(t *testing.T) {
	p := newPool(t, 1, "")

	in, out := job(t, pngOf(t, 400, 400))
	resp, err := p.Generate(context.Background(), preview.Request{
		Kind: preview.JobImage, Preset: preview.PresetSmall,
		MaxPixels: 100, // smaller than the image
	}, in, out)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if resp.Status != preview.StatusTooLarge {
		t.Fatalf("status = %v (%s), want too large", resp.Status, resp.Err)
	}

	// The same worker answers the next job, which is what "survives" means.
	in2, out2 := job(t, pngOf(t, 64, 64))
	resp2, err := p.Generate(context.Background(), preview.Request{
		Kind: preview.JobImage, Preset: preview.PresetSmall,
	}, in2, out2)
	if err != nil {
		t.Fatalf("the job after a refusal: %v", err)
	}
	if resp2.Status != preview.StatusOK {
		t.Fatalf("status = %v (%s)", resp2.Status, resp2.Err)
	}
}

func TestGarbageIsADecodeFailureNotADeath(t *testing.T) {
	p := newPool(t, 1, "")
	in, out := job(t, []byte("this is not an image at all"))

	resp, err := p.Generate(context.Background(), preview.Request{
		Kind: preview.JobImage, Preset: preview.PresetSmall,
	}, in, out)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if resp.Status == preview.StatusOK {
		t.Fatal("garbage produced a thumbnail")
	}
}

// The claim the whole design rests on: a crafted input that kills a worker
// costs one thumbnail and leaves the pool serving.
func TestAWorkerDeathCostsOneThumbnailAndThePoolKeepsServing(t *testing.T) {
	p := newPool(t, 1, "die")

	in, out := job(t, pngOf(t, 64, 64))
	_, err := p.Generate(context.Background(), preview.Request{
		Kind: preview.JobImage, Preset: preview.PresetSmall,
	}, in, out)
	if !errors.Is(err, preview.ErrWorkerDied) {
		t.Fatalf("err = %v, want ErrWorkerDied", err)
	}

	// The replacement is lazy, so the next job is what starts one. It has to
	// succeed, or a single crafted file would take the pool down for good.
	in2, out2 := job(t, pngOf(t, 64, 64))
	_, err = p.Generate(context.Background(), preview.Request{
		Kind: preview.JobImage, Preset: preview.PresetSmall,
	}, in2, out2)
	if !errors.Is(err, preview.ErrWorkerDied) {
		t.Fatalf("the second job returned %v; the helper dies on every job, "+
			"so this proves a replacement was started rather than the slot "+
			"being left dead", err)
	}
}

// A worker that never answers is killed at the deadline rather than waited
// for: it is single-purpose and there is nothing in it to preserve.
func TestAHungWorkerIsKilledAtTheDeadline(t *testing.T) {
	p := newPool(t, 1, "hang")
	in, out := job(t, pngOf(t, 64, 64))

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := p.Generate(ctx, preview.Request{
		Kind: preview.JobImage, Preset: preview.PresetSmall,
	}, in, out)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("a hung worker answered")
	}
	if elapsed > 5*time.Second {
		t.Fatalf("the deadline took %v to fire", elapsed)
	}

	// And the pool still works afterwards.
	p2 := newPool(t, 1, "")
	in2, out2 := job(t, pngOf(t, 32, 32))
	if _, err := p2.Generate(context.Background(), preview.Request{
		Kind: preview.JobImage, Preset: preview.PresetSmall,
	}, in2, out2); err != nil {
		t.Fatalf("a fresh pool after a hang: %v", err)
	}
}

// Concurrent jobs are what the pool exists for, and the slot bookkeeping is
// where a race would live.
func TestConcurrentJobsShareThePool(t *testing.T) {
	p := newPool(t, 3, "")

	var wg sync.WaitGroup
	errs := make([]error, 12)
	for i := 0; i < len(errs); i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			in, out := job(t, pngOf(t, 128, 96))
			_, err := p.Generate(context.Background(), preview.Request{
				Kind: preview.JobImage, Preset: preview.PresetSmall,
			}, in, out)
			errs[i] = err
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("job %d: %v", i, err)
		}
	}
}

func TestAClosedPoolRefuses(t *testing.T) {
	p := newPool(t, 1, "")
	if err := p.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	in, out := job(t, pngOf(t, 32, 32))
	if _, err := p.Generate(context.Background(), preview.Request{
		Kind: preview.JobImage, Preset: preview.PresetSmall,
	}, in, out); !errors.Is(err, preview.ErrPoolClosed) {
		t.Fatalf("err = %v, want ErrPoolClosed", err)
	}
}

// A caller waiting for a slot that never frees is told the pool is busy rather
// than blocking forever.
func TestACallerWaitingForABusyPoolIsRefused(t *testing.T) {
	// One worker, and it hangs, so the single slot stays occupied.
	p := newPool(t, 1, "hang")

	held := make(chan struct{})
	go func() {
		defer close(held)
		in, out := job(t, pngOf(t, 32, 32))
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, _ = p.Generate(ctx, preview.Request{ //nolint:errcheck // this one is expected to fail; the assertion is on the second.
			Kind: preview.JobImage, Preset: preview.PresetSmall,
		}, in, out)
	}()

	// Give the first job time to take the slot.
	time.Sleep(200 * time.Millisecond)

	in, out := job(t, pngOf(t, 32, 32))
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err := p.Generate(ctx, preview.Request{
		Kind: preview.JobImage, Preset: preview.PresetSmall,
	}, in, out)
	if !errors.Is(err, preview.ErrWorkerBusy) {
		t.Fatalf("err = %v, want ErrWorkerBusy", err)
	}
	<-held
}
