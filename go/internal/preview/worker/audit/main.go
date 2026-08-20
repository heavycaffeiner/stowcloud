//go:build linux

// Command audit measures the preview worker's seccomp allow-list.
//
// It runs the decode path under a filter whose unmatched action is
// SECCOMP_RET_LOG rather than a kill, so one run against the corpus finds every
// syscall the list is missing instead of dying at the first.
//
// The measurement is the point. A list arrived at by reasoning about what a
// decoder "should" need is a list that kills a worker the first time a user
// uploads something unusual, and the whole design says a crafted input costs
// one thumbnail rather than the pool.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	oscmd "os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/heavycaffeiner/stowcloud/go/internal/jail"
	"github.com/heavycaffeiner/stowcloud/go/internal/preview"
)

func main() {
	corpus := flag.String("corpus", "", "directory of images to decode")
	child := flag.Bool("child", false, "internal: run the decode pass")
	noFilter := flag.Bool("no-filter", false,
		"do not install a seccomp filter; for measuring under strace instead")
	flag.Parse()

	if *corpus == "" {
		say("audit: -corpus is required")
		os.Exit(2)
	}

	if *child {
		if err := decodeAll(*corpus, *noFilter); err != nil {
			say("audit: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if err := parent(*corpus); err != nil {
		say("audit: %v\n", err)
		os.Exit(1)
	}
}

// parent runs the child and reports what the audit log recorded.
func parent(corpus string) error {
	out("audit: %d syscalls on the allow-list for %s\n",
		len(jail.AllowedSyscalls()), runtime.GOARCH)

	self, err := os.Executable()
	if err != nil {
		return err
	}

	// The child installs the logging filter and decodes everything. Its own
	// exit status is not the answer: under SECCOMP_RET_LOG nothing is killed,
	// so a clean exit says only that the decodes ran.
	cmd := exec(self, "-corpus", corpus, "-child")
	if rerr := cmd(); rerr != nil {
		return fmt.Errorf("the decode pass failed: %w", rerr)
	}

	out("audit: the decode pass completed under SECCOMP_RET_LOG" + "\n")
	out("audit: read the kernel audit log for what the list is missing, e.g." + "\n")
	out("       journalctl -k --since '1 min ago' | grep -i seccomp" + "\n")
	out("       ausearch -m SECCOMP -ts recent" + "\n")
	out("\n")
	out("audit: every logged syscall number that is not on the list below" + "\n")
	out("       belongs on it. An empty log means the list is complete." + "\n")
	for _, n := range sortedList() {
		out("  %4d\n", n)
	}
	return nil
}

func sortedList() []int {
	list := append([]int(nil), jail.AllowedSyscalls()...)
	sort.Ints(list)
	return list
}

// decodeAll installs the logging filter and runs the whole decode path over
// every file in the corpus.
//
// Failures are not fatal: a file that does not decode is exercising the error
// path, which is exactly as interesting as a success. An error path allocates
// and unwinds differently, and a filter measured only against files that
// decode is one that kills the first time somebody uploads a corrupt image.
func decodeAll(corpus string, noFilter bool) error {
	// Under strace the filter is left off: the tracer sees every syscall
	// directly, and a filter that allowed them would hide nothing but would
	// add its own.
	if !noFilter {
		if err := jail.InstallSeccomp(jail.FilterWorkerAudit); err != nil {
			return fmt.Errorf("installing the audit filter: %w", err)
		}
	}

	// The corpus is read into memory before the marker below, because the
	// worker never opens a file: its input arrives as a descriptor. Measuring
	// the reads would put openat on a list the worker must not have.
	entries, err := os.ReadDir(corpus)
	if err != nil {
		return fmt.Errorf("reading the corpus: %w", err)
	}
	var images [][]byte
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) == ".md" {
			continue
		}
		data, rerr := os.ReadFile(filepath.Join(corpus, e.Name())) //nolint:gosec // G304: a path under the corpus directory named on the command line.
		if rerr != nil {
			continue
		}
		images = append(images, data)
	}

	// The marker. Everything before it is setup that the real worker does not
	// do, and a measurement that included it would size the filter for the
	// wrong process. A tracer splits the trace here.
	mark("decode-start")

	lim := preview.DefaultDecodeLimits()
	decoded, failed := 0, 0
	for _, data := range images {
		img, derr := preview.DecodeBounded(data, lim)
		if derr != nil {
			failed++
			continue
		}
		// The rest of what the worker does with an image, because each stage
		// is its own set of allocations and syscalls.
		img = preview.ReadOrientation(data).Apply(img)
		thumb, terr := preview.Thumbnail(img, preview.PresetSmall, lim)
		if terr != nil {
			failed++
			continue
		}
		if eerr := preview.EncodePNG(discard{}, thumb); eerr != nil {
			failed++
			continue
		}
		decoded++
	}

	mark("decode-end")
	say("audit: %d decoded, %d exercised an error path\n", decoded, failed)
	return nil
}

// mark writes a recognisable syscall a tracer can split the trace on.
//
// A write to a closed descriptor: it reaches the kernel, so a tracer sees it,
// and it does nothing, so it cannot perturb what is being measured.
func mark(name string) {
	_, _ = unix.Write(markerFD, []byte(name)) //nolint:errcheck // the write is expected to fail; only the tracer reads it.
}

// markerFD is a descriptor number nothing opens, so the write always fails
// with EBADF and never touches a real file.
const markerFD = 999

// say and out are this command's own reporting. A write to a terminal that
// failed has nowhere else to report it, which is the one case where dropping
// the error is the honest handling rather than a shortcut.
func say(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, format, args...) //nolint:errcheck // see above.
}

func out(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stdout, format, args...) //nolint:errcheck // as above.
}

// discard is an io.Writer that keeps nothing. The encoder's syscalls are what
// is being measured, not its output.
type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

// exec runs this binary again with the given arguments.
//
// A child rather than an in-process filter, because seccomp is irrevocable:
// once installed it applies to this process for the rest of its life, and the
// parent still has reporting to do afterwards.
func exec(self string, args ...string) func() error {
	return func() error {
		cmd := oscmd.Command(self, args...) //nolint:gosec // G204: this binary's own path and constant arguments.
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		err := cmd.Run()
		if err == nil {
			return nil
		}
		var ee *oscmd.ExitError
		if errors.As(err, &ee) {
			if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
				return fmt.Errorf("the decode pass was killed by signal %v, which under "+
					"SECCOMP_RET_LOG should not happen", ws.Signal())
			}
		}
		return fmt.Errorf("the decode pass failed: %w", err)
	}
}
