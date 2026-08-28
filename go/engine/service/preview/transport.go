//go:build linux

package preview

import (
	"fmt"
	"os"
	"runtime"

	"golang.org/x/sys/unix"
)

// The seqpacket control socket the jailed worker is driven over.
//
// It has moved out of the filesystem package: vfs is the filesystem boundary,
// and a socketpair codec never belonged in it.
//
// It sits beside the codec rather than inside worker/ because both halves need
// it: the parent calls SocketPair and SendMessage from the pool, and the worker
// calls RecvMessage and SendMessage from its loop. Putting it under worker/
// would make the parent import the worker while the worker imports this
// package for the codec and the decoders, which is a cycle the compiler
// refuses.
//
// A raw descriptor must not escape a keepalive. (*os.File).Fd takes the
// descriptor out of the runtime's view for the duration of the call, and a
// finalizer is then free to close it underneath the syscall, so every number
// read here is held across the call that uses it.
//
// The message boundary matters as much as the descriptor. SOCK_SEQPACKET makes
// a message a message: over a stream, a short read would look like a valid
// short message, which is exactly the ambiguity a fixed-layout wire codec
// exists to remove.

// SocketPair returns a connected seqpacket pair.
//
// Both ends are close-on-exec, so a descriptor does not leak into an unrelated
// child. The caller clears that on the end it hands to its own child.
func SocketPair() (a, b *os.File, err error) {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("preview: creating a socket pair: %w", err)
	}
	return os.NewFile(uintptr(fds[0]), "seqpacket"), os.NewFile(uintptr(fds[1]), "seqpacket"), nil
}

// SendMessage writes one message on sock, optionally passing descriptors.
//
// The files are passed as an SCM_RIGHTS control message and every one of them
// is kept alive across the call.
func SendMessage(sock *os.File, msg []byte, pass ...*os.File) error {
	// The rights are built inside the keepalive, so every passed file is held
	// from the moment its number is read until the syscall has returned.
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

// RecvMessage reads one message and any descriptors passed with it.
//
// The returned files are the caller's to close. A message carrying none
// returns an empty slice rather than nil, so a caller counting them does not
// have to distinguish the two.
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
