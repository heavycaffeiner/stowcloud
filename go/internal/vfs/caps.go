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

// Support is what a probe found, and the distinction it exists for is between
// the last two: a syscall an old kernel does not have and one a seccomp profile
// refused look identical from a failed call and need different answers from an
// operator. Docker's default profile blocking openat2 is not the same problem
// as a kernel below 5.6.
type Support uint8

const (
	SupportUnknown Support = iota
	// SupportPresent means the kernel has the call. It does not mean every use
	// of it will succeed.
	SupportPresent
	// SupportMissing is ENOSYS: this kernel does not implement it.
	SupportMissing
	// SupportBlocked is EPERM: it exists and a policy refused it, which for a
	// syscall is a seccomp filter.
	SupportBlocked
	// SupportDenied is EACCES: the call reached the kernel and the filesystem
	// refused the operand. It is kept apart from SupportBlocked because the two
	// send an operator to opposite places: a seccomp profile and a directory
	// mode. Reporting a mode as a profile costs an afternoon.
	SupportDenied
)

func (s Support) String() string {
	switch s {
	case SupportPresent:
		return "present"
	case SupportMissing:
		return "missing (this kernel does not implement it)"
	case SupportBlocked:
		return "blocked (the kernel has it and a policy refused it)"
	case SupportDenied:
		return "denied (the kernel has it and the filesystem refused the path)"
	}
	return "unknown"
}

// Caps is one runtime probe of the syscalls this package's guarantees rest on.
// Run once at startup; nothing here has a side effect on the filesystem.
type Caps struct {
	Kernel        string
	Openat2       Support
	StatxBtime    Support
	Renameat2     Support
	CopyFileRange Support
	CloseRange    Support
	Inotify       Support
	Seccomp       Support
}

// Probe runs every check. Each one is chosen so that it cannot create, modify
// or remove anything: the calls are made against invalid arguments or names
// that do not exist, and the errno is the answer.
func Probe() Caps {
	return Caps{
		Kernel:        kernelRelease(),
		Openat2:       probeOpenat2(),
		StatxBtime:    probeStatxBtime(),
		Renameat2:     probeRenameat2(),
		CopyFileRange: probeCopyFileRange(),
		CloseRange:    probeCloseRange(),
		Inotify:       probeInotify(),
		Seccomp:       probeSeccomp(),
	}
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
		{"close_range", c.CloseRange},
		{"inotify", c.Inotify},
		{"seccomp", c.Seccomp},
	}
	lines := make([]string, 0, len(rows)+1)
	lines = append(lines, fmt.Sprintf("%-15s %s", "kernel", c.Kernel))
	for _, row := range rows {
		lines = append(lines, fmt.Sprintf("%-15s %s", row.name, row.s))
	}
	return strings.Join(lines, "\n") + "\n"
}

// classify is the whole point of this file: ENOSYS is absence, EPERM is a
// policy, EACCES is the filesystem refusing the operand, and anything else
// means the call reached the kernel and got a real answer, which is what
// "present" means here.
//
// EPERM and EACCES were folded together once. They are not the same fact: a
// seccomp filter returns EPERM, while a directory whose mode does not let this
// uid search it returns EACCES, and an operator told to edit a seccomp profile
// because of a directory mode will not find anything wrong with the profile.
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
	}
	return SupportPresent
}

func kernelRelease() string {
	var u unix.Utsname
	if err := unix.Uname(&u); err != nil {
		return "unknown"
	}
	return string(bytes.TrimRight(u.Release[:], "\x00"))
}

// probeOpenat2 asks about the root directory rather than the working directory.
//
// The operand has to be one that cannot fail for a reason unrelated to the
// syscall. "." is not: the working directory is whatever the image or the
// operator set, and the base this shipped on put it at a home directory mode
// 700 and owned by the image's own uid. Running the image under any other uid
// made this probe return EACCES, which was then reported as a seccomp profile
// blocking openat2, and no amount of editing the profile fixed it. "/" is
// searchable by every uid on every system this runs on, so what is left to
// fail is the syscall itself.
func probeOpenat2() Support {
	how := unix.OpenHow{
		Flags:   unix.O_PATH | unix.O_DIRECTORY | unix.O_CLOEXEC,
		Resolve: unix.RESOLVE_NO_MAGICLINKS,
	}
	fd, err := unix.Openat2(unix.AT_FDCWD, "/", &how)
	if err == nil {
		closeQuiet(fd)
	}
	return classify(err)
}

// probeStatxBtime answers about the filesystem under the working directory as
// much as about the kernel, which is the honest answer: btime is a per-
// filesystem fact and a share on a mount that has none needs to know.
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

// probeRenameat2 passes a flag combination the kernel rejects before it looks
// at either path, so nothing is renamed and no name has to be invented: EINVAL
// means the syscall is there, ENOSYS means it is not.
func probeRenameat2() Support {
	err := unix.Renameat2(unix.AT_FDCWD, ".", unix.AT_FDCWD, ".",
		unix.RENAME_NOREPLACE|unix.RENAME_EXCHANGE)
	if errors.Is(err, unix.EINVAL) {
		return SupportPresent
	}
	return classify(err)
}

// probeCopyFileRange uses two invalid descriptors, so a kernel that has the
// call answers EBADF and one that does not answers ENOSYS. Nothing is copied.
func probeCopyFileRange() Support {
	_, err := unix.CopyFileRange(-1, nil, -1, nil, 0, 0)
	return classify(err)
}

// probeCloseRange asks for a range that cannot be valid, first past last, so a
// kernel that has the call answers EINVAL. Nothing is closed.
func probeCloseRange() Support {
	err := unix.CloseRange(1, 0, 0)
	return classify(err)
}

func probeInotify() Support {
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	if err == nil {
		closeQuiet(fd)
	}
	return classify(err)
}

// probeSeccomp passes a null program pointer, so a kernel that supports the
// operation and the flag answers EFAULT before installing anything.
func probeSeccomp() Support {
	_, _, errno := unix.Syscall(unix.SYS_SECCOMP,
		uintptr(unix.SECCOMP_SET_MODE_FILTER), uintptr(unix.SECCOMP_FILTER_FLAG_TSYNC), 0)
	switch errno {
	case 0, unix.EFAULT:
		return SupportPresent
	}
	return classify(errno)
}

// closeQuiet is for a descriptor a probe opened only to prove it could.
func closeQuiet(fd int) {
	if err := unix.Close(fd); err != nil {
		slog.Warn("closing a probe descriptor failed", slog.Any("error", err))
	}
}
