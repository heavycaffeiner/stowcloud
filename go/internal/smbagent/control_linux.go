//go:build linux

package smbagent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/internal/task"
)

// The socket the server pushes to.
//
// One request, one answer, one connection, handled serially: an apply takes the
// agent's own lock anyway, so accepting concurrently would only queue in a
// different place.
//
// This is the half of the channel that makes a change immediate. Without it the
// server writes four files and waits for a poll to notice, and learns nothing
// about what happened when it does.

// controlTimeout bounds one exchange. A client that connects and then says
// nothing must not hold the loop.
const controlTimeout = 5 * time.Second

// maxRequestLine bounds what is read before a request is refused. The whole
// vocabulary is two words, so anything larger is not a request.
const maxRequestLine = 4 << 10

// Serve binds and answers until the context ends.
func Serve(ctx context.Context, socket string, agent *Agent, configDir string, log *slog.Logger) error {
	if parent := filepath.Dir(socket); parent != "" {
		if err := os.MkdirAll(parent, 0o755); err != nil { //nolint:gosec // G301 wants stricter: the directory holds one socket whose own mode is the gate, and both containers have to traverse it.
			return fmt.Errorf("the socket directory: %w", err)
		}
	}
	// A socket file left behind by a killed agent makes the bind fail forever.
	// Removing it is safe: nothing else owns this path, and a live agent
	// holding it means this process should not have started.
	if err := os.Remove(socket); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("clearing the old socket: %w", err)
	}

	ln, err := net.Listen("unix", socket)
	if err != nil {
		return fmt.Errorf("binding the control socket: %w", err)
	}
	defer ln.Close() //nolint:errcheck // the process is ending, and the socket file is removed by the next start.

	setSocketOwner(socket, configDir, log)
	log.Info("listening for apply requests from the server", "socket", socket)

	// Closing the listener is what unblocks the accept below. Without it a
	// shutdown waits for a connection that may never come, and the process
	// hangs on the way out.
	task.Go(ctx, "smb-agent-control-close", func() {
		<-ctx.Done()
		if err := ln.Close(); err != nil {
			log.Warn("the control socket did not close", "error", err)
		}
	})

	for {
		conn, aerr := ln.Accept()
		if aerr != nil {
			if ctx.Err() != nil {
				// The shutdown closed it, which is not a failure.
				return nil
			}
			return fmt.Errorf("accepting on the control socket: %w", aerr)
		}
		handle(conn, agent, log)
	}
}

func handle(conn net.Conn, agent *Agent, log *slog.Logger) {
	defer conn.Close() //nolint:errcheck // the answer is flushed by the write below, and a closing failure has nowhere to go.

	// The clock comes from the agent rather than being read here, which is
	// what keeps every wall-clock read in one place.
	if err := conn.SetDeadline(agent.clock.Now().Add(controlTimeout)); err != nil {
		log.Warn("could not bound the control exchange", "error", err)
		return
	}

	reader := bufio.NewReaderSize(conn, maxRequestLine)
	line, rerr := reader.ReadString('\n')
	if rerr != nil && line == "" {
		log.Warn("reading a request", "error", rerr)
		return
	}

	var req Request
	var report Report
	switch {
	case len(line) > maxRequestLine:
		report = FailedReport("the request is too large to be one")
		LogReport(log, report, "the server")
	case json.Unmarshal([]byte(line), &req) != nil:
		report = FailedReport("unintelligible request")
		LogReport(log, report, "the server")
	default:
		switch req.Op {
		case OpApply:
			report = agent.Apply()
			LogReport(log, report, "the server")
		case OpStatus:
			// Repeating the last answer is not an event.
			report = agent.Last()
		default:
			report = FailedReport("unknown request: " + req.Op)
			LogReport(log, report, "the server")
		}
	}

	body, merr := json.Marshal(report)
	if merr != nil {
		body = []byte(`{"ok":false,"error":"the report could not be encoded"}`)
	}
	body = append(body, '\n')
	if _, werr := conn.Write(body); werr != nil {
		log.Warn("the answer could not be sent", "error", werr)
	}
}

// setSocketOwner makes the socket reachable by the server and by nothing else
// that can be helped.
//
// The identity to hand it to is whoever owns the rendered configuration
// directory: that is the process which writes there, which is the server by
// construction. On bare metal both sides are the same user and there is
// nothing to do.
//
// The container case cannot hand it over at all. Dropping every capability
// leaves this sidecar unable to give a file away, so a socket at the narrow
// mode is one the server's container is refused on. Adding that capability
// back to a container that parses SMB off the wire buys less than it costs, so
// the fallback is open, which is not the wide grant it looks like: the socket
// lives on a volume only the containers that mount it and the host's root can
// reach at all, and the vocabulary behind it is two words.
func setSocketOwner(socket, configDir string, log *slog.Logger) {
	mode := os.FileMode(0o660)

	if st, err := os.Stat(configDir); err == nil {
		if sys, ok := st.Sys().(*syscall.Stat_t); ok && int(sys.Uid) != os.Geteuid() {
			if cerr := os.Chown(socket, int(sys.Uid), int(sys.Gid)); cerr != nil {
				log.Info("cannot hand the control socket to the server's account, so it is opened to anything that can already reach this directory",
					"uid", sys.Uid, "error", cerr)
				mode = 0o666
			}
		}
	}

	if err := os.Chmod(socket, mode); err != nil {
		// A socket nobody can reach costs the push and leaves the poll doing
		// the work, which is what this deployment had before the channel
		// existed. The server reports it as unreachable rather than silently
		// continuing.
		log.Warn("could not set the control socket's mode", "error", err)
	}
}

// ServeInBackground runs the listener beside the poll loop.
//
// Not fatal when it fails: the poll still applies changes, which is exactly
// what this deployment had before the socket.
func ServeInBackground(ctx context.Context, socket string, agent *Agent, configDir string, log *slog.Logger) {
	task.Go(ctx, "smb-agent-control", func() {
		if err := Serve(ctx, socket, agent, configDir, log); err != nil {
			log.Error("the control socket is not listening; changes will be picked up by the poll instead",
				"socket", socket, "error", err)
		}
	})
}
