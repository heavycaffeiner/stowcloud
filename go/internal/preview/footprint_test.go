//go:build linux

package preview_test

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/internal/preview"
)

// What the worker pool costs in resident memory.
//
// This is the regression the footprint expectation named most confidently and
// then did not measure: the previous implementation forked its workers and this
// one execs them, so each worker is a fresh process image rather than a
// copy-on-write child. More resident memory per worker is the price of the
// worker being a real process, which is what lets the sandbox cover it.
//
// It reports rather than asserts. A threshold here would be a number about this
// machine, and what the footprint document needs is the measurement, taken the
// same way twice, with the shape of the growth visible.
//
// Run it with:
//
//	go test ./internal/preview -run TestWorkerPoolFootprint -v

// procRSS is one process's resident set in kilobytes, read from the kernel
// rather than sampled from inside: the point is what the operating system
// accounts to the process, which a runtime statistic does not report.
func procRSS(pid int) (int64, error) {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return 0, err
	}
	for line := range strings.SplitSeq(string(raw), "\n") {
		if !strings.HasPrefix(line, "VmRSS:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0, fmt.Errorf("preview: unreadable VmRSS line %q", line)
		}
		return strconv.ParseInt(fields[1], 10, 64)
	}
	return 0, fmt.Errorf("preview: no VmRSS for pid %d", pid)
}

// childPIDs is every direct child of this process, which is every worker the
// pool has exec'd.
func childPIDs(t *testing.T) []int {
	t.Helper()
	self := os.Getpid()

	entries, err := os.ReadDir("/proc")
	if err != nil {
		t.Fatalf("reading /proc: %v", err)
	}
	var out []int
	for _, e := range entries {
		pid, perr := strconv.Atoi(e.Name())
		if perr != nil {
			continue
		}
		stat, serr := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if serr != nil {
			continue
		}
		// The fields after the comm are state and then ppid, and comm can
		// itself contain spaces, so the scan starts after its closing bracket.
		close := strings.LastIndexByte(string(stat), ')')
		if close < 0 {
			continue
		}
		fields := strings.Fields(string(stat)[close+1:])
		if len(fields) < 2 {
			continue
		}
		if ppid, aerr := strconv.Atoi(fields[1]); aerr == nil && ppid == self {
			out = append(out, pid)
		}
	}
	return out
}

func TestWorkerPoolFootprint(t *testing.T) {
	for _, workers := range []int{1, 2, 4} {
		t.Run(fmt.Sprintf("%d-workers", workers), func(t *testing.T) {
			p := newPool(t, workers, "")

			// A worker is exec'd on the first job that needs its slot, so the
			// pool has no processes until it has done some work. One job per
			// worker, run one after another, is what fills every slot.
			for i := range workers {
				in, out := job(t, pngOf(t, 800, 600))
				if _, err := p.Generate(context.Background(), preview.Request{
					Kind: preview.JobImage, Preset: preview.PresetSmall,
				}, preview.PlainSource{F: in}, out); err != nil {
					t.Fatalf("job %d: %v", i, err)
				}
			}

			var total int64
			pids := childPIDs(t)
			for _, pid := range pids {
				rss, err := procRSS(pid)
				if err != nil {
					// A worker that exited between the listing and the read.
					continue
				}
				total += rss
			}

			if len(pids) == 0 {
				t.Skip("no worker processes were resident to measure")
			}
			t.Logf("%d workers: %d resident processes, %d KB total, %d KB each",
				workers, len(pids), total, total/int64(len(pids)))
		})
	}
}
