package auth

import "testing"

// resolvePasswordParams' floor is exercised through resolvePasswordParamsFor
// directly, with the test-binary flag passed in, so the assertion is about
// the rule itself and not about whether this file happens to run inside a
// test binary.
func TestResolvePasswordParamsRefusesAWeakerFloorOutsideATestBinary(t *testing.T) {
	t.Parallel()
	weak := Params{MemoryKiB: 1024, Iterations: 1, Parallelism: 1, KeyLen: 32}

	if got := resolvePasswordParamsFor(weak, false); got != CurrentParams() {
		t.Fatalf("outside a test binary, weaker parameters resolved to %+v, want %+v", got, CurrentParams())
	}
	if got := resolvePasswordParamsFor(weak, true); got != weak {
		t.Fatalf("inside a test binary, the requested parameters resolved to %+v, want %+v", got, weak)
	}

	// A set naming the real cost with a one-byte derived key is weaker than
	// it looks, and is the case a memory-and-iterations-only floor let past.
	short := CurrentParams()
	short.KeyLen = 1
	if got := resolvePasswordParamsFor(short, false); got != CurrentParams() {
		t.Fatalf("outside a test binary, a 1-byte key resolved to %+v, want %+v", got, CurrentParams())
	}

	strong := Params{MemoryKiB: CurrentParams().MemoryKiB * 2, Iterations: CurrentParams().Iterations, Parallelism: 1, KeyLen: 32}
	if got := resolvePasswordParamsFor(strong, false); got != strong {
		t.Fatalf("stronger parameters were overridden to %+v, want %+v", got, strong)
	}
}
