//go:build linux

package worker_test

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/jail"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/preview"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/preview/worker"
)

// The proof.
//
// The pool's own tests drive an unjailed worker, because the failures they
// check are the parent's. These run the real one, under the real jail, and ask
// the kernel what it does. A security claim that cannot be executed is a
// comment.

//nolint:gochecknoglobals // one build per test binary, which is what sync.Once is for.
var (
	jailedOnce sync.Once
	jailedBin  string
	jailedErr  error
)

// buildJailedWorker compiles the shipped worker, the one that applies the jail
// before it reads its first message. Proving a sandbox against a stand-in
// proves nothing about the one that ships.
func buildJailedWorker(t *testing.T) string {
	t.Helper()
	jailedOnce.Do(func() {
		dir, err := os.MkdirTemp("", "jailedworker")
		if err != nil {
			jailedErr = err
			return
		}
		bin := filepath.Join(dir, "jailedworker")
		//nolint:gosec // G204: the arguments are this test's own constants.
		cmd := exec.Command("go", "build", "-o", bin,
			"github.com/heavycaffeiner/stowcloud/go/engine/service/preview/worker/jailedworker")
		if out, berr := cmd.CombinedOutput(); berr != nil {
			jailedErr = errors.New(string(out))
			return
		}
		jailedBin = bin
	})
	if jailedErr != nil {
		t.Skipf("the jailed worker could not be built: %v", jailedErr)
	}
	return jailedBin
}

// jailedPool runs the real worker under the preferred policy, so a kernel
// without Landlock still exercises the seccomp half.
func jailedPool(t *testing.T) *preview.Pool {
	t.Helper()
	if _, err := jail.ABIVersion(); err != nil {
		t.Skipf("this kernel has no Landlock: %v", err)
	}
	p, err := preview.NewPool(preview.PoolOptions{
		Workers: 1,
		Exe:     buildJailedWorker(t),
		Args:    []string{},
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

func sourceFile(t *testing.T, w, h int) *os.File {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 0x80, A: 0xff})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encoding the source: %v", err)
	}
	path := filepath.Join(t.TempDir(), "src.png")
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

// probe runs one probe inside the finished jail.
//
// A probe that is killed never answers at all: the process is gone and the
// parent sees the socket close, which is itself a pass.
func probe(t *testing.T, p *preview.Pool, which worker.Probe) (worker.ProbeOutcome, string, error) {
	t.Helper()
	in, out := sourceFile(t, 8, 8), outputFile(t)
	resp, err := p.Generate(t.Context(), preview.Request{
		Kind:   preview.JobProbe,
		Preset: preview.Preset(which),
	}, preview.PlainSource{F: in}, out)
	if err != nil {
		return 0, "", err
	}
	return worker.OutcomeFrom(resp.Width), resp.Err, nil
}

// The transport works, so a run where every probe was killed can be told from
// one where the socket was never connected.
func TestJailedWorkerAnswersAPing(t *testing.T) {
	outcome, detail, err := probe(t, jailedPool(t), worker.ProbePing)
	if err != nil {
		t.Fatalf("the jailed worker did not answer a ping: %v", err)
	}
	if outcome != worker.OutcomeCompleted {
		t.Errorf("ping reported %v: %s", outcome, detail)
	}
}

// refusedOrKilled is the pass condition for a probe the jail is meant to stop:
// the kernel refused it, or the process is gone.
func refusedOrKilled(t *testing.T, which worker.Probe, what string) {
	t.Helper()
	outcome, detail, err := probe(t, jailedPool(t), which)
	if err != nil {
		if !errors.Is(err, preview.ErrWorkerDied) {
			t.Fatalf("unexpected failure: %v", err)
		}
		// Killed outright, which is the seccomp answer and a pass.
		return
	}
	if outcome == worker.OutcomeSucceeded {
		t.Errorf("the jailed worker %s: %s", what, detail)
	}
}

// The worker holds descriptors and has no way to open another: openat is off
// the allow list and the Landlock domain grants nothing.
func TestAJailedWorkerCannotOpenAPath(t *testing.T) {
	refusedOrKilled(t, worker.ProbeOpenEtcPasswd, "opened a file by name")
}

// No socket and no connect, so no network.
func TestAJailedWorkerCannotReachTheNetwork(t *testing.T) {
	refusedOrKilled(t, worker.ProbeCreateSocket, "created a socket")
}

// No clone and no execve, so no new process. A worker that cannot clone cannot
// fork either, which is the property the list is for.
func TestAJailedWorkerCannotFork(t *testing.T) {
	refusedOrKilled(t, worker.ProbeFork, "forked")
}

// ApplyLimits is wired, so the in-process pixel ceiling has the kernel backstop
// the comments have always claimed. This is the worker reporting what the
// kernel actually gave it.
func TestAJailedWorkerReportsItsResourceLimits(t *testing.T) {
	outcome, detail, err := probe(t, jailedPool(t), worker.ProbeReportLimits)
	if err != nil {
		t.Fatalf("the limits probe failed: %v", err)
	}
	if outcome != worker.OutcomeCompleted {
		t.Fatalf("the limits probe reported %v: %s", outcome, detail)
	}

	want := jail.DefaultLimits()
	if as := fieldOf(t, detail, "as"); as != want.AddressSpaceBytes {
		t.Errorf("RLIMIT_AS is %d, want the configured %d", as, want.AddressSpaceBytes)
	}
	if nofile := fieldOf(t, detail, "nofile"); nofile != want.OpenFiles {
		t.Errorf("RLIMIT_NOFILE is %d, want %d", nofile, want.OpenFiles)
	}
	if nproc := fieldOf(t, detail, "nproc"); nproc != want.ChildProcesses {
		t.Errorf("RLIMIT_NPROC is %d, want %d", nproc, want.ChildProcesses)
	}
}

// SealDescriptors is wired, so nothing the worker inherited past its control
// socket survives. os/exec's CLOEXEC defaults cover most of them, and "most" is
// not a security answer.
func TestAJailedWorkerHoldsNoUnexpectedDescriptor(t *testing.T) {
	outcome, detail, err := probe(t, jailedPool(t), worker.ProbeCountDescriptors)
	if err != nil {
		t.Fatalf("the descriptor probe failed: %v", err)
	}
	if outcome != worker.OutcomeCompleted {
		t.Fatalf("the descriptor probe reported %v: %s", outcome, detail)
	}

	// Standard in, out and error, the control socket, and the two descriptors
	// this very job arrived with. Nothing above them.
	const ceiling = worker.ControlFD + 2
	if highest := fieldOf(t, detail, "highest"); highest > ceiling {
		t.Errorf("the worker holds descriptor %d, above the control socket and its job: %q",
			highest, detail)
	}
}

// A jailed worker survives many jobs: the filter covers the steady state, not
// just the first decode. The runtime builds its poller lazily, so an early job
// passing proves less than a later one.
func TestAJailedWorkerSurvivesManyJobs(t *testing.T) {
	p := jailedPool(t)
	for i := range 12 {
		in, out := sourceFile(t, 64, 48), outputFile(t)
		resp, err := p.Generate(t.Context(), preview.Request{
			Kind:      preview.JobImage,
			Preset:    preview.PresetSmall,
			Flags:     preview.FlagStripEXIF,
			MaxPixels: 1 << 20,
		}, preview.PlainSource{F: in}, out)
		if err != nil {
			t.Fatalf("job %d failed: %v", i, err)
		}
		if resp.Status != preview.StatusOK {
			t.Fatalf("job %d: status %v: %s", i, resp.Status, resp.Err)
		}
	}
}

// A worker decoding a larger image than the last one grows its heap, which is
// where an unlisted syscall would surface.
func TestAJailedWorkerDecodesGrowingImages(t *testing.T) {
	p := jailedPool(t)
	for _, size := range []int{32, 128, 512, 1024} {
		in, out := sourceFile(t, size, size), outputFile(t)
		resp, err := p.Generate(t.Context(), preview.Request{
			Kind:      preview.JobImage,
			Preset:    preview.PresetMedium,
			Flags:     preview.FlagStripEXIF,
			MaxPixels: 1 << 24,
		}, preview.PlainSource{F: in}, out)
		if err != nil {
			t.Fatalf("a %dx%d source killed the worker: %v", size, size, err)
		}
		if resp.Status != preview.StatusOK {
			t.Fatalf("a %dx%d source: status %v: %s", size, size, resp.Status, resp.Err)
		}
	}
}

// fieldOf reads one "name=value" field out of a probe's detail string.
func fieldOf(t *testing.T, detail, name string) uint64 {
	t.Helper()
	for _, part := range strings.Fields(detail) {
		k, v, ok := strings.Cut(part, "=")
		if !ok || k != name {
			continue
		}
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			t.Fatalf("the %s field is %q: %v", name, v, err)
		}
		return n
	}
	t.Fatalf("no %s field in %q", name, detail)
	return 0
}
