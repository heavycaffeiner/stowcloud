package instance

import (
	"os"

	"golang.org/x/sys/windows"
)

// lockExclusive takes a whole-file lock, or fails immediately.
//
// The development host's copy. It exists so this package compiles and its
// tests run where they are written; the file beside it is what ships. The
// exclusion is real on both, which is what those tests are about.
func lockExclusive(f *os.File) error {
	rc, err := f.SyscallConn()
	if err != nil {
		return err
	}
	var lockErr error
	if cerr := rc.Control(func(fd uintptr) {
		var overlapped windows.Overlapped
		lockErr = windows.LockFileEx(windows.Handle(fd),
			windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
			0, 1, 0, &overlapped)
	}); cerr != nil {
		return cerr
	}
	return lockErr
}
