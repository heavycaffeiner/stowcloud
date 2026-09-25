package instance

import (
	"os"

	"golang.org/x/sys/windows"
)

// lockExclusive claims the whole file, returning at once if someone holds it.
//
// Present so this package builds and its tests run on a development machine;
// the sibling file is what a deployment uses. Both enforce genuine exclusion,
// which is the property the tests are written against.
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
