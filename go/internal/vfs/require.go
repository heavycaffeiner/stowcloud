//go:build linux

package vfs

import (
	"errors"
	"fmt"
)

// The one syscall this package cannot do without.
//
// Every other probe result is advice. This one is a startup refusal under every
// hardening policy, including the one that turns hardening off, because "off"
// means the operator accepts a weaker sandbox and not that they accept a path
// resolver that can be raced.
//
// There is no fallback and none is being written. The only one anyone would
// write is resolving a path one component at a time, which is the
// normalise-then-open shape the whole design exists to refuse: between checking
// a component and opening it, that component can become a symlink. That would
// reintroduce the race rather than narrow the sandbox, so a weaker resolver is
// worse than not starting.

// ErrResolverUnavailable is the atomic path resolver being unusable.
var ErrResolverUnavailable = errors.New("vfs: the atomic path resolver is unavailable")

// ResolverError says which of the two failures happened, because the operator's
// next step is different for each: one is a kernel to upgrade and the other is
// a sandbox profile to change.
type ResolverError struct {
	Support Support
	Kernel  string
}

func (e *ResolverError) Error() string {
	switch e.Support {
	case SupportMissing:
		return fmt.Sprintf(
			"vfs: this kernel (%s) does not implement openat2, which this server resolves every path with. "+
				"There is no fallback: resolving a path one component at a time is the race this design exists to close. "+
				"Upgrade to a kernel that has it", e.Kernel)
	case SupportBlocked:
		return fmt.Sprintf(
			"vfs: openat2 exists on this kernel (%s) and a seccomp filter refused it with EPERM, which is usually a "+
				"container runtime's default profile. This server resolves every path with it and has no fallback, "+
				"because resolving a path one component at a time is the race this design exists to close. "+
				"Allow openat2 in the profile", e.Kernel)
	case SupportDenied:
		return fmt.Sprintf(
			"vfs: openat2 exists on this kernel (%s) and the probe was refused with EACCES, which is a filesystem "+
				"permission and not a seccomp profile. This server resolves every path with it and has no fallback, "+
				"because resolving a path one component at a time is the race this design exists to close. "+
				"The usual cause is running the image as a uid that cannot search its own working directory, or "+
				"a mounted directory the running uid cannot reach. "+
				"Check the user the container runs as", e.Kernel)
	}
	return fmt.Sprintf("vfs: openat2 is not usable on this kernel (%s) and this server has no fallback", e.Kernel)
}

func (e *ResolverError) Is(target error) bool { return target == ErrResolverUnavailable }

// RequireResolver refuses to start when the atomic resolver is unusable.
//
// It takes a probe result rather than running one, so a caller that has already
// probed does not probe twice, and so the refusal can be tested without a
// kernel that actually lacks the call.
func RequireResolver(c Caps) error {
	if c.Openat2 == SupportPresent {
		return nil
	}
	return &ResolverError{Support: c.Openat2, Kernel: c.Kernel}
}
