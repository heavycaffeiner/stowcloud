//go:build linux

package preview

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/clock"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/limits"
)

// The parent half.
//
// N worker processes, each with its own SOCK_SEQPACKET pair. Jobs go to a free
// worker carrying the caller's deadline, and a caller that finds no free slot is
// rejected rather than queued.
//
// A worker dying is routine rather than exceptional. A seccomp kill, an
// RLIMIT_AS OOM, a segfault, the CPU limit and a wire version mismatch all
// appear identically as an empty read, a reset, or a message this build never
// wrote. The parent reaps, fails that single job, and execs a replacement on the
// next job for that slot. Replacement is lazy rather than eager, or a source
// that reliably kills workers turns into a fork bomb.

// Pool failures.
var (
	// ErrWorkerDied reports a worker killed mid-job, costing one thumbnail.
	ErrWorkerDied = errors.New("preview: the worker died")
	// ErrWorkerBusy reports no worker becoming free within the caller's
	// deadline.
	ErrWorkerBusy = errors.New("preview: no free worker")
	// ErrNotImplemented is video.
	ErrNotImplemented = errors.New("preview: not implemented in this build")
	// ErrPoolClosed reports a job submitted after shutdown.
	ErrPoolClosed = errors.New("preview: the pool is closed")
)

// defaultJobDeadline applies when the caller supplies none. Decoding is not
// interactive, yet an unbounded decode leaves a worker nobody reclaims.
const defaultJobDeadline = 30 * time.Second

// defaultWorkers is the pool size when the caller names none. This is decode
// work, and more processes than cores buys nothing but memory.
const defaultWorkers = 2

// PoolOptions holds the pool's configuration.
type PoolOptions struct {
	// Workers is how many processes to keep. Zero takes a small default.
	Workers int
	// Exe names the binary to run and Args supplies the argv following it. They
	// default to this process's own executable plus the preview-worker
	// subcommand, which is what keeps both halves inside one binary.
	Exe  string
	Args []string
	// Clock provides the reference the deadline is measured against. Nothing
	// else in this package consults a wall clock.
	Clock clock.Clock
	// Env supplies the child environment, where nil yields an empty one. That is
	// deliberate: the worker reads no configuration, and an inherited
	// environment would be somewhere to put a path.
	Env []string
	// Stderr receives the worker's diagnostics. Nil sends them to this
	// process's own stderr, which is what a deployment wants; a test that has
	// to read what a dying worker said supplies a file, because a harness
	// between the two does not reliably carry it.
	Stderr io.Writer
}

// Source identifies the file a worker decodes.
//
// It is an interface so a share file can arrive through the VFS while a plain
// one, such as a test fixture or a staged upload, arrives directly, without the
// pool discovering which it holds.
type Source interface {
	File() *os.File
}

// PlainSource wraps an ordinary file.
type PlainSource struct{ F *os.File }

// File returns the descriptor to hand over.
func (p PlainSource) File() *os.File { return p.F }

// Pool is the parent side.
type Pool struct {
	opt PoolOptions

	mu     sync.Mutex
	closed bool
	slots  []*slot
	free   chan int
}

// slot holds one worker's position in the pool. The process within it is
// replaced over time while the slot itself endures.
type slot struct {
	mu   sync.Mutex
	proc *os.Process
	// sock is the raw descriptor that sendmsg requires in order to attach the
	// job's two descriptors to a message. conn is that same socket wrapped for
	// the runtime poller, which is what a read deadline requires.
	sock *os.File
	conn *net.UnixConn
}

// NewPool constructs a pool without starting any process. A worker is exec'd on
// the first job requiring its slot, the same laziness that stops a crash loop
// from becoming a fork bomb.
func NewPool(opt PoolOptions) (*Pool, error) {
	if opt.Workers <= 0 {
		opt.Workers = defaultWorkers
	}
	if opt.Clock == nil {
		opt.Clock = clock.System()
	}
	if opt.Exe == "" {
		self, err := os.Executable()
		if err != nil {
			return nil, fmt.Errorf("preview: finding this executable: %w", err)
		}
		opt.Exe = self
	}
	if len(opt.Args) == 0 {
		opt.Args = []string{"preview-worker"}
	}

	p := &Pool{opt: opt, free: make(chan int, opt.Workers)}
	for i := range opt.Workers {
		p.slots = append(p.slots, &slot{})
		p.free <- i
	}
	return p, nil
}

// Generate executes a single job.
//
// in and out are descriptors the caller opened. The worker receives those and
// never a path, which is what leaves a traversal bug in a decoder with nothing
// to traverse.
func (p *Pool) Generate(ctx context.Context, req Request, in Source, out *os.File) (Response, error) {
	p.mu.Lock()
	closed := p.closed
	p.mu.Unlock()
	if closed {
		return Response{}, ErrPoolClosed
	}

	// The gate is non-blocking: a full pool refuses at the cost of a channel
	// receive rather than queueing behind a decode.
	var idx int
	select {
	case idx = <-p.free:
	case <-ctx.Done():
		return Response{}, ErrWorkerBusy
	default:
		return Response{}, ErrWorkerBusy
	}
	defer func() { p.free <- idx }()

	s := p.slots[idx]
	s.mu.Lock()
	defer s.mu.Unlock()

	// Replacement happens lazily: the slot's process starts here when it has
	// none, which is also where a worker that died previously is replaced.
	if s.sock == nil {
		if err := p.start(s); err != nil {
			return Response{}, err
		}
	}

	resp, err := p.exchange(ctx, s, req, in, out)
	if err != nil {
		// Any socket failure ends this worker. Reaping here rather than on the
		// next job is what prevents a dead process from receiving a second.
		if cause := reap(s); cause != "" {
			return Response{}, fmt.Errorf("%w (the worker ended: %s)", err, cause)
		}
		return Response{}, err
	}
	return resp, nil
}

// start launches a worker process into a slot.
//
// Every failure path releases whatever it opened. A leak here means a descriptor
// the parent retains for the life of the process plus a child nobody reaps.
func (p *Pool) start(s *slot) error {
	// SOCK_SEQPACKET keeps message boundaries intact. A stream would let a short
	// read pass as a valid short message, precisely the ambiguity the
	// fixed-layout codec exists to eliminate.
	parent, child, err := SocketPair()
	if err != nil {
		return err
	}
	defer func() {
		//nolint:errcheck // the child holds its own copy after exec.
		_ = child.Close()
	}()

	//nolint:gosec // G204: the executable is this process's own path, never a caller's.
	cmd := exec.Command(p.opt.Exe, p.opt.Args...)
	cmd.Env = p.opt.Env
	// The child's descriptor 3, where the worker expects to find it. The value is
	// fixed rather than supplied as an argument because the worker parses none:
	// an argv would be somewhere to put a path.
	cmd.ExtraFiles = []*os.File{child}
	cmd.Stdout = nil
	cmd.Stderr = os.Stderr
	if p.opt.Stderr != nil {
		cmd.Stderr = p.opt.Stderr
	}

	if serr := cmd.Start(); serr != nil {
		closeFile(parent)
		return fmt.Errorf("preview: starting a worker: %w", serr)
	}

	conn, cerr := net.FileConn(parent)
	if cerr != nil {
		closeFile(parent)
		killProcess(cmd.Process)
		return fmt.Errorf("preview: wrapping the control socket: %w", cerr)
	}
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		//nolint:errcheck // the wrap already failed and the socket is being abandoned.
		_ = conn.Close()
		closeFile(parent)
		killProcess(cmd.Process)
		return fmt.Errorf("preview: the control socket is a %T, not a unix socket", conn)
	}

	s.proc, s.sock, s.conn = cmd.Process, parent, unixConn
	return nil
}

// exchange dispatches a job and reads the reply.
func (p *Pool) exchange(
	ctx context.Context, s *slot, req Request, in Source, out *os.File,
) (Response, error) {
	deadline, hasDeadline := ctx.Deadline()
	if !hasDeadline {
		deadline = p.opt.Clock.Now().Add(defaultJobDeadline)
	}
	if ms := deadline.Sub(p.opt.Clock.Now()).Milliseconds(); ms > 0 && ms < int64(^uint32(0)) {
		req.DeadlineMs = uint32(ms)
	}

	if s.sock == nil {
		return Response{}, ErrWorkerDied
	}
	// Both descriptors go over as an SCM_RIGHTS message, and neither leaves its
	// owner as a bare number: the transport holds each file alive across the
	// syscall.
	if err := SendMessage(s.sock, req.Encode(), in.File(), out); err != nil {
		return Response{}, fmt.Errorf("%w: sending the job: %w", ErrWorkerDied, err)
	}

	// The deadline is enforced by killing rather than waiting, since the worker
	// serves one purpose and holds nothing worth preserving.
	//
	// It is set on the net.Conn instead of the *os.File, because a descriptor
	// produced by socketpair is not registered with the runtime poller, and an
	// os.File
	// wrapping one rejects a deadline outright.
	if err := s.conn.SetReadDeadline(deadline); err != nil {
		return Response{}, fmt.Errorf("%w: %w", ErrWorkerDied, err)
	}

	buf := make([]byte, limits.WorkerWireMessage)
	n, err := s.conn.Read(buf)
	if err != nil {
		return Response{}, fmt.Errorf("%w: %w", ErrWorkerDied, err)
	}
	if n == 0 {
		// An empty read is how a seccomp kill, an OOM and a segfault all appear
		// from this side.
		return Response{}, ErrWorkerDied
	}

	resp, derr := DecodeResponse(buf[:n])
	if derr != nil {
		// A message that did not parse exactly, including one carrying another
		// build's wire version, is a dead worker rather than a negotiation.
		return Response{}, fmt.Errorf("%w: %w", ErrWorkerDied, derr)
	}
	return resp, nil
}

// reap kills a slot's process and waits for it, leaving the slot empty so the
// next job starts a replacement. It reports how the process ended, feeding the
// error the caller is already assembling.
//
// The status is read rather than thrown away. Every death is the same event to
// the pool, which holds for what it does next but not for what it can report: a
// worker killed by SIGSYS after seccomp refused a syscall and one that exited
// cleanly would both appear as a worker dying with EOF, naming no cause.
func reap(s *slot) string {
	cause := ""
	if s.proc != nil {
		killProcess(s.proc)
		// Waited on so the child does not linger as a zombie.
		st, werr := s.proc.Wait()
		switch {
		case werr != nil:
			cause = "wait: " + werr.Error()
		case st != nil:
			cause = st.String()
		}
		s.proc = nil
	}
	if s.conn != nil {
		//nolint:errcheck // the socket is what just failed.
		_ = s.conn.Close()
		s.conn = nil
	}
	if s.sock != nil {
		//nolint:errcheck // as above.
		_ = s.sock.Close()
		s.sock = nil
	}
	return cause
}

// Close shuts the pool down.
func (p *Pool) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	p.mu.Unlock()

	for _, s := range p.slots {
		s.mu.Lock()
		// The cause is discarded here, since Close kills every worker
		// deliberately and how one ended conveys nothing.
		reap(s)
		s.mu.Unlock()
	}
	return nil
}

func closeFile(f *os.File) {
	if f == nil {
		return
	}
	//nolint:errcheck // a descriptor being abandoned on a path that already failed.
	_ = f.Close()
}

func killProcess(proc *os.Process) {
	if proc == nil {
		return
	}
	//nolint:errcheck // the process may already be gone, which is the case being handled.
	_ = proc.Kill()
}
