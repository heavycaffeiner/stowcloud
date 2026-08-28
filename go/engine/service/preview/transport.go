//go:build linux

package preview

import (
	"fmt"
	"os"
	"runtime"

	"golang.org/x/sys/unix"
)

// The seqpacket control socket used to drive the jailed worker.
//
// It has left the filesystem package, since vfs marks the filesystem boundary
// and a socketpair codec never belonged there.
//
// It lives alongside the codec rather than under worker/ because both halves
// require it: the parent invokes SocketPair and SendMessage from the pool, while
// the worker invokes RecvMessage and SendMessage from its loop. Placing it under
// worker/ would have the parent import the worker while the worker imports this
// package for the codec and the decoders, a cycle the compiler rejects.
//
// No raw descriptor may outlive a keepalive. (*os.File).Fd removes the
// descriptor from the runtime's view for the duration of the call, leaving a
// finalizer free to close it beneath the syscall, so every number obtained here
// is retained across the call that uses it.
//
// Message boundaries matter as much as descriptors. SOCK_SEQPACKET preserves
// them: over a stream a short read would resemble a valid short message, exactly
// the ambiguity a fixed-layout wire codec exists to eliminate.

// SocketPair creates a connected seqpacket pair.
//
// Both ends carry close-on-exec, preventing a descriptor from leaking into an
// unrelated child. The caller clears it on whichever end it passes to its own
// child.
func SocketPair() (a, b *os.File, err error) {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("preview: creating a socket pair: %w", err)
	}
	return os.NewFile(uintptr(fds[0]), "seqpacket"), os.NewFile(uintptr(fds[1]), "seqpacket"), nil
}

// SendMessage transmits a message over sock, optionally attaching descriptors.
//
// Files travel in an SCM_RIGHTS control message, and each is kept alive for the
// duration of the call.
func SendMessage(sock *os.File, msg []byte, pass ...*os.File) error {
	// The rights are assembled within the keepalive, so every attached file is
	// retained from the moment its number is read until the syscall returns.
	err := withFdErr(sock, func(fd int) error {
		var rights []byte
		if len(pass) > 0 {
			fds := make([]int, 0, len(pass))
			for _, f := range pass {
				n, ferr := rawFd(f)
				if ferr != nil {
					return ferr
				}
				fds = append(fds, n)
			}
			rights = unix.UnixRights(fds...)
		}
		return unix.Sendmsg(fd, msg, rights, nil, 0)
	})
	keepAliveAll(pass)
	if err != nil {
		return fmt.Errorf("preview: sending a message: %w", err)
	}
	return nil
}

// RecvMessage receives a message together with any descriptors attached to it.
//
// Closing the returned files is the caller's responsibility. A message carrying
// none yields an empty slice rather than nil, sparing a caller that counts them
// from telling the two apart.
func RecvMessage(sock *os.File, buf []byte, maxFiles int) (n int, files []*os.File, err error) {
	oob := make([]byte, unix.CmsgSpace(maxFiles*4))
	var oobn int

	rerr := withFdErr(sock, func(fd int) error {
		var e error
		n, oobn, _, _, e = unix.Recvmsg(fd, buf, oob, 0)
		return e
	})
	if rerr != nil {
		return 0, nil, fmt.Errorf("preview: receiving a message: %w", rerr)
	}

	files = []*os.File{}
	if oobn == 0 {
		return n, files, nil
	}

	msgs, perr := unix.ParseSocketControlMessage(oob[:oobn])
	if perr != nil {
		return 0, nil, fmt.Errorf("preview: a malformed control message: %w", perr)
	}
	for _, m := range msgs {
		fds, uerr := unix.ParseUnixRights(&m)
		if uerr != nil {
			CloseFiles(files)
			return 0, nil, fmt.Errorf("preview: a malformed rights message: %w", uerr)
		}
		for i, fd := range fds {
			files = append(files, os.NewFile(uintptr(fd), fmt.Sprintf("passed-%d", i)))
		}
	}
	return n, files, nil
}

// withFdErr runs fn with sock's raw descriptor, held live across the call.
func withFdErr(f *os.File, fn func(fd int) error) error {
	n, err := rawFd(f)
	if err != nil {
		return err
	}
	err = fn(n)
	runtime.KeepAlive(f)
	return err
}

// rawFd reads a descriptor number, refusing one the runtime has already closed.
func rawFd(f *os.File) (int, error) {
	if f == nil {
		return 0, fmt.Errorf("preview: a nil file has no descriptor")
	}
	fd := int(f.Fd())
	if fd < 0 {
		return 0, fmt.Errorf("preview: the file is already closed")
	}
	return fd, nil
}

func keepAliveAll(files []*os.File) {
	for _, f := range files {
		runtime.KeepAlive(f)
	}
}

// CloseFiles closes a set of received descriptors, for a caller abandoning
// them after a parse failure.
func CloseFiles(files []*os.File) {
	for _, f := range files {
		if f == nil {
			continue
		}
		//nolint:errcheck // a descriptor being discarded after a parse failure has nowhere to report.
		_ = f.Close()
	}
}
