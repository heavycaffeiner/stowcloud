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

// No path is ever given to the worker. The seccomp allow list omits openat and
// the Landlock domain grants nothing, leaving a path-traversal bug in a decoder
// with nothing to traverse. Input and output reach it as descriptors alongside
// each job.

// ControlFD is the descriptor carrying the control socket from the parent.
//
// It is three because the runtime reserves zero, one and two. The value is fixed
// rather than supplied as an argument because the worker parses no arguments at
// all: an argv would be somewhere to put a path, and this process must have no
// means of naming a file.
const ControlFD = 3

// MaxInputBytes caps a single source image.
//
// The parent already rejects larger files, so this represents the worker
// refusing to take the parent's word about a length. They are separate
// processes and either could be compromised. The duplication is deliberate.
const MaxInputBytes = 256 << 20

// jobDescriptors is how many descriptors accompany one job: the input and the
// output, and never a third.
const jobDescriptors = 2

// trapEnv swaps the worker filter's kill for a SIGSYS trap. Diagnostic only,
// read from the environment because the worker parses no arguments.
const trapEnv = "SC_PREVIEW_TRAP"

// errnoEnv fails an unlisted call with ENOSYS instead of killing. Diagnostic
// only, and read from the environment for the same reason as the trap.
const errnoEnv = "SC_PREVIEW_ERRNO"

// Run constitutes the worker's entire life: install the jail, then serve jobs
// until the socket closes.
//
// The sequence carries the design, and the two calls left dead in the previous
// build are wired into it:
//
//  1. GOMAXPROCS(1), before anything spawns a thread.
//  2. The Landlock domain together with its re-exec.
//  3. SealDescriptors, closing everything inherited beyond the control socket:
//     listening sockets, share roots, database handles. RLIMIT_NOFILE limits
//     what may newly be opened and does nothing about these.
//  4. ApplyLimits, giving the in-process pixel ceiling the kernel backstop the
//     comments have always described. A decoder exploit is precisely where an
//     in-process bound ceases to matter.
//  5. The seccomp filter last, because it cannot be undone.
//
// Only afterwards is the first message read. A decoder bug in that first message
// is exactly what the jail exists for.
func Run(policy jail.Policy) (jail.Status, error) {
	// Pinned to one so the runtime spawns no thread after the filter is
	// installed, which is what allows clone to stay off the allow list. This
	// does not produce a single OS thread: the runtime retains several for its
	// own purposes regardless. What matters is that they exist before the
	// filter, not that they are absent.
	//
	// The cost here is nil, since a worker decodes one image at a time by
	// design and the pool supplies the parallelism.
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

	// The allow-list filter belongs to the worker and kills rather than
	// returning an error, because a decoder reaching a syscall it does not need
	// is already running something nobody wrote.
	//
	// The trap variant replaces the kill with SIGSYS, which the runtime prints
	// as a stack naming the call. The errno variant fails the call with ENOSYS
	// instead, which the runtime reports through its ordinary error paths and
	// which therefore survives a harness that drops the worker's stderr: the
	// trap diagnosed a missing syscall locally and printed nothing whatsoever
	// in CI, where the same failure read "bad system call" and no more.
	//
	// Neither ships. A trap a handler absorbs is not a sandbox, and a decoder
	// handed ENOSYS may fall back and carry on past the call meant to stop it.
	filter := jail.FilterWorker
	switch {
	case os.Getenv(trapEnv) == "1":
		filter = jail.FilterWorkerTrap
	case os.Getenv(errnoEnv) == "1":
		filter = jail.FilterWorkerErrno
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

// Serve executes the job loop over an already-open control socket.
//
// It is exported so the pool's tests can exercise a real worker process without
// installing the jail, which is verified separately against a live kernel. mode
// is a test hook and stays empty in the product: the two behaviours it offers,
// dying mid-job and never replying, are what the parent's reap and deadline
// paths exist to handle and cannot otherwise be triggered on demand.
func Serve(control *os.File, mode string) (int, error) {
	if control == nil {
		return 0, errors.New("preview worker: no control socket")
	}
	return 0, serveLoop(control, mode)
}

// workerSpec describes a Landlock domain that grants nothing.
//
// A spec without grants denies the entire filesystem, which is the intent: the
// worker holds descriptors opened by the parent and has no route to opening
// another. EXECUTE stays unhandled so the domain survives the re-exec that makes
// it process-wide, and seccomp eliminates execve afterwards, which stops it more
// firmly.
func workerSpec() jail.Spec {
	return jail.Spec{ExceptExec: true}
}

// serveLoop consumes jobs until the socket closes.
func serveLoop(control *os.File, mode string) error {
	for {
		req, in, out, err := recvJob(control)
		if err != nil {
			if errors.Is(err, io.EOF) {
				// The parent disconnected, which is how a worker retires.
				return nil
			}
			// A message failing to parse exactly is fatal by contract. A
			// partially valid message from this process's peer offers nothing
			// to recover from, and neither does one this process emitted.
			return err
		}

		switch mode {
		case preview.ModeDie:
			// How a seccomp kill, an OOM and a segfault all appear from the
			// parent: the process has simply vanished mid-job.
			preview.CloseFiles([]*os.File{in, out})
			os.Exit(1)
		case preview.ModeHang:
			// Never replies, leaving the parent's deadline to end it. A sleep
			// instead of a bare select, since the runtime converts the latter
			// into a deadlock panic when it is the sole goroutine.
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

// handle processes one job and never returns an error. Failures travel as a
// status on the response, because the worker's purpose is to answer rather than
// to die.
func handle(req preview.Request, in, out *os.File) preview.Response {
	switch req.Kind {
	case preview.JobProbe:
		// Attempted from within the completed jail. A probe that gets killed
		// never replies at all: the process disappears and the parent observes
		// the socket closing, which itself counts as a pass.
		outcome, detail := RunProbe(Probe(req.Preset))
		return preview.Response{
			Status: preview.StatusOK,
			Width:  uint16(outcome),
			Err:    detail,
		}
	case preview.JobVideo:
		// The truthful answer, preserved across the wire so a client receives an
		// actionable rejection instead of a generic failure.
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
		// Applied to the pixels and then discarded. The encoder emits no
		// metadata, so nothing propagates from here.
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

// decodeFailure translates a decode error into a wire status.
func decodeFailure(err error) preview.Response {
	switch {
	case errors.Is(err, preview.ErrTooLarge):
		// The graceful limit triggered, which is its entire purpose: the worker
		// lives to report it rather than being killed by RLIMIT_AS.
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

// countingWriter tracks how much reached the output descriptor, which the parent
// needs in order to know how much of the file the thumbnail occupies.
type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// recvJob reads a single request together with its two descriptors.
//
// Every job carries exactly two, input and output. Any other count is the same
// fatal condition as a message that failed to parse: the peer is not speaking
// this protocol.
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
