package files

import (
	"errors"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/backend/internal/fs/vfs"
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
	t.Parallel()
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
	t.Parallel()
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

func TestMapVFSErrMapsEveryNamedError(t *testing.T) {
	t.Parallel()
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

// The typed errors are the two that could carry a host path, since both are
// built from values the registry holds. Neither may.
func TestTypedErrorsLeakNoHostPath(t *testing.T) {
	t.Parallel()
	broken := (&ShareBrokenError{Share: "documents", Reason: "unreadable"}).Error()
	precondition := (&PreconditionError{Current: "deadbeef"}).Error()
	for _, msg := range []string{broken, precondition} {
		if strings.Contains(msg, "/") || strings.Contains(msg, `\`) {
			t.Fatalf("error message %q carries a path separator", msg)
		}
	}
}
