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

// Grant is one path the domain allows something beneath.
type Grant struct {
	// Path is a host path. It is opened once to build the rule and closed
	// again; the domain outlives the descriptor.
	Path string

	// Access is the rights allowed beneath Path. Zero grants every right the
	// ruleset handles, which is what a share root gets.
	Access uint64
}

// Spec describes a Landlock domain. The ruleset handles every filesystem right
// the running kernel reports, so a right that is handled and not granted is
// denied everywhere; a Spec with no grants at all denies the whole filesystem.
type Spec struct {
	// ExceptExec leaves LANDLOCK_ACCESS_FS_EXECUTE unhandled.
	//
	// This is not a weakening and it is worth being explicit about. A ruleset
	// that handled EXECUTE and granted nothing would deny the worker's own
	// execve, which is how the domain becomes process-wide in the first place.
	// Denying execve is seccomp's job in the step after, and seccomp denies it
	// harder: Landlock would refuse to execute a file, while seccomp removes
	// the syscall, so there is nothing to execute and nothing to point it at.
	ExceptExec bool

	GrantBeneath []Grant
}

// abiVersion asks the kernel which Landlock ABI it implements.
//
// The two failures are different facts to an operator and are kept apart:
// ENOSYS is a kernel older than 5.13, and EOPNOTSUPP is Landlock compiled out
// or disabled at boot.
func abiVersion() (int, error) {
	v, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET,
		0, 0, uintptr(unix.LANDLOCK_CREATE_RULESET_VERSION))
	if errno != 0 {
		return 0, fmt.Errorf("landlock_create_ruleset(version): %w", errno)
	}
	return int(v), nil
}

// ABIVersion reports the running kernel's Landlock ABI, for the capability
// probe. Zero and an error mean the kernel has none.
func ABIVersion() (int, error) { return abiVersion() }

// fsRights is the filesystem access set each ABI version added, accumulated up
// to abi.
//
// A kernel newer than this table reports an ABI whose extra rights are not
// handled, which is the conservative direction: an unhandled right is one
// Landlock does not police, so the worst case is the domain permitting
// something the process was permitted to do anyway. Guessing at a constant this
// build does not have a name for is the other direction.
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

// readExecute is what the binary's own path needs: the process has to be able
// to read and execute itself to complete the re-exec that makes the domain
// process-wide.
const readExecute = unix.LANDLOCK_ACCESS_FS_READ_FILE | unix.LANDLOCK_ACCESS_FS_EXECUTE

// ReadOnly is a grant that can read files and list directories and nothing
// else. It is for a path the server reads and must never write, such as the
// operator's own configuration.
const ReadOnly = unix.LANDLOCK_ACCESS_FS_READ_FILE | unix.LANDLOCK_ACCESS_FS_READ_DIR

// restrict builds spec's domain and applies it to the calling thread.
//
// The caller must already hold the OS thread. Landlock restricts the calling
// thread and has no all-threads flag, so a goroutine that migrates between this
// and the exec that follows leaves the domain on a thread that is not the one
// that execs, and the call still returns success.
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
		//nolint:gosec // the kernel takes a struct pointer; there is no wrapper for this call.
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

	// Required before an unprivileged process may restrict itself.
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
	// A right the ruleset does not handle is EINVAL from the kernel, so the
	// grant is narrowed to what is actually being policed.
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
		//nolint:gosec // as above, and the first twelve bytes are the packed layout the kernel reads.
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

// available reports whether this kernel can carry a domain at all, so Preferred
// can name the reason rather than reporting a generic failure.
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
