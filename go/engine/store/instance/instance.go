// Package instance is the advisory lock a process holds over a data
// directory for as long as it owns it.
//
// The file's contents are irrelevant and never read. Ownership lives entirely
// in the kernel's lock on the open descriptor, and the kernel releases that on
// exit regardless of how the exit happened. A leftover file from a killed
// process therefore conveys nothing and stops nobody.
package instance

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// LockFile is the name taken under the data directory.
const LockFile = ".stowcloud-instance.lock"

// ErrInUse is another process holding the lock.
//
// Which process is deliberately not guessed at. It may be a server or a repair
// run, and the answer is the same either way: stop it first.
var ErrInUse = errors.New("data directory in use")

// Lock is a held lock. Release drops it.
type Lock struct{ f *os.File }

// Take acquires the directory's lock without waiting.
//
// Without waiting, because what this protects against is a running server
// rather than a moment of contention. The state spans several WAL databases
// with no snapshot across them, so a second process reading them while a server
// writes would combine a user from one instant with the grants from another.
// Waiting for a server to exit is not something a command should do silently.
func Take(dir string) (*Lock, error) {
	path := filepath.Join(dir, LockFile)
	//nolint:gosec // G304 flags the variable: the filename is this package's constant and only the directory is the caller's.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", LockFile, err)
	}
	if lerr := lockExclusive(f); lerr != nil {
		return nil, errors.Join(fmt.Errorf("%w: %s: %w", ErrInUse, dir, lerr), f.Close())
	}
	return &Lock{f: f}, nil
}

// Release drops the lock by closing the descriptor it is held on.
//
// The file stays where it is. Removing it would let a second process create and
// lock a fresh one while a third still holds this one, which is two owners of
// one directory each believing they are alone.
func (l *Lock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	f := l.f
	l.f = nil
	return f.Close()
}
