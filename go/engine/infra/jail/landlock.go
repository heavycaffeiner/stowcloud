//go:build linux

package jail

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"unsafe"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
	"golang.org/x/sys/unix"
)

// Grant names a path beneath which the domain permits something.
type Grant struct {
	// Path is a host path, opened once while constructing the rule and then
	// closed. The domain persists beyond the descriptor.
	Path string

	// Access lists the rights permitted beneath Path. Zero confers every right
	// the ruleset handles, which is what a share root receives.
	Access uint64
}

// Spec describes a Landlock domain. The ruleset covers every filesystem right
// the running kernel advertises, so any handled right left ungranted is denied
// throughout. A Spec carrying no grants denies the entire filesystem.
type Spec struct {
	// ExceptExec leaves LANDLOCK_ACCESS_FS_EXECUTE outside the handled set.
	//
	// This does not weaken anything, and saying so explicitly is worthwhile. A
	// ruleset handling EXECUTE while granting nothing would block the worker's
	// own execve, which is precisely how the domain becomes process-wide.
	// Blocking execve belongs to seccomp in the following step, and seccomp does
	// it more thoroughly: Landlock would decline to execute a file, whereas
	// seccomp eliminates the syscall, leaving nothing to execute and nothing to
	// aim it at.
	ExceptExec bool

	GrantBeneath []Grant
}

// abiVersion queries the kernel for the Landlock ABI it implements.
//
// The two failure modes tell an operator different things and stay separate:
// ENOSYS means a kernel predating 5.13, while EOPNOTSUPP means Landlock was
// compiled out or disabled at boot.
func abiVersion() (int, error) {
	v, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET,
		0, 0, uintptr(unix.LANDLOCK_CREATE_RULESET_VERSION))
	if errno != 0 {
		return 0, fmt.Errorf("landlock_create_ruleset(version): %w", errno)
	}
	return int(v), nil
}

// ABIVersion returns the running kernel's Landlock ABI for the capability probe.
// Zero together with an error indicates the kernel provides none.
func ABIVersion() (int, error) { return abiVersion() }

// fsRights accumulates, up to abi, the filesystem access set introduced by each
// ABI version.
//
// A kernel newer than this table reports an ABI whose additional rights go
// unhandled, which errs safely: an unhandled right is one Landlock does not
// enforce, so the worst outcome is the domain allowing something the process
// could already do. Guessing at a constant this build has no name for would err
// the other way.
func fsRights(abi int) uint64 {
	if abi < 1 {
		return 0
	}
	r := uint64(unix.LANDLOCK_ACCESS_FS_EXECUTE |
		unix.LANDLOCK_ACCESS_FS_WRITE_FILE |
		unix.LANDLOCK_ACCESS_FS_READ_FILE |
		unix.LANDLOCK_ACCESS_FS_READ_DIR |
		unix.LANDLOCK_ACCESS_FS_REMOVE_DIR |
		unix.LANDLOCK_ACCESS_FS_REMOVE_FILE |
		unix.LANDLOCK_ACCESS_FS_MAKE_CHAR |
		unix.LANDLOCK_ACCESS_FS_MAKE_DIR |
		unix.LANDLOCK_ACCESS_FS_MAKE_REG |
		unix.LANDLOCK_ACCESS_FS_MAKE_SOCK |
		unix.LANDLOCK_ACCESS_FS_MAKE_FIFO |
		unix.LANDLOCK_ACCESS_FS_MAKE_BLOCK |
		unix.LANDLOCK_ACCESS_FS_MAKE_SYM)
	if abi >= 2 {
		r |= unix.LANDLOCK_ACCESS_FS_REFER
	}
	if abi >= 3 {
		r |= unix.LANDLOCK_ACCESS_FS_TRUNCATE
	}
	if abi >= 5 {
		r |= unix.LANDLOCK_ACCESS_FS_IOCTL_DEV
	}
	return r
}

// readExecute covers what the binary's own path requires: reading and executing
// itself, which the re-exec that makes the domain process-wide depends on.
const readExecute = unix.LANDLOCK_ACCESS_FS_READ_FILE | unix.LANDLOCK_ACCESS_FS_EXECUTE

// discardDevice covers /dev/null, which os/exec opens for a child's stdio
// whenever the caller leaves it nil. The server spawns a decoder worker that
// way, so a domain without this grant makes every thumbnail fail with
// "permission denied" on a path the request never named.
//
// It grants nothing: a read gives EOF and a write is discarded. Reading and
// writing both, because os/exec opens it for stdin as well as the two output
// streams.
const discardDevice = unix.LANDLOCK_ACCESS_FS_READ_FILE | unix.LANDLOCK_ACCESS_FS_WRITE_FILE

// ReadOnly grants file reads and directory listings and nothing further. It
// suits a path the server reads but must never write, such as the operator's own
// configuration.
const ReadOnly = unix.LANDLOCK_ACCESS_FS_READ_FILE | unix.LANDLOCK_ACCESS_FS_READ_DIR

// restrict constructs spec's domain and applies it to the calling thread.
//
// The caller must already be locked to the OS thread. Landlock restricts only
// the calling thread and offers no all-threads flag, so a goroutine migrating
// between this call and the subsequent exec leaves the domain attached to a
// thread other than the one that execs, while the call still reports
// success.
func restrict(spec Spec) error {
	abi, err := abiVersion()
	if err != nil {
		return err
	}
	handled := fsRights(abi)
	if spec.ExceptExec {
		handled &^= unix.LANDLOCK_ACCESS_FS_EXECUTE
	}
	if handled == 0 {
		return fmt.Errorf("landlock ABI %d handles no filesystem right this build knows", abi)
	}

	attr := unix.LandlockRulesetAttr{Access_fs: handled}
	fd, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET,
		//nolint:gosec // this call has no x/sys wrapper and the kernel wants a struct pointer.
		uintptr(unsafe.Pointer(&attr)), unsafe.Sizeof(attr), 0)
	runtime.KeepAlive(attr)
	if errno != 0 {
		return fmt.Errorf("landlock_create_ruleset: %w", errno)
	}
	ruleset := os.NewFile(fd, "landlock ruleset")
	defer closeAfter(ruleset, "landlock ruleset")

	for _, g := range spec.GrantBeneath {
		if aerr := addPathBeneath(ruleset, g, handled); aerr != nil {
			return aerr
		}
	}

	// A precondition for any unprivileged process restricting itself.
	if perr := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); perr != nil {
		return fmt.Errorf("prctl(PR_SET_NO_NEW_PRIVS): %w", perr)
	}
	if rerr := restrictSelf(ruleset); rerr != nil {
		return rerr
	}
	if abi < 3 {
		slog.Warn("the running kernel implements an older Landlock ABI, so the rights it does not know are not policed",
			slog.Int("abi", abi))
	}
	return nil
}

func addPathBeneath(ruleset *os.File, g Grant, handled uint64) error {
	access := g.Access
	if access == 0 {
		access = handled
	}
	// Rights outside the ruleset's handled set draw EINVAL from the kernel, so
	// the grant is trimmed to what is actually enforced.
	access &= handled
	if access == 0 {
		return nil
	}

	target, err := os.OpenFile(g.Path, os.O_RDONLY|unix.O_PATH|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("landlock grant %s: %w", g.Path, err)
	}
	defer closeAfter(target, "landlock grant path")

	parent, err := num.Narrow[int32](int(target.Fd()))
	runtime.KeepAlive(target)
	if err != nil {
		return fmt.Errorf("landlock grant %s: %w", g.Path, err)
	}

	rule := unix.LandlockPathBeneathAttr{Allowed_access: access, Parent_fd: parent}
	_, _, errno := unix.Syscall6(unix.SYS_LANDLOCK_ADD_RULE,
		ruleset.Fd(), uintptr(unix.LANDLOCK_RULE_PATH_BENEATH),
		//nolint:gosec // same as above; the kernel parses the packed layout in the leading twelve bytes.
		uintptr(unsafe.Pointer(&rule)), 0, 0, 0)
	runtime.KeepAlive(ruleset)
	runtime.KeepAlive(rule)
	if errno != 0 {
		return fmt.Errorf("landlock_add_rule %s: %w", g.Path, errno)
	}
	return nil
}

func restrictSelf(ruleset *os.File) error {
	_, _, errno := unix.Syscall(unix.SYS_LANDLOCK_RESTRICT_SELF, ruleset.Fd(), 0, 0)
	runtime.KeepAlive(ruleset)
	if errno != 0 {
		return fmt.Errorf("landlock_restrict_self: %w", errno)
	}
	return nil
}

// available reports whether this kernel supports a domain at all, letting
// Preferred state the reason instead of returning a generic failure.
func available() (bool, error) {
	abi, err := abiVersion()
	if err != nil {
		if errors.Is(err, unix.ENOSYS) {
			return false, fmt.Errorf("this kernel is older than Landlock, which needs 5.13: %w", err)
		}
		return false, err
	}
	return abi >= 1, nil
}

func closeAfter(f *os.File, what string) {
	if err := f.Close(); err != nil {
		slog.Warn("closing a descriptor failed",
			slog.String("what", what), slog.Any("error", err))
	}
}
