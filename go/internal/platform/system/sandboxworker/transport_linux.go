//go:build linux

// Package sandboxworker contains the neutral transport primitives shared by a
// process that hands work to a confined worker and the worker receiving it.
//
// The transport deliberately carries only bytes and file descriptors. Product
// wire formats, decoder policy, and worker lifecycle remain above this
// boundary.
package sandboxworker

import (
	"fmt"
	"os"
	"runtime"

	"golang.org/x/sys/unix"
)

const (
	// MaxMessageSize is the largest payload accepted by this transport. A
	// SOCK_SEQPACKET peer cannot turn a larger payload into a second read, so
	// refusing it here keeps the boundary explicit for every caller.
	MaxMessageSize = 8 << 10
	// MaxFiles is the largest number of descriptors one message may carry.
	// Callers should pass the narrower count their protocol requires to
	// RecvMessage.
	MaxFiles = 16
)

// SocketPair creates a connected close-on-exec SOCK_SEQPACKET pair.
//
// The returned files own their descriptors. A caller handing one end to a
// child must clear close-on-exec in the child setup where appropriate.
func SocketPair() (a, b *os.File, err error) {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("sandboxworker: creating a socket pair: %w", err)
	}
	return os.NewFile(uintptr(fds[0]), "sandboxworker-seqpacket"),
		os.NewFile(uintptr(fds[1]), "sandboxworker-seqpacket"), nil
}

// SendMessage transmits one neutral byte message over sock, optionally
// transferring ownership of descriptors to the receiving process. The sender
// retains ownership of pass; only the receiver receives new descriptor
// objects.
func SendMessage(sock *os.File, msg []byte, pass ...*os.File) error {
	if len(msg) > MaxMessageSize {
		return fmt.Errorf("sandboxworker: message is %d bytes, past the %d-byte bound", len(msg), MaxMessageSize)
	}
	if len(pass) > MaxFiles {
		return fmt.Errorf("sandboxworker: message carries %d descriptors, past the %d-file bound", len(pass), MaxFiles)
	}

	// Build SCM_RIGHTS while every passed file is kept alive through Sendmsg.
	err := withFdErr(sock, func(fd int) error {
		if len(pass) == 0 {
			return unix.Sendmsg(fd, msg, nil, nil, 0)
		}
		return withFilesErr(pass, func(fds []int) error {
			return unix.Sendmsg(fd, msg, unix.UnixRights(fds...), nil, 0)
		})
	})
	if err != nil {
		return fmt.Errorf("sandboxworker: sending a message: %w", err)
	}
	return nil
}

// RecvMessage receives one byte message and any descriptors attached to it.
//
// The returned files are owned by the caller and must be closed, normally with
// CloseFiles. A message carrying no descriptors returns a non-nil empty slice.
// maxFiles bounds the ancillary buffer and must not exceed MaxFiles. A
// truncated payload or ancillary data is rejected rather than treated as a
// valid shorter message.
func RecvMessage(sock *os.File, buf []byte, maxFiles int) (n int, files []*os.File, err error) {
	if len(buf) > MaxMessageSize {
		return 0, nil, fmt.Errorf("sandboxworker: receive buffer is %d bytes, past the %d-byte bound", len(buf), MaxMessageSize)
	}
	if maxFiles < 0 || maxFiles > MaxFiles {
		return 0, nil, fmt.Errorf("sandboxworker: receive descriptor bound %d is outside 0..%d", maxFiles, MaxFiles)
	}

	oob := make([]byte, unix.CmsgSpace(maxFiles*unix.SizeofInt))
	var oobn, flags int
	rerr := withFdErr(sock, func(fd int) error {
		var e error
		n, oobn, flags, _, e = unix.Recvmsg(fd, buf, oob, 0)
		return e
	})
	if rerr != nil {
		return 0, nil, fmt.Errorf("sandboxworker: receiving a message: %w", rerr)
	}
	if flags&(unix.MSG_TRUNC|unix.MSG_CTRUNC) != 0 {
		return 0, nil, fmt.Errorf("sandboxworker: received a truncated message")
	}

	files = []*os.File{}
	if oobn == 0 {
		return n, files, nil
	}

	msgs, perr := unix.ParseSocketControlMessage(oob[:oobn])
	if perr != nil {
		return 0, nil, fmt.Errorf("sandboxworker: a malformed control message: %w", perr)
	}
	for _, m := range msgs {
		fds, uerr := unix.ParseUnixRights(&m)
		if uerr != nil {
			CloseFiles(files)
			return 0, nil, fmt.Errorf("sandboxworker: a malformed rights message: %w", uerr)
		}
		for i, fd := range fds {
			files = append(files, os.NewFile(uintptr(fd), fmt.Sprintf("sandboxworker-passed-%d", i)))
		}
	}
	if len(files) > maxFiles {
		CloseFiles(files)
		return 0, nil, fmt.Errorf("sandboxworker: received %d descriptors, past the %d-file bound", len(files), maxFiles)
	}
	return n, files, nil
}

// withFdErr runs fn against f's descriptor while the runtime keeps f alive.
// SyscallConn also prevents Close from racing the callback, unlike Fd.
func withFdErr(f *os.File, fn func(fd int) error) error {
	if f == nil {
		return fmt.Errorf("sandboxworker: a nil file has no descriptor")
	}
	rc, err := f.SyscallConn()
	if err != nil {
		return err
	}
	var callErr error
	if err := rc.Control(func(fd uintptr) { callErr = fn(int(fd)) }); err != nil {
		return err
	}
	runtime.KeepAlive(f)
	return callErr
}

// withFilesErr collects descriptors and keeps every owner alive through fn.
// It is the sole helper for SCM_RIGHTS descriptors passed by this package.
func withFilesErr(files []*os.File, fn func(fds []int) error) error {
	fds := make([]int, len(files))
	for i, f := range files {
		if err := withFdErr(f, func(fd int) error {
			fds[i] = fd
			return nil
		}); err != nil {
			return err
		}
	}
	err := fn(fds)
	keepAliveAll(files)
	return err
}

func keepAliveAll(files []*os.File) {
	for _, f := range files {
		runtime.KeepAlive(f)
	}
}

// CloseFiles closes received descriptors, including nil entries, and ignores
// close errors because this helper is used while abandoning a failed message.
func CloseFiles(files []*os.File) {
	for _, f := range files {
		if f == nil {
			continue
		}
		//nolint:errcheck // discarded descriptors have no useful error destination.
		_ = f.Close()
	}
}
