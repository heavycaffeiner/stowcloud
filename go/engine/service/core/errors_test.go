package core

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
)

// allSentinels is every error this package declares, in one list, so a new
// sentinel joins the distinctness proof by being added here rather than by
// somebody remembering to write a test for it.
func allSentinels() []struct {
	name string
	err  error
} {
	return []struct {
		name string
		err  error
	}{
		{"ErrNotFound", ErrNotFound},
		{"ErrDenied", ErrDenied},
		{"ErrPrecondition", ErrPrecondition},
		{"ErrConflict", ErrConflict},
		{"ErrExists", ErrExists},
		{"ErrNotEmpty", ErrNotEmpty},
		{"ErrCrossShare", ErrCrossShare},
		{"ErrNoSpace", ErrNoSpace},
		{"ErrTrashDisabled", ErrTrashDisabled},
		{"ErrLinkExpired", ErrLinkExpired},
		{"ErrQuotaExceeded", ErrQuotaExceeded},
		{"ErrShareBroken", ErrShareBroken},
	}
}

func TestEverySentinelIsDistinct(t *testing.T) {
	set := allSentinels()
	for _, a := range set {
		for _, b := range set {
			if a.name == b.name {
				continue
			}
			if errors.Is(a.err, b.err) {
				t.Fatalf("errors.Is(%s, %s) is true; the two sentinels are the same value", a.name, b.name)
			}
		}
	}
}

func TestShareBrokenErrorUnwrapsToItsSentinel(t *testing.T) {
	err := error(&ShareBrokenError{Share: "documents", Reason: "missing"})
	if !errors.Is(err, ErrShareBroken) {
		t.Fatalf("errors.Is(%v, ErrShareBroken) is false", err)
	}
	var target *ShareBrokenError
	if !errors.As(err, &target) {
		t.Fatalf("errors.As did not find a *ShareBrokenError in %v", err)
	}
	if target.Share != "documents" || target.Reason != "missing" {
		t.Fatalf("payload = %+v, want share documents, reason missing", target)
	}
}

func TestPreconditionErrorUnwrapsToItsSentinel(t *testing.T) {
	err := error(&PreconditionError{Current: "abc123"})
	if !errors.Is(err, ErrPrecondition) {
		t.Fatalf("errors.Is(%v, ErrPrecondition) is false", err)
	}
	if !IsPrecondition(err) {
		t.Fatal("IsPrecondition is false for a *PreconditionError")
	}
	var target *PreconditionError
	if !errors.As(err, &target) || target.Current != "abc123" {
		t.Fatalf("errors.As gave %+v, want the current token abc123", target)
	}
}

func TestPreconditionErrorCarriesAnEmptyTokenForAMissingTarget(t *testing.T) {
	err := error(&PreconditionError{})
	if !IsPrecondition(err) {
		t.Fatal("IsPrecondition is false for a *PreconditionError with no token")
	}
	if !strings.Contains(err.Error(), ErrPrecondition.Error()) {
		t.Fatalf("Error() = %q, want it to carry the sentinel's own text", err.Error())
	}
}

func TestIsPreconditionIsFalseForEveryOtherSentinel(t *testing.T) {
	for _, s := range allSentinels() {
		if s.name == "ErrPrecondition" {
			continue
		}
		if IsPrecondition(s.err) {
			t.Fatalf("IsPrecondition(%s) is true", s.name)
		}
	}
}

func TestMapVFSErrMapsEveryNamedError(t *testing.T) {
	cases := []struct {
		name string
		in   error
		want error
	}{
		{"not found", vfs.ErrNotFound, ErrNotFound},
		{"denied", vfs.ErrDenied, ErrDenied},
		{"symlink denied", vfs.ErrSymlinkDenied, ErrDenied},
		{"exists", vfs.ErrExists, ErrExists},
		{"not empty", vfs.ErrNotEmpty, ErrNotEmpty},
		{"no space", vfs.ErrNoSpace, ErrNoSpace},
		{"cross device", vfs.ErrCrossDevice, ErrCrossShare},
		{"not a directory", vfs.ErrNotADirectory, ErrNotFound},
		{"is a directory", vfs.ErrIsDirectory, ErrDenied},
		{"invalid name", vfs.ErrInvalidName, ErrNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mapVFSErr(tc.in); !errors.Is(got, tc.want) {
				t.Fatalf("mapVFSErr(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestMapVFSErrMapsAWrappedError(t *testing.T) {
	// Every vfs sentinel arrives wrapped by the operation that produced it,
	// so a mapping that only matched a bare value would map nothing real.
	wrapped := fmt.Errorf("openat2: %w", vfs.ErrDenied)
	if got := mapVFSErr(wrapped); !errors.Is(got, ErrDenied) {
		t.Fatalf("mapVFSErr(%v) = %v, want ErrDenied", wrapped, got)
	}
}

func TestMapVFSErrPassesAnUnnamedErrorThroughUnchanged(t *testing.T) {
	// An error the table does not name is an infrastructure failure, and it
	// keeps its identity so a caller can still match it.
	infra := errors.New("the disk controller reset")
	got := mapVFSErr(infra)
	if !errors.Is(got, infra) {
		t.Fatalf("mapVFSErr(%v) = %v, want the original error", infra, got)
	}
	for _, s := range allSentinels() {
		if errors.Is(got, s.err) {
			t.Fatalf("an unnamed error was mapped to %s", s.name)
		}
	}
}

func TestMapVFSErrPassesNilThrough(t *testing.T) {
	if got := mapVFSErr(nil); got != nil {
		t.Fatalf("mapVFSErr(nil) = %v, want nil", got)
	}
}

// The typed errors are the two that could carry a host path, since both are
// built from values the registry holds. Neither may.
func TestTypedErrorsLeakNoHostPath(t *testing.T) {
	broken := (&ShareBrokenError{Share: "documents", Reason: "unreadable"}).Error()
	precondition := (&PreconditionError{Current: "deadbeef"}).Error()
	for _, msg := range []string{broken, precondition} {
		if strings.Contains(msg, "/") || strings.Contains(msg, `\`) {
			t.Fatalf("error message %q carries a path separator", msg)
		}
	}
}
