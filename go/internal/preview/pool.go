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

	"golang.org/x/sys/unix"

	"github.com/heavycaffeiner/stowcloud/go/internal/limits"
)

// The parent half.
//
// N worker processes, each on its own SOCK_SEQPACKET pair. A job goes to a free
// worker with the caller's deadline.
//
// Worker death is an ordinary event rather than an exception. A seccomp kill,
// an RLIMIT_AS OOM, a segfault and the CPU limit all present identically as an
// empty read or a reset, so the parent reaps, fails that one job, and execs a
// replacement on the next job for that slot. Replacement is lazy rather than
// eager, or a source that reliably kills workers becomes a fork bomb.

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

// PoolOptions configures the pool.
type PoolOptions struct {
	// Workers is how many processes to keep. Zero takes a small default: this
	// is decode work, and more processes than cores buys nothing but memory.
	Workers int
	// Exe is the binary to run, and Args the argv after it. Defaults to this
	// process's own executable and the preview-worker subcommand, which is
	// what makes the two halves one binary.
	Exe  string
	Args []string
	// Env is the child environment. Nil means an empty one, which is
	// deliberate: the worker reads no configuration and an inherited
	// environment is a place to put a path.
	Env []string
}

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
		opt.Workers = 2
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
	for i := 0; i < opt.Workers; i++ {
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
func (p *Pool) Generate(ctx context.Context, req Request, in, out *os.File) (Response, error) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return Response{}, ErrPoolClosed
	}
	p.mu.Unlock()

	var idx int
	select {
	case idx = <-p.free:
	case <-ctx.Done():
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
		p.reap(s)
		return Response{}, err
	}
	return resp, nil
}

// start execs a worker into a slot.
func (p *Pool) start(s *slot) error {
	// SOCK_SEQPACKET, so a message is a message: a stream would let a short
	// read look like a valid short message, which is exactly the ambiguity the
	// fixed-layout codec exists to avoid.
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("preview: creating the control socket: %w", err)
	}
	parent := os.NewFile(uintptr(pair[0]), "worker-control")
	child := os.NewFile(uintptr(pair[1]), "worker-control-child")
	defer func() {
		_ = child.Close() //nolint:errcheck // the child holds its own copy after exec.
	}()

	cmd := exec.Command(p.opt.Exe, p.opt.Args...) //nolint:gosec // G204: the executable is this process's own path, never a caller's.
	cmd.Env = p.opt.Env
	// The child's descriptor 3, which is where the worker looks for it. It is
	// fixed rather than passed as an argument because the worker parses no
	// arguments: an argv is a place to put a path.
	cmd.ExtraFiles = []*os.File{child}
	cmd.Stdout = nil
	cmd.Stderr = os.Stderr

	if serr := cmd.Start(); serr != nil {
		_ = parent.Close() //nolint:errcheck // the start already failed.
		return fmt.Errorf("preview: starting a worker: %w", serr)
	}

	conn, cerr := net.FileConn(parent)
	if cerr != nil {
		_ = parent.Close()     //nolint:errcheck // the wrap already failed.
		_ = cmd.Process.Kill() //nolint:errcheck // nothing can be done with a second failure here.
		return fmt.Errorf("preview: wrapping the control socket: %w", cerr)
	}
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		_ = conn.Close()       //nolint:errcheck // as above.
		_ = parent.Close()     //nolint:errcheck // as above.
		_ = cmd.Process.Kill() //nolint:errcheck // as above.
		return fmt.Errorf("preview: the control socket is a %T, not a unix socket", conn)
	}

	s.proc, s.sock, s.conn = cmd.Process, parent, unixConn
	return nil
}

// exchange sends one job and reads its answer.
func (p *Pool) exchange(ctx context.Context, s *slot, req Request, in, out *os.File) (Response, error) {
	deadline, hasDeadline := ctx.Deadline()
	if !hasDeadline {
		deadline = time.Now().Add(defaultJobDeadline)
	}
	if ms := time.Until(deadline).Milliseconds(); ms > 0 && ms < int64(^uint32(0)) {
		req.DeadlineMs = uint32(ms)
	}

	rights := unix.UnixRights(int(in.Fd()), int(out.Fd()))
	if err := unix.Sendmsg(int(s.sock.Fd()), req.Encode(), rights, nil, 0); err != nil {
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
		// A message that did not parse exactly kills the worker by contract.
		return Response{}, derr
	}
	return resp, nil
}

// reap kills and waits for a slot's process, leaving the slot empty so the
// next job starts a replacement.
func (p *Pool) reap(s *slot) {
	if s.proc != nil {
		_ = s.proc.Kill() //nolint:errcheck // the process may already be gone, which is the case being handled.
		// Waited for so the child does not become a zombie. The status is not
		// read: every way a worker dies is the same event to the parent.
		_, _ = s.proc.Wait() //nolint:errcheck // as above.
		s.proc = nil
	}
	if s.conn != nil {
		_ = s.conn.Close() //nolint:errcheck // the socket is what just failed.
		s.conn = nil
	}
	if s.sock != nil {
		_ = s.sock.Close() //nolint:errcheck // as above.
		s.sock = nil
	}
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
		p.reap(s)
		s.mu.Unlock()
	}
	return nil
}

// defaultJobDeadline is what a job gets when the caller supplied no deadline.
// A decode is not an interactive operation, but an unbounded one is a worker
// nobody reclaims.
const defaultJobDeadline = 30 * time.Second
