//go:build !windows

package instance

import (
	"os"

	"golang.org/x/sys/unix"
)

// lockExclusive claims the whole file, returning at once if someone holds it.
//
// Ownership attaches to the open file description and not to the process. Two
// Take calls from one process therefore open two descriptions and compete just
// as separate processes would, which is why the exclusion can be exercised
// without spawning anything.
//
// The descriptor is reached through SyscallConn so the runtime keeps it alive
// across the call. Passing a bare Fd would permit a close to land underneath
// the syscall.
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
