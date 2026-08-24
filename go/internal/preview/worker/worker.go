//go:build linux

// Package worker is the jailed half of preview generation.
//
// It is a separate package rather than a file in internal/preview so that the
// parent cannot accidentally call into it: the two halves talk over a socket
// and share only the wire codec.
//
// The worker is never told a path. openat is not on the seccomp allow-list and
// the Landlock domain grants nothing, so a path-traversal bug in a decoder has
// nothing to traverse. Input and output arrive as descriptors beside each job.
package worker

import (
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/internal/jail"
	"github.com/heavycaffeiner/stowcloud/go/internal/limits"
	"github.com/heavycaffeiner/stowcloud/go/internal/preview"
	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
)

// ControlFD is the descriptor the parent passes the control socket on.
//
// Three, because the runtime keeps zero, one and two. It is fixed rather than
// passed as an argument because the worker parses no arguments: an argv is a
// place to put a path, and this process must have no way to name a file.
const ControlFD = 3

// MaxInputBytes bounds one source image.
//
// The parent already refuses larger files, so this is the worker declining to
// trust the parent about a length: the two are separate processes and one of
// them may be compromised.
const MaxInputBytes = 256 << 20

// Run is the worker's whole life: apply the jail, then serve jobs until the
// socket closes.
//
// The jail goes on before the first job and before anything is read, because a
// decoder bug in the first message is exactly the case it exists for.
func Run(policy jail.Policy) (jail.Status, error) {
	// Held at one so the runtime does not start a thread after the filter is
	// installed, which is what keeps clone off the allow-list. It does not mean
	// one OS thread: the runtime keeps several for its own work whatever this
	// says. What matters is that they exist before the filter, not that they do
	// not exist.
	//
	// A worker that cannot clone cannot fork either, which is the property the
	// list is for.
	//
	// It costs nothing here: a worker decodes one image at a time by design,
	// because the pool is what provides the parallelism.
	runtime.GOMAXPROCS(1)

	st, err := jail.Apply(policy, workerSpec())
	if err != nil {
		return st, err
	}
	// The allow-list filter is the worker's own, and it kills rather than
	// returning an error: a decoder reaching a syscall it does not need is
	// already executing something nobody wrote.
	//
	// SC_PREVIEW_TRAP swaps the kill for SIGSYS, which the runtime reports with
	// a stack naming the call. A kill prints nothing at all, so a filter
	// missing an entry looks the same as a decoder that crashed, and finding
	// which entry meant reading si_syscall out of a core: not available on a
	// machine that discards cores, which is most CI.
	//
	// Diagnostic only. A trap the process survives is not a sandbox, so this is
	// never set in a deployment; it is read from the environment because the
	// worker parses no arguments by design.
	filter := jail.FilterWorker
	if os.Getenv("SC_PREVIEW_TRAP") == "1" {
		filter = jail.FilterWorkerTrap
	}
	if serr := jail.InstallSeccomp(filter); serr != nil {
		return st, serr
	}

	control := os.NewFile(ControlFD, "control")
	if control == nil {
		return st, errors.New("preview worker: no control socket on the expected descriptor")
	}
	defer func() {
		if cerr := control.Close(); cerr != nil {
			// Nothing to report it to: the parent is the only peer and the
			// socket is what just failed.
			_ = cerr //nolint:errcheck // the socket is the only channel and it is gone.
		}
	}()

	_, err = Serve(control, "")
	return st, err
}

// Serve runs the job loop on an already-open control socket.
//
// It is exported so the pool's tests can drive a real worker process without
// applying the jail, which is proved separately against a real kernel. mode is
// a test hook and is empty in the product: the two failure modes it offers,
// dying mid-job and never answering, are what the parent's reap and deadline
// paths exist to handle and cannot otherwise be reached on demand.
func Serve(control *os.File, mode string) (int, error) {
	if control == nil {
		return 0, errors.New("preview worker: no control socket")
	}
	return 0, serveLoop(control, mode)
}

// workerSpec is a Landlock domain granting nothing.
//
// A spec with no grants denies the whole filesystem, which is the point: the
// worker holds descriptors the parent opened and has no way to open another.
// EXECUTE is left unhandled so the domain survives the re-exec that makes it
// process-wide; seccomp removes execve afterwards, which is the harder stop.
func workerSpec() jail.Spec {
	return jail.Spec{ExceptExec: true}
}

// serveLoop reads jobs until the socket closes.
func serveLoop(control *os.File, mode string) error {
	for {
		req, in, out, err := recvJob(control)
		if err != nil {
			if errors.Is(err, io.EOF) {
				// The parent hung up, which is how a worker retires.
				return nil
			}
			// A message that did not parse exactly is fatal by contract: a
			// partially valid message from this process's peer is not a thing
			// to recover from, and neither is one this process sent.
			return err
		}

		switch mode {
		case modeDie:
			// What a seccomp kill, an OOM and a segfault all look like from
			// the parent: the process is simply gone mid-job.
			closeBoth(in, out)
			os.Exit(1)
		case modeHang:
			// Never answers, so the parent's deadline is what ends it. A sleep
			// rather than a bare select, which the runtime turns into a
			// deadlock panic when it is the only goroutine.
			closeBoth(in, out)
			time.Sleep(time.Hour)
		}

		resp := handle(req, in, out)
		closeBoth(in, out)

		if werr := send(control, resp.Encode()); werr != nil {
			return werr
		}
	}
}

// The test hooks. Named constants rather than bare strings so a typo in a test
// is a compile error there rather than a silently normal worker.
const (
	modeDie  = "die"
	modeHang = "hang"
)

// handle runs one job. It never returns an error: a failure is a status on the
// response, because the worker's job is to answer rather than to die.
func handle(req preview.Request, in, out *os.File) preview.Response {
	if req.Kind == preview.JobProbe {
		// Attempted from inside the finished jail. A probe that is killed
		// never answers at all: the process is gone and the parent sees the
		// socket close, which is itself a pass.
		outcome, detail := RunProbe(Probe(req.Preset))
		return preview.Response{
			Status: preview.StatusOK,
			Width:  uint16(outcome),
			Err:    detail,
		}
	}
	if req.Kind == preview.JobVideo {
		// The honest answer, kept over the wire so a client gets a refusal it
		// can act on rather than a generic failure.
		return preview.Response{
			Status: preview.StatusNotImplemented,
			Err:    "video preview generation is not implemented in this build",
		}
	}
	if in == nil || out == nil {
		return preview.Response{
			Status: preview.StatusInternal,
			Err:    "a job arrived without both descriptors",
		}
	}

	data, err := readAll(in)
	if err != nil {
		return preview.Response{Status: preview.StatusInternal, Err: err.Error()}
	}

	lim := preview.DefaultDecodeLimits()
	if req.MaxPixels > 0 && uint64(req.MaxPixels) < lim.MaxPixels {
		// The parent may ask for less than the compiled-in bound but never for
		// more: a request that could raise a limit is a request that can
		// remove one.
		lim.MaxPixels = uint64(req.MaxPixels)
	}

	img, err := preview.DecodeBounded(data, lim)
	if err != nil {
		return decodeFailure(err)
	}

	if req.Flags&preview.FlagStripEXIF != 0 {
		// Applied to the pixels and then gone: the encoder writes no metadata,
		// so nothing carries across from here.
		img = preview.ReadOrientation(data).Apply(img)
	}

	thumb, err := preview.Thumbnail(img, req.Preset, lim)
	if err != nil {
		return decodeFailure(err)
	}

	counted := &countingWriter{w: out}
	if err := preview.EncodePNG(counted, thumb); err != nil {
		return preview.Response{Status: preview.StatusInternal, Err: err.Error()}
	}

	b := thumb.Bounds()
	return preview.Response{
		Status: preview.StatusOK,
		Width:  clampU16(b.Dx()),
		Height: clampU16(b.Dy()),
		Bytes:  clampU32(counted.n),
	}
}

// decodeFailure maps a decode error onto a wire status.
func decodeFailure(err error) preview.Response {
	switch {
	case errors.Is(err, preview.ErrTooLarge):
		// The graceful limit fired, which is the whole reason it exists: the
		// worker survives to say so instead of being killed by RLIMIT_AS.
		return preview.Response{Status: preview.StatusTooLarge, Err: err.Error()}
	case errors.Is(err, preview.ErrUnsupported):
		return preview.Response{Status: preview.StatusUnsupported, Err: err.Error()}
	default:
		return preview.Response{Status: preview.StatusDecodeFailed, Err: err.Error()}
	}
}

// readAll reads the input descriptor under a ceiling.
func readAll(f *os.File) ([]byte, error) {
	r := io.LimitReader(f, MaxInputBytes+1)
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("reading the source: %w", err)
	}
	if len(data) > MaxInputBytes {
		return nil, fmt.Errorf("the source exceeds %d bytes", MaxInputBytes)
	}
	return data, nil
}

// countingWriter records how much reached the output descriptor, which the
// parent needs to know how much of the file is the thumbnail.
type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// recvJob reads one request and its two descriptors.
//
// Exactly two arrive with each job, input and output. A different count is the
// same fatal case as a message that did not parse: it means the peer is not
// speaking this protocol.
func recvJob(control *os.File) (preview.Request, *os.File, *os.File, error) {
	buf := make([]byte, limits.WorkerWireMessage)
	// Two, because exactly two descriptors arrive with each job. Asking for a
	// bound larger than the protocol allows would let a peer hand over more.
	n, files, err := vfs.RecvMessage(control, buf, 2)
	if err != nil {
		return preview.Request{}, nil, nil, fmt.Errorf("receiving a job: %w", err)
	}
	if n == 0 {
		closeAll(files)
		return preview.Request{}, nil, nil, io.EOF
	}
	if len(files) != 2 {
		closeAll(files)
		return preview.Request{}, nil, nil, fmt.Errorf(
			"%w: a job arrived with %d descriptors, want 2", preview.ErrProtocol, len(files))
	}

	req, derr := preview.DecodeRequest(buf[:n])
	if derr != nil {
		closeAll(files)
		return preview.Request{}, nil, nil, derr
	}
	return req, files[0], files[1], nil
}

// send writes one response.
func send(control *os.File, msg []byte) error {
	return vfs.SendMessage(control, msg)
}

func closeBoth(in, out *os.File) { closeAll([]*os.File{in, out}) }

// closeAll releases descriptors the worker is finished with. A leak here is a
// worker that runs out of them and starts failing every job.
func closeAll(files []*os.File) {
	for _, f := range files {
		if f == nil {
			continue
		}
		_ = f.Close() //nolint:errcheck // a close on a job descriptor has no reader for its error.
	}
}

func clampU16(v int) uint16 {
	if v < 0 {
		return 0
	}
	if v > 0xffff {
		return 0xffff
	}
	return uint16(v)
}

func clampU32(v int64) uint32 {
	if v < 0 {
		return 0
	}
	if v > 0xffffffff {
		return 0xffffffff
	}
	return uint32(v)
}
