//go:build linux

package vfs

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// The seqpacket control socket the preview worker is driven over.
//
// It lives here for the same reason every other descriptor operation does: a
// raw descriptor must not leave this package, because (*os.File).Fd takes it
// out of the runtime's view for the duration of the call and a finalizer is
// then free to close it underneath the syscall. The two keepalive helpers are
// the only places that hold one, and these are their socket-shaped callers.
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
		return nil, nil, fmt.Errorf("vfs: creating a socket pair: %w", err)
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
		return fmt.Errorf("vfs: sending a message: %w", err)
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
		return 0, nil, fmt.Errorf("vfs: receiving a message: %w", rerr)
	}

	files = []*os.File{}
	if oobn == 0 {
		return n, files, nil
	}

	msgs, perr := unix.ParseSocketControlMessage(oob[:oobn])
	if perr != nil {
		return 0, nil, fmt.Errorf("vfs: a malformed control message: %w", perr)
	}
	for _, m := range msgs {
		fds, uerr := unix.ParseUnixRights(&m)
		if uerr != nil {
			closeFiles(files)
			return 0, nil, fmt.Errorf("vfs: a malformed rights message: %w", uerr)
		}
		for i, fd := range fds {
			files = append(files, os.NewFile(uintptr(fd), fmt.Sprintf("passed-%d", i)))
		}
	}
	return n, files, nil
}

func keepAliveAll(files []*os.File) {
	for _, f := range files {
		keepAlive(f)
	}
}

func closeFiles(files []*os.File) {
	for _, f := range files {
		if f == nil {
			continue
		}
		_ = f.Close() //nolint:errcheck // a descriptor being discarded after a parse failure.
	}
}
