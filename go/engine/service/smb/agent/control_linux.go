//go:build linux

package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/clock"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/task"
	"golang.org/x/sys/unix"
)

// The listening end, which the server pushes changes to.
//
// One request, one answer, one connection, handled serially. An apply takes the
// agent's own lock regardless, so accepting concurrently would only move the
// queue somewhere else.
//
// This half is what makes a change immediate. Without it the server writes its
// files and waits for a poll to notice, learning nothing about the outcome when
// it finally does.

// ExchangeTimeout bounds one exchange, so a client that connects and then says
// nothing cannot hold the loop.
const ExchangeTimeout = 5 * time.Second

// MaxRequestBytes bounds a request line. The whole vocabulary is two words, so
// anything larger is not a request.
const MaxRequestBytes = 4 << 10

// Handler answers the two operations. The runtime supplies it; this file owns
// the transport and nothing else.
type Handler interface {
	// Apply performs a pass and reports it. The context bounds the external
	// commands a pass runs, so a caller that gives up stops waiting on them
	// rather than leaving them to finish against a connection that is gone.
	Apply(ctx context.Context) Report
	// Last repeats the previous report without performing a pass.
	Last() Report
}

// Serve binds the socket and answers until the context ends.
//
// clk supplies the deadline each exchange is bounded by. It is a parameter
// rather than a direct wall-clock read so this package keeps no clock of its
// own, which is the rule the whole tree follows.
func Serve(ctx context.Context, socket string, h Handler, configDir string, clk clock.Clock, log *slog.Logger) error {
	if parent := filepath.Dir(socket); parent != "" {
		if err := prepareSocketDir(parent); err != nil {
			return err
		}
	}

	// A socket file left by a killed agent makes every later bind fail.
	// Deleting it carries no risk here, since nothing else owns this path and
	// an agent still holding it means this process should never have run.
	if err := os.Remove(socket); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("clearing the old socket: %w", err)
	}

	ln, err := net.Listen("unix", socket)
	if err != nil {
		return fmt.Errorf("binding the control socket in %s: %w", filepath.Dir(socket), err)
	}
	defer ln.Close() //nolint:errcheck // the process is ending and the next start removes the socket file.

	handOver(socket, configDir, log)
	log.Info("listening for apply requests from the server", "socket", socket)

	// Closing the listener is what releases the accept below. Absent that, a
	// shutdown waits on a connection that may never arrive and the process
	// hangs while exiting.
	task.Go(ctx, "smb-agent-control-close", func() {
		<-ctx.Done()
		if cerr := ln.Close(); cerr != nil {
			log.Warn("the control socket did not close", "error", cerr)
		}
	})

	for {
		conn, aerr := ln.Accept()
		if aerr != nil {
			if ctx.Err() != nil {
				// The shutdown closed it, so this is an orderly exit.
				return nil
			}
			return fmt.Errorf("accepting on the control socket: %w", aerr)
		}
		answer(ctx, conn, h, clk, log)
	}
}

// answer handles one connection: read a request, act, reply, close.
func answer(ctx context.Context, conn net.Conn, h Handler, clk clock.Clock, log *slog.Logger) {
	defer conn.Close() //nolint:errcheck // the reply is already written and a failed close has nowhere to go.

	if err := conn.SetDeadline(clk.Now().Add(ExchangeTimeout)); err != nil {
		log.Warn("could not bound the control exchange", "error", err)
		return
	}

	report := dispatch(ctx, conn, h, log)

	body, merr := json.Marshal(report)
	if merr != nil {
		body = []byte(`{"ok":false,"error":"the report could not be encoded"}`)
	}
	if _, werr := conn.Write(append(body, '\n')); werr != nil {
		log.Warn("the answer could not be sent", "error", werr)
	}
}

// dispatch reads one request and produces the report answering it.
func dispatch(ctx context.Context, conn io.Reader, h Handler, log *slog.Logger) Report {
	line, err := readRequestLine(conn)
	if err != nil {
		log.Warn("reading a request", "error", err)
		return FailedReport(err.Error())
	}

	var req Request
	if json.Unmarshal([]byte(line), &req) != nil {
		report := FailedReport("unintelligible request")
		LogReport(log, report, "the server")
		return report
	}

	switch req.Op {
	case OpApply:
		report := h.Apply(ctx)
		LogReport(log, report, "the server")
		return report
	case OpStatus:
		// Repeating the previous answer is not an event.
		return h.Last()
	default:
		report := FailedReport("unknown request: " + req.Op)
		LogReport(log, report, "the server")
		return report
	}
}

// readRequestLine reads one line under the size bound.
//
// The limit wraps the connection. A bufio.Reader does not enforce one: its
// buffer size governs a single fill, while ReadString keeps growing its own
// result until it finds the delimiter, so a peer sending no newline is bounded
// by the peer rather than by the reader. Measured on this tree, the previous
// arrangement accumulated 64 MiB from a 4 KiB buffer.
//
// A line that reaches the limit without a newline was truncated, and the
// truncation is refused rather than parsed: whichever prefix happens to be
// valid JSON is not the request the peer sent.
func readRequestLine(conn io.Reader) (string, error) {
	line, err := bufio.NewReader(io.LimitReader(conn, MaxRequestBytes)).ReadString('\n')
	switch {
	case err != nil && len(line) >= MaxRequestBytes:
		return "", fmt.Errorf("the request exceeded %d bytes", MaxRequestBytes)
	case err != nil && line == "":
		return "", fmt.Errorf("the peer closed without sending a request: %w", err)
	default:
		return line, nil
	}
}

// prepareSocketDir makes the socket's directory one this process can create in.
//
// It acts only where the directory is not already writable, and reports what it
// could not repair rather than failing the start: the poll loop applies the same
// changes without the socket, which is what this deployment had before the
// channel existed.
func prepareSocketDir(dir string) error {
	if dir == "" || dir == "." {
		return nil
	}
	// Group-writable, because the server writes this directory as a different
	// uid and reaching it is the entire purpose of the channel.
	if err := os.MkdirAll(dir, 0o770); err != nil { //nolint:gosec // G301 wants 0750: the server runs as another uid and must create in here.
		return fmt.Errorf("creating the control socket's directory %s: %w", dir, err)
	}
	// unix.Access asks the question the bind is about to ask, rather than
	// inferring it from the mode: a read-only mount and a directory owned by
	// another uid both look writable in the bits alone.
	if unix.Access(dir, unix.W_OK) == nil {
		return nil
	}
	if err := os.Chown(dir, os.Geteuid(), os.Getegid()); err != nil {
		return fmt.Errorf(
			"the control socket's directory %s is not writable by this process and could not be taken over: %w",
			dir, err)
	}
	return nil
}

// handOver makes the socket reachable by the server and by as little else as
// can be managed.
//
// The recipient is whoever owns the rendered configuration directory, because
// that is the process writing there, which by construction is the server. On
// bare metal both sides share a user and nothing needs doing.
//
// Inside a container the hand-over cannot happen at all. Having dropped every
// capability, this sidecar cannot give a file away, leaving a narrowly-moded
// socket unreachable from the server's container. Granting that capability back
// to a container parsing SMB off the wire costs more than it returns, so the
// fallback widens the mode instead.
//
// That fallback rests on an assumption worth stating where the code relying on
// it lives: a world-writable control socket able to trigger an apply is a
// privilege surface if the volume isolation ever fails to hold. It holds today
// because the socket sits on a volume reachable only by the two containers that
// mount it and by the host's root, and the vocabulary behind it is two words.
func handOver(socket, configDir string, log *slog.Logger) {
	const narrow = os.FileMode(0o660)

	st, err := os.Stat(configDir)
	if err != nil {
		setMode(socket, narrow, log)
		return
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok || int(sys.Uid) == os.Geteuid() {
		setMode(socket, narrow, log)
		return
	}

	// The mode is set before the owner changes. Once the socket belongs to
	// somebody else this process can no longer chmod it, so the opposite order
	// left a socket correctly handed over and still at whatever mode the umask
	// produced.
	setMode(socket, narrow, log)
	if cerr := os.Chown(socket, int(sys.Uid), int(sys.Gid)); cerr != nil {
		log.Info(
			"cannot hand the control socket to the server's account, so it is opened to anything that can already reach this directory",
			"uid", sys.Uid, "error", cerr)
		// Widened only because the narrow mode is now unusable by the one
		// process that has to reach it.
		setMode(socket, 0o666, log) //nolint:gosec // G302: the hand-over failed, so the alternative is a socket nothing can use.
	}
}

// setMode applies a mode and reports a failure without stopping the agent.
//
// A socket nobody can reach costs the push and leaves the poll doing the work,
// which is what this deployment had before the channel existed. The server
// reports it as unreachable rather than continuing in silence.
func setMode(socket string, mode os.FileMode, log *slog.Logger) {
	if err := os.Chmod(socket, mode); err != nil {
		log.Warn("could not set the control socket's mode", "mode", mode, "error", err)
	}
}

// ServeInBackground runs the listener alongside the poll loop.
//
// A failure here is not fatal: the poll still applies changes, which is exactly
// what this deployment had before the socket existed.
func ServeInBackground(
	ctx context.Context, socket string, h Handler, configDir string, clk clock.Clock, log *slog.Logger,
) {
	task.Go(ctx, "smb-agent-control", func() {
		if err := Serve(ctx, socket, h, configDir, clk, log); err != nil {
			log.Error("the control socket is not listening; changes will be picked up by the poll instead",
				"socket", socket, "error", err)
		}
	})
}
