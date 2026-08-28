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

// exitConfig is the exit code for a rejected configuration, which is what a
// hardening rejection amounts to.
const exitConfig = 78

// steps holds Apply's kernel-facing half behind an unexported struct, letting a
// test inject a failure without requiring a kernel that actually rejects it.
//
// Exposing this as a caller-settable parameter would create an external switch
// for disabling the sandbox, which is exactly what this package exists to
// prevent.
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

// Apply executes the server's hardening sequence under policy.
//
// It runs once per server process image and twice per server lifetime. The first
// call constructs the Landlock domain and replaces the process image, so success
// means it never returns. The second call, inside the new image, installs the
// seccomp filter and returns the status the health endpoint publishes.
//
// The ordering carries the design. Landlock comes first because the domain must
// be built while its paths remain openable. The exec follows because that is
// what spreads the domain across every thread. Seccomp comes last because it is
// the only one with an all-threads flag and has no need to survive an exec.
//
// Under Required, a step that cannot be applied yields an error wrapping
// ErrHardeningRefused; the caller formats it through Refuse and exits. A package
// calling os.Exit directly is one whose rejection path no test ever
// exercises.
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
		// The restrict-and-exec below produced this image, so every thread
		// inherits the domain.
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

	// Reached only on failure, since a successful exec never returns.
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

// Refuse reports why a required hardening step failed to apply and returns the
// process exit code. It states the step, the errno and the kernel version,
// because a bare "hardening failed" gives an operator nothing actionable.
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
