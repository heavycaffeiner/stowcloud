package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// InstanceLockFile is the advisory lock every process that owns a data
// directory holds for its lifetime.
//
// Two hold it today: the tree this replaces, which takes it as its serve
// command starts, and the one-shot import, which takes it so that "stop the
// old server first" is a refusal rather than a sentence in a runbook. The
// serve command on this side takes it too when there is one to take it.
//
// It is not a PID file. Nothing is written into it and nothing reads it. The
// only state is the kernel lock on the open descriptor, which the kernel drops
// when the process exits however it exits, so a file left behind by a crash
// says nothing and blocks nothing.
const InstanceLockFile = ".stowcloud-instance.lock"

// ErrDataDirInUse is another process holding the lock. Which process is
// deliberately not guessed at: it may be a server or another importer, and
// either way the answer is the same.
var ErrDataDirInUse = errors.New("data directory in use")

// InstanceLock is a held lock. Release drops it.
type InstanceLock struct{ f *os.File }

// LockInstance takes the directory's lock without waiting.
//
// Without waiting, because the thing it is protecting against is a running
// server rather than a moment of contention: the Rust state spans several WAL
// databases with no snapshot across them, so an import that read them while a
// server was writing would combine a user from one instant with the grants from
// another. Waiting for a server to exit is not something a command should do
// silently.
func LockInstance(dir string) (*InstanceLock, error) {
	path := filepath.Join(dir, InstanceLockFile)
	//nolint:gosec // the name is this package's own constant under the directory it was handed.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", InstanceLockFile, err)
	}
	if err := lockExclusive(f); err != nil {
		return nil, errors.Join(fmt.Errorf("%w: %s: %w", ErrDataDirInUse, dir, err), f.Close())
	}
	return &InstanceLock{f: f}, nil
}

// Release drops the lock by closing the descriptor it is held on. The file
// stays where it is: removing it would let a second process create and lock a
// new one while a third still holds this.
func (l *InstanceLock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	f := l.f
	l.f = nil
	return f.Close()
}
