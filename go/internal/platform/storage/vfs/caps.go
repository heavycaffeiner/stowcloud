//go:build linux

package vfs

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"golang.org/x/sys/unix"
)

// Support is the outcome of probing one syscall this package's guarantees
// rest on. The three failure cases look identical from a bare failed call
// but point an operator at different fixes: a kernel too old to have the
// call, a seccomp filter refusing it, and a filesystem permission refusing
// the operand are three different afternoons.
type Support uint8

const (
	SupportUnknown Support = iota
	// SupportPresent means the syscall reached the kernel and answered.
	SupportPresent
	// SupportMissing is ENOSYS: this kernel build does not implement it.
	SupportMissing
	// SupportBlocked is EPERM: the kernel has it and a policy, ordinarily a
	// seccomp filter, refused it.
	SupportBlocked
	// SupportDenied is EACCES: the kernel has it and the filesystem refused
	// the specific operand, which is a directory permission, not a sandbox
	// profile.
	SupportDenied
)

func (s Support) String() string {
	switch s {
	case SupportPresent:
		return "present"
	case SupportMissing:
		return "missing, this kernel does not implement it"
	case SupportBlocked:
		return "blocked, a policy refused it"
	case SupportDenied:
		return "denied, the filesystem refused the operand"
	default:
		return "unknown"
	}
}

// Caps is one runtime probe of the syscalls this package depends on. Every
// probe is built so it cannot create, modify or remove anything on disk:
// each targets an invalid argument or a path guaranteed to exist, and reads
// the resulting errno as the answer.
type Caps struct {
	Kernel        string
	Openat2       Support
	StatxBtime    Support
	Renameat2     Support
	CopyFileRange Support
}

// Probe runs every check once.
func Probe() Caps {
	return Caps{
		Kernel:        kernelRelease(),
		Openat2:       probeOpenat2(),
		StatxBtime:    probeStatxBtime(),
		Renameat2:     probeRenameat2(),
		CopyFileRange: probeCopyFileRange(),
	}
}

// classify turns a syscall's own error into a Support value. EPERM and
// EACCES are kept apart deliberately: folding them sends an operator whose
// directory mode is wrong to edit a seccomp profile that was never the
// problem.
func classify(err error) Support {
	switch {
	case err == nil:
		return SupportPresent
	case errors.Is(err, unix.ENOSYS):
		return SupportMissing
	case errors.Is(err, unix.EPERM):
		return SupportBlocked
	case errors.Is(err, unix.EACCES):
		return SupportDenied
	default:
		return SupportPresent
	}
}

func kernelRelease() string {
	var u unix.Utsname
	if err := unix.Uname(&u); err != nil {
		return "unknown"
	}
	return string(bytes.TrimRight(u.Release[:], "\x00"))
}

// probeOpenat2 targets "/" rather than the working directory. The working
// directory is whatever the process happened to start in, and a container
// image that sets it to a mode-700 home directory owned by a different uid
// turns this probe into a permission test instead of a syscall-availability
// test the moment the image runs as any other uid. "/" is searchable by
// every uid this runs under, so the only thing left able to fail is the
// syscall itself.
func probeOpenat2() Support {
	how := unix.OpenHow{
		Flags:   unix.O_PATH | unix.O_DIRECTORY | unix.O_CLOEXEC,
		Resolve: unix.RESOLVE_NO_MAGICLINKS,
	}
	fd, err := unix.Openat2(unix.AT_FDCWD, "/", &how)
	if err != nil {
		return classify(err)
	}
	// The probe already has its answer; a close failing here has nothing
	// left to report to besides a log line, since returning it would make an
	// unrelated cleanup failure look like openat2 itself being unavailable.
	if cerr := unix.Close(fd); cerr != nil {
		slog.Warn("vfs: closing the openat2 probe descriptor failed", slog.Any("error", cerr))
	}
	return SupportPresent
}

// probeStatxBtime asks about the filesystem under the working directory,
// which is the honest scope for this fact: birth-time support belongs to a
// mount instance, not to the kernel as a whole, and a share sitting on a
// mount with none needs to know regardless of what other mounts support.
func probeStatxBtime() Support {
	var stx unix.Statx_t
	if err := unix.Statx(unix.AT_FDCWD, ".", 0, unix.STATX_BTIME, &stx); err != nil {
		return classify(err)
	}
	if stx.Mask&unix.STATX_BTIME == 0 {
		return SupportMissing
	}
	return SupportPresent
}

// probeRenameat2 passes a flag combination the kernel refuses before it
// looks at either path (RENAME_NOREPLACE and RENAME_EXCHANGE together are
// mutually exclusive), so nothing is renamed and no name has to exist:
// EINVAL proves the call reached the kernel, ENOSYS proves it did not.
func probeRenameat2() Support {
	err := unix.Renameat2(unix.AT_FDCWD, ".", unix.AT_FDCWD, ".",
		unix.RENAME_NOREPLACE|unix.RENAME_EXCHANGE)
	if errors.Is(err, unix.EINVAL) {
		return SupportPresent
	}
	return classify(err)
}

// probeCopyFileRange passes two invalid descriptors, so a kernel with the
// call answers EBADF and one without answers ENOSYS. Nothing is copied.
func probeCopyFileRange() Support {
	_, err := unix.CopyFileRange(-1, nil, -1, nil, 0, 0)
	return classify(err)
}

func (c Caps) String() string {
	rows := []struct {
		name string
		s    Support
	}{
		{"openat2", c.Openat2},
		{"statx btime", c.StatxBtime},
		{"renameat2", c.Renameat2},
		{"copy_file_range", c.CopyFileRange},
	}
	lines := make([]string, 0, len(rows)+1)
	lines = append(lines, fmt.Sprintf("%-15s %s", "kernel", c.Kernel))
	for _, row := range rows {
		lines = append(lines, fmt.Sprintf("%-15s %s", row.name, row.s))
	}
	return strings.Join(lines, "\n")
}
