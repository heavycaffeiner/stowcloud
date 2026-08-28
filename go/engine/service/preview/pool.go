//go:build linux

package preview

import (
	"context"
	"errors"
	"fmt"
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
// N worker processes, each on its own SOCK_SEQPACKET pair. A job goes to a free
// worker with the caller's deadline, and a caller finding no free slot is
// refused rather than queued.
//
// Worker death is an ordinary event rather than an exception. A seccomp kill,
// an RLIMIT_AS OOM, a segfault, the CPU limit and a wire version mismatch all
// present identically as an empty read, a reset or a message this build did not
// write, so the parent reaps, fails that one job, and execs a replacement on
// the next job for that slot. Replacement is lazy rather than eager, or a
// source that reliably kills workers becomes a fork bomb.

// Pool failures.
var (
	// ErrWorkerDied is a worker killed mid-job. One thumbnail is lost.
	ErrWorkerDied = errors.New("preview: the worker died")
	// ErrWorkerBusy is no free worker within the caller's deadline.
	ErrWorkerBusy = errors.New("preview: no free worker")
	// ErrNotImplemented is video.
	ErrNotImplemented = errors.New("preview: not implemented in this build")
	// ErrPoolClosed is a job submitted after shutdown.
	ErrPoolClosed = errors.New("preview: the pool is closed")
)

// defaultJobDeadline is what a job gets when the caller supplied no deadline.
// A decode is not an interactive operation, but an unbounded one is a worker
// nobody reclaims.
const defaultJobDeadline = 30 * time.Second

// defaultWorkers is the pool size when the caller names none. This is decode
// work, and more processes than cores buys nothing but memory.
const defaultWorkers = 2

// PoolOptions configures the pool.
type PoolOptions struct {
	// Workers is how many processes to keep. Zero takes a small default.
	Workers int
	// Exe is the binary to run, and Args the argv after it. Defaults to this
	// process's own executable and the preview-worker subcommand, which is
	// what makes the two halves one binary.
	Exe  string
	Args []string
	// Clock is what the deadline is measured against. Nothing else in this
	// package reads a wall clock.
	Clock clock.Clock
	// Env is the child environment. Nil means an empty one, which is
	// deliberate: the worker reads no configuration and an inherited
	// environment is a place to put a path.
	Env []string
}

// Source is the file a worker decodes.
//
// An interface so a share file arrives through the VFS and a plain one (a test
// fixture, a staged upload) arrives directly, without the pool learning which
// it has.
type Source interface {
	File() *os.File
}

// PlainSource is an ordinary file.
type PlainSource struct{ F *os.File }

// File is the descriptor to pass.
func (p PlainSource) File() *os.File { return p.F }

// Pool is the parent side.
type Pool struct {
	opt PoolOptions

	mu     sync.Mutex
	closed bool
	slots  []*slot
	free   chan int
}

// slot is one worker's place in the pool. The process inside it is replaced
// over time; the slot is what persists.
type slot struct {
	mu   sync.Mutex
	proc *os.Process
	// sock is the raw descriptor, which is what sendmsg needs to attach the
	// job's two descriptors to a message. conn is the same socket wrapped for
	// the runtime poller, which is what a read deadline needs.
	sock *os.File
	conn *net.UnixConn
}

// NewPool builds a pool. No process is started here: a worker is exec'd on the
// first job that needs its slot, which is the same laziness that keeps a
// crash loop from becoming a fork bomb.
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

// Generate runs one job.
//
// in and out are descriptors the caller opened. The worker is handed those and
// never a path, which is what makes a traversal bug in a decoder have nothing
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

	// Lazy replacement: the slot's process is started here if it has none,
	// which is also where a worker that died last time gets replaced.
	if s.sock == nil {
		if err := p.start(s); err != nil {
			return Response{}, err
		}
	}

	resp, err := p.exchange(ctx, s, req, in, out)
	if err != nil {
		// Any failure on the socket means this worker is finished. Reaping
		// here rather than on the next job is what keeps a dead process from
		// being handed a second one.
		if cause := reap(s); cause != "" {
			return Response{}, fmt.Errorf("%w (the worker ended: %s)", err, cause)
		}
		return Response{}, err
	}
	return resp, nil
}

// start execs a worker into a slot.
//
// Every failure path closes what it opened. A leak here is a descriptor the
// parent holds for the life of the process and a child nobody reaps.
func (p *Pool) start(s *slot) error {
	// SOCK_SEQPACKET, so a message is a message: a stream would let a short
	// read look like a valid short message, which is exactly the ambiguity the
	// fixed-layout codec exists to avoid.
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
	// The child's descriptor 3, which is where the worker looks for it. It is
	// fixed rather than passed as an argument because the worker parses no
	// arguments: an argv is a place to put a path.
	cmd.ExtraFiles = []*os.File{child}
	cmd.Stdout = nil
	cmd.Stderr = os.Stderr

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

// exchange sends one job and reads its answer.
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

	// The deadline is enforced by killing rather than by waiting: the worker
	// is single-purpose and there is nothing in it to preserve.
	//
	// It goes on the net.Conn rather than on the *os.File, because a descriptor
	// from socketpair is not registered with the runtime poller and an os.File
	// wrapping one refuses a deadline outright.
	if err := s.conn.SetReadDeadline(deadline); err != nil {
		return Response{}, fmt.Errorf("%w: %w", ErrWorkerDied, err)
	}

	buf := make([]byte, limits.WorkerWireMessage)
	n, err := s.conn.Read(buf)
	if err != nil {
		return Response{}, fmt.Errorf("%w: %w", ErrWorkerDied, err)
	}
	if n == 0 {
		// An empty read is what a seccomp kill, an OOM and a segfault all look
		// like from here.
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

// reap kills and waits for a slot's process, leaving the slot empty so the
// next job starts a replacement. It returns how the process ended, for the
// error the caller is already building.
//
// The status is read rather than discarded. Every death is the same event to
// the pool, which is true of what it does next and false of what it can say: a
// worker killed by SIGSYS because seccomp refused a syscall and one that exited
// cleanly would both surface as "the worker died: EOF", naming no cause.
func reap(s *slot) string {
	cause := ""
	if s.proc != nil {
		killProcess(s.proc)
		// Waited for so the child does not become a zombie.
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
		// The cause is dropped here: on Close every worker is killed on
		// purpose, so how it ended says nothing.
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
