package smbagent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
)

// The server's half of the control channel.
//
// One connection per request, with a deadline on every stage: this runs inside
// a settings-screen request, and an agent that accepts a connection and then
// stops talking must not hold that request open.

// ErrNotListening means nothing is there.
//
// Not a fault by itself: a bare-metal deployment may run the agent on a poll
// with no socket, and a deployment with no SMB sidecar at all is a legitimate
// configuration.
var ErrNotListening = errors.New("no SMB agent is listening")

// ErrProtocol means the agent answered something this version does not
// understand.
var ErrProtocol = errors.New("the SMB agent answered something unintelligible")

// maxReportLine bounds what is read back. A report is a handful of share names
// and two address lines, so anything larger is not one, and the agent is
// trusted no further than the rest of this tree trusts a socket peer.
const maxReportLine = 256 << 10

// Do sends one request and reads one answer.
func Do(ctx context.Context, socket string, req Request) (Report, error) {
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", socket)
	if err != nil {
		// Nothing bound and nothing accepting are the same answer to the
		// caller: the agent is not there.
		if errors.Is(err, net.ErrClosed) || isAbsent(err) {
			return Report{}, fmt.Errorf("%w at %s", ErrNotListening, socket)
		}
		return Report{}, fmt.Errorf("connecting to the SMB agent: %w", err)
	}
	defer conn.Close() //nolint:errcheck // the exchange is done, and a closing failure tells the caller nothing they can act on.

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

	line, rerr := bufio.NewReader(io.LimitReader(conn, maxReportLine)).ReadString('\n')
	if rerr != nil && strings.TrimSpace(line) == "" {
		return Report{}, fmt.Errorf("%w: it closed without answering", ErrProtocol)
	}

	var report Report
	if uerr := json.Unmarshal([]byte(line), &report); uerr != nil {
		return Report{}, fmt.Errorf("%w: decoding the answer: %w", ErrProtocol, uerr)
	}
	return report, nil
}

// Apply is "apply now", the only call the server makes in production.
func Apply(ctx context.Context, socket string) (Report, error) {
	return Do(ctx, socket, Request{Op: OpApply})
}

// Status repeats the agent's last report without asking for another apply.
func Status(ctx context.Context, socket string) (Report, error) {
	return Do(ctx, socket, Request{Op: OpStatus})
}

// isAbsent reports whether the failure means nothing is listening, rather than
// something going wrong while connecting.
func isAbsent(err error) bool {
	var op *net.OpError
	if !errors.As(err, &op) {
		return false
	}
	// A missing socket file and a socket nobody is accepting on both mean the
	// agent is not there. A permission failure does not: that is a deployment
	// fault the operator has to see as itself.
	msg := op.Err.Error()
	return strings.Contains(msg, "no such file") || strings.Contains(msg, "connection refused")
}
