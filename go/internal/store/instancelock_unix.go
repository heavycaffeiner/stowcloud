//go:build !windows

package store

import (
	"os"

	"golang.org/x/sys/unix"
)

// lockExclusive takes a whole-file advisory lock, or fails immediately.
//
// The lock belongs to the open file description rather than to the process, so
// a second call in this process opens a second descriptor and contends with the
// first exactly as another process would. That is what makes two concurrent
// importers testable without two processes.
//
// SyscallConn rather than Fd: the descriptor stays in the runtime's view for the
// duration of the call, so nothing can close it underneath the syscall.
func lockExclusive(f *os.File) error {
	rc, err := f.SyscallConn()
	if err != nil {
		return err
	}
	var lockErr error
	if cerr := rc.Control(func(fd uintptr) {
		lockErr = unix.Flock(int(fd), unix.LOCK_EX|unix.LOCK_NB)
	}); cerr != nil {
		return cerr
	}
	return lockErr
}
