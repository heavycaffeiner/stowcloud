# The jail proof

Four combinations are asked for: two architectures, two kernels, and the strict
and permissive policies across them. The one that matters most is the strict
policy on a kernel without Landlock, which must refuse to start.

## What was proved, and how

This host has one kernel and one architecture, so a second kernel cannot be
booted. The kernel is made to look like one without Landlock instead: a seccomp
filter answers the Landlock syscall with `ENOSYS`, which is exactly what a
kernel that does not implement it returns, and the availability probe has no
other way to ask. From inside, the two are indistinguishable, which is the whole
point.

The filter is installed in a re-executed child, because it cannot be removed
once installed and the rest of the suite needs the real answer. The child
asserts Landlock is available before the filter and gone after it, so a run
where the filter failed to install cannot pass by accident, and it prints a
marker only after the refusal has actually been seen. The parent fails without
that marker: a child that skipped also exits zero and also prints a pass, which
is a shape that already produced a false green once in this port.

`go test ./internal/jail -run RequiredRefusesWithoutLandlock`

| Combination | Result |
|---|---|
| x86_64, Landlock present, `required` | applies both layers, starts |
| x86_64, Landlock present, `preferred` | applies both layers, no degradation |
| x86_64, Landlock absent, `required` | **refuses to start, naming the landlock step** |
| x86_64, Landlock absent, `preferred` | starts, reports the degradation |
| x86_64, Landlock absent, `off` | starts, reports nothing it never attempted |

The test was checked against a deliberately broken build: removing the refusal
from the strict path makes it fail with `the strict policy gave <nil>, want a
refusal`. A proof that passes against a build with the property removed is not
a proof.

## What was not proved

**aarch64, in every combination.** This machine is x86_64 and no aarch64 host
was available. The seccomp filter's own architecture check is the reason this
matters: a filter that compares a syscall number without first checking the
architecture reads the wrong number under a foreign ABI, and that is a defect
this project has already recorded once. The Go filter checks the architecture
and refuses an unmapped one, and that path is exercised by the filter's own
tests rather than by running on the second architecture.

**A genuinely different kernel.** The simulation covers the availability probe,
which is what the policy branches on. It does not cover a kernel whose Landlock
implementation differs in some other way, because there is no such kernel here
to run against.

Both gaps are the same shape: a second machine would close them, and nothing
about the code prevents it.
