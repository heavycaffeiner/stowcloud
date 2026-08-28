//go:build linux

package jail

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"runtime"

	"golang.org/x/sys/unix"
)

// exitConfig is the exit code a refused configuration gets, which is what a
// hardening refusal is.
const exitConfig = 78

// steps is the kernel-facing half of Apply, behind an unexported struct so a
// test can fault one without needing a kernel that refuses it.
//
// A parameter a caller could set would be a way to turn the sandbox off from
// outside, which is the thing this package exists to stop.
type steps struct {
	restrictAndReexec func(Spec, string) error
	installSeccomp    func(FilterKind) error
	reexeced          func(string) bool
	landlockAvailable func() (bool, error)
	kernel            func() string
}

func kernelSteps() steps {
	return steps{
		restrictAndReexec: RestrictAndReexec,
		installSeccomp:    InstallSeccomp,
		reexeced:          Reexeced,
		landlockAvailable: available,
		kernel:            kernelRelease,
	}
}

// Apply runs the server's hardening sequence under policy.
//
// It is called once in the life of a server process image and twice in the life
// of a server: the first time it builds the Landlock domain and replaces the
// process image, so on success it does not return at all, and the second time,
// in the new image, it installs the seccomp filter and returns the status the
// health endpoint reports.
//
// The order is the design. Landlock first, because the domain has to be built
// while the paths are still openable; the exec next, because that is what makes
// the domain cover every thread; seccomp last, because it is the only one of
// the two with an all-threads flag and it does not need to survive an exec.
//
// Under Required a step that could not be applied returns an error wrapping
// ErrHardeningRefused, and the caller renders it with Refuse and exits. A
// package that calls os.Exit itself is a package whose refusal path no test
// ever runs.
func Apply(policy Policy, spec Spec) (Status, error) {
	return apply(policy, spec, kernelSteps())
}

func apply(policy Policy, spec Spec, s steps) (Status, error) {
	st := Status{Policy: policy, Kernel: s.kernel()}
	if policy == Off {
		return st, nil
	}

	if err := applyLandlock(&st, policy, spec, s); err != nil {
		return st, err
	}

	if err := s.installSeccomp(FilterProcess); err != nil {
		st.Steps = append(st.Steps, StepStatus{Name: StepSeccomp, Err: err})
		if policy == Required {
			return st, refusal(st)
		}
		return st, nil
	}
	st.Steps = append(st.Steps, StepStatus{Name: StepSeccomp, Applied: true})
	return st, nil
}

// applyLandlock records the domain step. It returns an error only under
// Required, where a step that could not be applied stops the sequence.
func applyLandlock(st *Status, policy Policy, spec Spec, s steps) error {
	if s.reexeced(reexecMarker) {
		// This image was produced by the restrict-and-exec below, so it carries
		// the domain on every thread by inheritance.
		st.Steps = append(st.Steps, StepStatus{Name: StepLandlock, Applied: true})
		return nil
	}

	ok, err := s.landlockAvailable()
	if !ok || err != nil {
		if err == nil {
			err = fmt.Errorf("this kernel reports no Landlock ABI")
		}
		st.Steps = append(st.Steps, StepStatus{Name: StepLandlock, Err: err})
		if policy == Required {
			return refusal(*st)
		}
		return nil
	}

	// The goroutine must not migrate between the restrict and the exec, or the
	// domain is left on a thread that is not the one that execs.
	runtime.LockOSThread()
	rerr := s.restrictAndReexec(spec, reexecMarker)
	runtime.UnlockOSThread()

	// Only reached on failure: a successful exec never returns.
	st.Steps = append(st.Steps, StepStatus{Name: StepLandlock, Err: rerr})
	if policy == Required {
		return refusal(*st)
	}
	return nil
}

func refusal(st Status) error {
	step, ok := st.firstUnapplied()
	if !ok {
		return nil
	}
	return fmt.Errorf("%w: %s: %w", ErrHardeningRefused, step.Name, step.Err)
}

// Refuse writes the reason a required hardening step could not be applied and
// returns the process exit code. It names the step, the errno and the kernel,
// because "hardening failed" tells an operator nothing they can act on.
func Refuse(w io.Writer, st Status) int {
	step, ok := st.firstUnapplied()
	if !ok {
		return exitConfig
	}
	msg := fmt.Sprintf("stowcloud: refusing to start\n"+
		"  hardening is %q and the %s step could not be applied\n"+
		"  reason: %v\n"+
		"  kernel: %s\n"+
		"  set hardening = \"preferred\" to run degraded, or \"off\" to accept it\n",
		st.Policy, step.Name, step.Err, st.Kernel)
	if _, err := io.WriteString(w, msg); err != nil {
		slog.Error("the startup refusal could not be printed", slog.Any("error", err))
	}
	return exitConfig
}

func kernelRelease() string {
	var u unix.Utsname
	if err := unix.Uname(&u); err != nil {
		return "unknown"
	}
	return string(bytes.TrimRight(u.Release[:], "\x00"))
}
