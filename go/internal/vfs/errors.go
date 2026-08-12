package vfs

import "errors"

// The typed errors this package answers with. None of them chooses an HTTP
// status: that is one mapping function in the HTTP layer, where the caller's
// grants are known and the rule that an unlistable path is 404 everywhere can
// be applied.
var (
	ErrNotFound      = errors.New("not found")
	ErrDenied        = errors.New("permission denied")
	ErrExists        = errors.New("already exists")
	ErrNotEmpty      = errors.New("directory not empty")
	ErrNoSpace       = errors.New("no space left on device")
	ErrCrossDevice   = errors.New("cross-device operation")
	ErrNotADirectory = errors.New("not a directory")

	// ErrSymlinkDenied is distinct from ErrNotFound on purpose. It is how a
	// share with the deny policy reports that a symlink was refused, which is a
	// different fact from the target not existing, and the layer above decides
	// which one a client is allowed to learn.
	ErrSymlinkDenied = errors.New("symlink traversal denied")
)
