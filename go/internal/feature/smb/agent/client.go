package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"syscall"
)

// The server's end of the control channel.
//
// Every stage carries a deadline. This runs inside a settings-screen request,
// so an agent that accepts a connection and then falls silent must not hold
// that request open.

// ErrNotListening reports that nothing is there.
//
// That is not a fault in itself: a deployment running without the SMB
// container names no socket, which is a legitimate configuration.
var ErrNotListening = errors.New("no SMB agent is listening")

// ErrProtocol reports an answer this version cannot interpret.
var ErrProtocol = errors.New("the SMB agent answered something unintelligible")

// MaxReportBytes bounds what a report may occupy.
//
// A report holds a handful of share names and two address lines, so anything
// larger is not one. The agent is trusted no further than any other socket peer
// in this tree.
const MaxReportBytes = 256 << 10

// Do performs one exchange: a single request out, a single answer back.
func Do(ctx context.Context, socket string, req Request) (Report, error) {
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", socket)
	if err != nil {
		// A socket nothing bound and a socket nothing accepts on are the same
		// answer to the caller: the agent is absent.
		if errors.Is(err, net.ErrClosed) || absent(err) {
			return Report{}, fmt.Errorf("%w at %s", ErrNotListening, socket)
		}
		return Report{}, fmt.Errorf("connecting to the SMB agent: %w", err)
	}
	defer conn.Close() //nolint:errcheck // the exchange is over, and a failed close tells the caller nothing actionable.

	// The deadline covers the write and the read together, so a peer that
	// accepts and then stalls cannot outlast the caller's own bound.
	if deadline, ok := ctx.Deadline(); ok {
		if derr := conn.SetDeadline(deadline); derr != nil {
			return Report{}, fmt.Errorf("bounding the SMB agent exchange: %w", derr)
		}
	}

	body, merr := json.Marshal(req)
	if merr != nil {
		return Report{}, fmt.Errorf("%w: encoding the request: %w", ErrProtocol, merr)
	}
	if _, werr := conn.Write(append(body, '\n')); werr != nil {
		return Report{}, fmt.Errorf("sending to the SMB agent: %w", werr)
	}

	return readReport(conn)
}

// readReport reads one answer under the size bound.
//
// The limit wraps the connection rather than being checked after the fact,
// because a bound applied to an already-buffered line has allocated exactly
// what it meant to refuse. A peer that never sends a newline therefore costs
// MaxReportBytes and stops.
func readReport(conn io.Reader) (Report, error) {
	line, rerr := bufio.NewReader(io.LimitReader(conn, MaxReportBytes)).ReadString('\n')
	if rerr != nil && strings.TrimSpace(line) == "" {
		return Report{}, fmt.Errorf("%w: it closed without answering", ErrProtocol)
	}
	// A line that reached the limit without a newline was truncated, so what
	// arrived is a fragment of a report rather than a report. Parsing it would
	// accept whichever prefix happened to be valid JSON.
	if rerr != nil && len(line) >= MaxReportBytes {
		return Report{}, fmt.Errorf("%w: the answer exceeded %d bytes", ErrProtocol, MaxReportBytes)
	}

	var report Report
	if uerr := json.Unmarshal([]byte(line), &report); uerr != nil {
		return Report{}, fmt.Errorf("%w: decoding the answer: %w", ErrProtocol, uerr)
	}
	return report, nil
}

// Apply asks the agent to apply now, the only call the server makes in
// production.
func Apply(ctx context.Context, socket string) (Report, error) {
	return Do(ctx, socket, Request{Op: OpApply})
}

// Status repeats the agent's previous report without requesting another apply.
func Status(ctx context.Context, socket string) (Report, error) {
	return Do(ctx, socket, Request{Op: OpStatus})
}

// absent reports whether a dial failure means nothing is listening, as opposed
// to something going wrong while connecting.
//
// A missing socket file and a socket nobody accepts on both mean the agent is
// not there. A permission failure does not: that is a deployment fault the
// operator needs to see as itself rather than as an absent agent.
func absent(err error) bool {
	var op *net.OpError
	if !errors.As(err, &op) {
		return false
	}
	// Matched as errno values rather than by substring. The old spelling
	// compared the message text, which a translated or reworded libc breaks
	// silently, turning an absent agent into a reported connection fault.
	return errors.Is(op.Err, syscall.ENOENT) || errors.Is(op.Err, syscall.ECONNREFUSED)
}
