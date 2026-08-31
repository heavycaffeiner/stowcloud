//go:build linux

package vfs

import (
	"errors"
	"fmt"
)

// openat2 is the one syscall every resolution in this package funnels
// through. Every other probe result is advisory; this one is a startup
// refusal, because the only alternative implementation is resolving a path
// one component at a time, and that shape is exactly the check-then-open
// race this whole design exists to close. There is no fallback here to
// write, and none is added later: a weaker resolver is worse than refusing
// to start.

// ErrResolverUnavailable is the sentinel for openat2 being unusable.
var ErrResolverUnavailable = errors.New("vfs: the atomic path resolver is unavailable")

// ResolverError distinguishes why the resolver is unusable, since the
// operator's next step differs: a kernel to upgrade, a seccomp profile to
// widen, or a running uid that cannot search its own working directory.
type ResolverError struct {
	Support Support
	Kernel  string
}

func (e *ResolverError) Error() string {
	switch e.Support {
	case SupportMissing:
		return fmt.Sprintf(
			"vfs: kernel %s has no openat2, and every path resolution in this server depends on it with no fallback, "+
				"since resolving a path one component at a time reopens the race this design closes. Upgrade the kernel.",
			e.Kernel)
	case SupportBlocked:
		return fmt.Sprintf(
			"vfs: kernel %s has openat2 but a policy refused it with EPERM, typically a container runtime's default "+
				"seccomp profile. There is no fallback for the same reason as always: resolving a path one component "+
				"at a time is the race this design closes. Allow openat2 in the profile.",
			e.Kernel)
	case SupportDenied:
		return fmt.Sprintf(
			"vfs: kernel %s has openat2 but the probe was refused with EACCES, a plain filesystem permission and not a "+
				"sandbox policy, usually the running uid being unable to search its own working directory. There is "+
				"no fallback. Check the user this process runs as.",
			e.Kernel)
	default:
		return fmt.Sprintf("vfs: openat2 is not usable on kernel %s and this server has no fallback for it.", e.Kernel)
	}
}

func (e *ResolverError) Is(target error) bool { return target == ErrResolverUnavailable }

// RequireResolver refuses to start unless the atomic resolver is present.
//
// It takes an already-run Caps rather than probing itself, so a caller that
// already probed does not pay for it twice, and so the refusal path can be
// exercised in a test without a kernel that genuinely lacks the call.
func RequireResolver(c Caps) error {
	if c.Openat2 == SupportPresent {
		return nil
	}
	return &ResolverError{Support: c.Openat2, Kernel: c.Kernel}
}
