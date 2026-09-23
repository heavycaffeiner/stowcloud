//go:build linux

package vfs

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// A blocked resolver and a missing one fail identically at the syscall
// layer but need an operator sent to two different places: one edits a
// sandbox profile, the other upgrades a kernel. The refusal must keep the
// two apart rather than reporting one generic failure.
func TestRequireResolverDistinguishesBlockedFromMissing(t *testing.T) {
	blocked := RequireResolver(Caps{Openat2: SupportBlocked, Kernel: "6.1.0"})
	missing := RequireResolver(Caps{Openat2: SupportMissing, Kernel: "5.4.0"})

	if !errors.Is(blocked, ErrResolverUnavailable) {
		t.Fatalf("blocked: %v, want ErrResolverUnavailable", blocked)
	}
	if !errors.Is(missing, ErrResolverUnavailable) {
		t.Fatalf("missing: %v, want ErrResolverUnavailable", missing)
	}
	if blocked.Error() == missing.Error() {
		t.Fatal("blocked and missing report the same message")
	}
	if !strings.Contains(blocked.Error(), "profile") {
		t.Errorf("blocked message does not mention a profile: %v", blocked)
	}
	if !strings.Contains(missing.Error(), "Upgrade") {
		t.Errorf("missing message does not say to upgrade: %v", missing)
	}
	if !strings.Contains(blocked.Error(), "6.1.0") || !strings.Contains(missing.Error(), "5.4.0") {
		t.Error("a message does not name the kernel it was given")
	}
}

// EACCES is a filesystem permission and EPERM is a policy refusal; folding
// the two sends an operator whose directory mode is wrong to edit a
// seccomp profile that was never at fault.
func TestRequireResolverDeniedDoesNotBlameAPolicy(t *testing.T) {
	denied := RequireResolver(Caps{Openat2: SupportDenied, Kernel: "6.1.0"})
	if !errors.Is(denied, ErrResolverUnavailable) {
		t.Fatalf("denied: %v, want ErrResolverUnavailable", denied)
	}
	if strings.Contains(denied.Error(), "profile") {
		t.Errorf("denied message points at a profile: %v", denied)
	}
	if !strings.Contains(denied.Error(), "uid") {
		t.Errorf("denied message does not mention the running uid: %v", denied)
	}
	blocked := RequireResolver(Caps{Openat2: SupportBlocked, Kernel: "6.1.0"})
	if denied.Error() == blocked.Error() {
		t.Fatal("denied and blocked report the same message")
	}
}

// The probe must not depend on the process working directory. A base image
// that starts a process in a directory it cannot search would otherwise
// make this probe answer EACCES for a reason that has nothing to do with
// openat2 itself.
func TestProbeOpenat2IgnoresTheWorkingDirectory(t *testing.T) {
	if Probe().Openat2 != SupportPresent {
		t.Skip("openat2 is unavailable here for an unrelated reason")
	}
	dir := t.TempDir()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Skipf("cannot drop the mode on the working directory: %v", err)
	}
	t.Cleanup(func() {
		// The execute bit goes back so the framework can descend into the
		// directory to remove it.
		if err := os.Chmod(dir, 0o700); err != nil {
			t.Errorf("restoring the working directory mode: %v", err)
		}
	})

	if got := Probe().Openat2; got != SupportPresent {
		t.Fatalf("openat2 probed as %v from an unsearchable working directory, want present", got)
	}
}

// SupportUnknown, an unanswered probe, is a refusal too: not having probed
// is not the same fact as having probed and found the syscall present.
func TestRequireResolverUnknownIsARefusal(t *testing.T) {
	if err := RequireResolver(Caps{Openat2: SupportUnknown}); !errors.Is(err, ErrResolverUnavailable) {
		t.Fatalf("unknown: %v, want ErrResolverUnavailable", err)
	}
}

func TestRequireResolverPresentStarts(t *testing.T) {
	if err := RequireResolver(Caps{Openat2: SupportPresent}); err != nil {
		t.Fatalf("present: %v, want nil", err)
	}
}

// Every refusal states plainly that there is no fallback, since the next
// question an operator asks is why the server does not just walk the path
// component by component, and the answer is that doing so reopens the race
// this design closes.
func TestRequireResolverStatesThereIsNoFallback(t *testing.T) {
	for _, s := range []Support{SupportBlocked, SupportMissing, SupportDenied} {
		msg := RequireResolver(Caps{Openat2: s, Kernel: "6.1.0"}).Error()
		if !strings.Contains(msg, "no fallback") {
			t.Errorf("%v: message does not say there is no fallback: %s", s, msg)
		}
	}
}

// This host must have the resolver, or every escape and confinement test
// elsewhere in the package is meaningless.
func TestThisHostHasOpenat2(t *testing.T) {
	c := Probe()
	if c.Openat2 != SupportPresent {
		t.Fatalf("openat2 is %v on this host; this package's guarantees do not hold here", c.Openat2)
	}
}
