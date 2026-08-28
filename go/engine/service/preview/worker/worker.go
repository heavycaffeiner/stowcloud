//go:build linux

package worker

import (
	"errors"
	"fmt"
	"image"
	"io"
	"os"
	"runtime"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/jail"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/limits"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/preview"
)

// The worker is never told a path. openat is not on the seccomp allow list and
// the Landlock domain grants nothing, so a path-traversal bug in a decoder has
// nothing to traverse. Input and output arrive as descriptors beside each job.

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
// them may be compromised. Defence in depth, deliberately duplicated.
const MaxInputBytes = 256 << 20

// jobDescriptors is how many descriptors accompany one job: the input and the
// output, and never a third.
const jobDescriptors = 2

// trapEnv swaps the worker filter's kill for a SIGSYS trap. Diagnostic only,
// read from the environment because the worker parses no arguments.
const trapEnv = "SC_PREVIEW_TRAP"

// Run is the worker's whole life: apply the jail, then serve jobs until the
// socket closes.
//
// The order is the design, and the two calls that were dead in the previous
// build are wired into it:
//
//  1. GOMAXPROCS(1), before anything starts a thread.
//  2. The Landlock domain and its re-exec.
//  3. SealDescriptors, closing everything the worker inherited past its
//     control socket: listening sockets, share roots, database handles.
//     RLIMIT_NOFILE bounds what it may newly open and does nothing about these.
//  4. ApplyLimits, so the in-process pixel ceiling has the kernel backstop the
//     comments have always claimed. A decoder exploit is exactly the case where
//     an in-process bound stops counting.
//  5. The seccomp filter, last, because it is irreversible.
//
// Only then is the first message read. A decoder bug in the first message is
// exactly the case the jail exists for.
func Run(policy jail.Policy) (jail.Status, error) {
	// Held at one so the runtime does not start a thread after the filter is
	// installed, which is what keeps clone off the allow list. It does not mean
	// one OS thread: the runtime keeps several for its own work whatever this
	// says. What matters is that they exist before the filter, not that they do
	// not exist.
	//
	// It costs nothing here: a worker decodes one image at a time by design,
	// because the pool is what provides the parallelism.
	runtime.GOMAXPROCS(1)

	st, err := jail.Apply(policy, workerSpec())
	if err != nil {
		return st, err
	}

	if serr := jail.SealDescriptors(); serr != nil {
		return st, fmt.Errorf("preview worker: sealing inherited descriptors: %w", serr)
	}
	if lerr := jail.ApplyLimits(jail.DefaultLimits()); lerr != nil {
		return st, fmt.Errorf("preview worker: applying resource limits: %w", lerr)
	}
	// Read while the call that reads it is still permitted, so the proof can
	// report what the kernel gave without widening the filter.
	captureLimits()

	// The allow-list filter is the worker's own, and it kills rather than
	// returning an error: a decoder reaching a syscall it does not need is
	// already executing something nobody wrote.
	//
	// The trap variant swaps the kill for SIGSYS, which the runtime reports
	// with a stack naming the call. A kill prints nothing at all, so a filter
	// missing an entry looks the same as a decoder that crashed. A trap the
	// process survives is not a sandbox, so this is never set in a deployment.
	filter := jail.FilterWorker
	if os.Getenv(trapEnv) == "1" {
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
		//nolint:errcheck // the parent is the only peer and the socket is what just closed.
		_ = control.Close()
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
		case preview.ModeDie:
			// What a seccomp kill, an OOM and a segfault all look like from
			// the parent: the process is simply gone mid-job.
			preview.CloseFiles([]*os.File{in, out})
			os.Exit(1)
		case preview.ModeHang:
			// Never answers, so the parent's deadline is what ends it. A sleep
			// rather than a bare select, which the runtime turns into a
			// deadlock panic when it is the only goroutine.
			preview.CloseFiles([]*os.File{in, out})
			time.Sleep(time.Hour)
		}

		resp := handle(req, in, out)
		preview.CloseFiles([]*os.File{in, out})

		if werr := preview.SendMessage(control, resp.Encode()); werr != nil {
			return werr
		}
	}
}

// handle runs one job. It never returns an error: a failure is a status on the
// response, because the worker's job is to answer rather than to die.
func handle(req preview.Request, in, out *os.File) preview.Response {
	switch req.Kind {
	case preview.JobProbe:
		// Attempted from inside the finished jail. A probe that is killed
		// never answers at all: the process is gone and the parent sees the
		// socket close, which is itself a pass.
		outcome, detail := RunProbe(Probe(req.Preset))
		return preview.Response{
			Status: preview.StatusOK,
			Width:  uint16(outcome),
			Err:    detail,
		}
	case preview.JobVideo:
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
	return decodeJob(req, in, out)
}

// decodeJob is the image path: read under the worker's own ceiling, decode
// under the clamped limits, scale, and encode to the output descriptor.
func decodeJob(req preview.Request, in, out *os.File) preview.Response {
	data, err := readAll(in)
	if err != nil {
		return preview.Response{Status: preview.StatusInternal, Err: err.Error()}
	}

	lim := clampLimits(req, preview.DefaultDecodeLimits())

	img, err := preview.DecodeBounded(data, lim)
	if err != nil {
		return decodeFailure(err)
	}

	if req.Flags&preview.FlagStripEXIF != 0 {
		// Applied to the pixels and then gone: the encoder writes no metadata,
		// so nothing carries across from here.
		img = preview.ReadOrientation(data).Apply(img)
	}

	thumb, err := scaleFor(req, img, lim)
	if err != nil {
		return decodeFailure(err)
	}

	counted := &countingWriter{w: out}
	if eerr := preview.EncodePNG(counted, thumb); eerr != nil {
		return preview.Response{Status: preview.StatusInternal, Err: eerr.Error()}
	}

	b := thumb.Bounds()
	return preview.Response{
		Status: preview.StatusOK,
		Width:  clampU16(b.Dx()),
		Height: clampU16(b.Dy()),
		Bytes:  clampU32(counted.n),
	}
}

// scaleFor picks the output box: the request's exact one when it carries it,
// and the preset's otherwise.
//
// The exact box is the compatibility content route. It is scaled to directly
// rather than by stretching a preset result, because a thumbnail of a
// thumbnail is a blurrier answer than the one the caller asked for.
func scaleFor(req preview.Request, img image.Image, lim preview.DecodeLimits) (image.Image, error) {
	if req.Width > 0 && req.Height > 0 {
		return preview.ThumbnailSized(img, int(req.Width), int(req.Height), lim)
	}
	return preview.Thumbnail(img, req.Preset, lim)
}

// clampLimits applies the request's ceiling in one direction only.
//
// The parent may ask for less than the compiled-in bound and never for more: a
// request that could raise a limit is a request that can remove one, and the
// parent is not trusted to be uncompromised.
func clampLimits(req preview.Request, lim preview.DecodeLimits) preview.DecodeLimits {
	if req.MaxPixels > 0 && uint64(req.MaxPixels) < lim.MaxPixels {
		lim.MaxPixels = uint64(req.MaxPixels)
	}
	return lim
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

// readAll reads the input descriptor under the worker's own ceiling.
//
// The limit reader takes one byte past the bound so a source exactly at it is
// distinguishable from one over it.
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
	// Asking for a bound larger than the protocol allows would let a peer hand
	// over more descriptors than a job has.
	n, files, err := preview.RecvMessage(control, buf, jobDescriptors)
	if err != nil {
		return preview.Request{}, nil, nil, fmt.Errorf("receiving a job: %w", err)
	}
	if n == 0 {
		preview.CloseFiles(files)
		return preview.Request{}, nil, nil, io.EOF
	}
	if len(files) != jobDescriptors {
		preview.CloseFiles(files)
		return preview.Request{}, nil, nil, fmt.Errorf(
			"%w: a job arrived with %d descriptors, want %d",
			preview.ErrProtocol, len(files), jobDescriptors)
	}

	req, derr := preview.DecodeRequest(buf[:n])
	if derr != nil {
		preview.CloseFiles(files)
		return preview.Request{}, nil, nil, derr
	}
	return req, files[0], files[1], nil
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
