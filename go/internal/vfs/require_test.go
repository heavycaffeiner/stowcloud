//go:build linux

package vfs

import (
	"errors"
	"strings"
	"testing"
)

// A blocked resolver and a missing one look identical from a failed call and
// need different answers from an operator: one is a sandbox profile to change,
// the other is a kernel to upgrade. Reporting them the same way sends half of
// them to the wrong place.
func TestTheRefusalDistinguishesBlockedFromMissing(t *testing.T) {
	blocked := RequireResolver(Caps{Openat2: SupportBlocked, Kernel: "6.1.0"})
	if !errors.Is(blocked, ErrResolverUnavailable) {
		t.Fatalf("a blocked resolver gave %v, want a refusal", blocked)
	}
	missing := RequireResolver(Caps{Openat2: SupportMissing, Kernel: "5.4.0"})
	if !errors.Is(missing, ErrResolverUnavailable) {
		t.Fatalf("a missing resolver gave %v, want a refusal", missing)
	}

	// The two messages have to differ, or the distinction the probe went to
	// the trouble of making is thrown away at the point it matters.
	if blocked.Error() == missing.Error() {
		t.Fatal("the two failures report the same message")
	}
	// And each says what to do about it.
	if !strings.Contains(blocked.Error(), "profile") {
		t.Errorf("the blocked message does not point at a sandbox profile: %v", blocked)
	}
	if !strings.Contains(missing.Error(), "Upgrade") {
		t.Errorf("the missing message does not point at the kernel: %v", missing)
	}
	// Both name the kernel, which is what an operator quotes in a bug report.
	if !strings.Contains(blocked.Error(), "6.1.0") || !strings.Contains(missing.Error(), "5.4.0") {
		t.Error("a message does not name the kernel")
	}
}

// An unknown result is a refusal too. A probe that could not answer is not the
// same as one that answered yes.
func TestAnUnknownProbeResultIsARefusal(t *testing.T) {
	if err := RequireResolver(Caps{Openat2: SupportUnknown}); !errors.Is(err, ErrResolverUnavailable) {
		t.Fatalf("an unknown result gave %v, want a refusal", err)
	}
}

func TestAPresentResolverStarts(t *testing.T) {
	if err := RequireResolver(Caps{Openat2: SupportPresent}); err != nil {
		t.Fatalf("a present resolver gave %v", err)
	}
}

// The refusal says there is no fallback, because the obvious next question is
// why the server does not just do it another way, and the answer is that the
// other way is the race this design closes.
func TestTheRefusalSaysThereIsNoFallback(t *testing.T) {
	for _, s := range []Support{SupportBlocked, SupportMissing} {
		msg := RequireResolver(Caps{Openat2: s, Kernel: "6.1.0"}).Error()
		if !strings.Contains(msg, "no fallback") {
			t.Errorf("the message for %v does not say there is no fallback: %s", s, msg)
		}
	}
}

// This host has the call, which is also what makes every other test in this
// package meaningful.
func TestThisHostHasTheResolver(t *testing.T) {
	c := Probe()
	if c.Openat2 != SupportPresent {
		t.Fatalf("openat2 is %v on this host, so this package's guarantees do not hold here", c.Openat2)
	}
}
