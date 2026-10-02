//go:build linux

package e2e_test

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/heavycaffeiner/stowcloud/backend/internal/platform/clock"
	"github.com/heavycaffeiner/stowcloud/backend/internal/platform/protocol/limits"
)

// A body declared past its route's class is answered with a 413 the client can
// actually read. The request is written by hand over a raw socket because the
// defect is in what reaches the peer: net/http reports the reset as a transport
// error and never surfaces the status the server wrote.
func TestAnOversizedDeclaredBodyIsRefusedWithAReadableStatus(t *testing.T) {
	base := boot(t)
	addr := strings.TrimPrefix(base, "http://")

	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatalf("dialing: %v", err)
	}
	defer func() {
		// The server may have closed its side already, which is not a fault
		// here: the status it wrote is what this test reads.
		if cerr := conn.Close(); cerr != nil && !errors.Is(cerr, net.ErrClosed) {
			t.Errorf("closing the connection: %v", cerr)
		}
	}()
	if derr := conn.SetDeadline(clock.System().Now().Add(20 * time.Second)); derr != nil {
		t.Fatalf("deadline: %v", derr)
	}

	// Past the JSON class bound, and within the drain cap: the server reads
	// these bytes so the refusal can reach the client.
	const declared = 6 << 20
	head := fmt.Sprintf("POST /api/v1/auth/login HTTP/1.1\r\nHost: %s\r\nOrigin: http://%s\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n", addr, addr, declared)
	if _, werr := conn.Write([]byte(head)); werr != nil {
		t.Fatalf("writing headers: %v", werr)
	}

	// A real client writes its body and then reads. Writing it all first is
	// what exposes the defect: if the server closed on unread bytes, the
	// write fails with a reset and the status never arrives.
	chunk := make([]byte, 32<<10)
	for sent := 0; sent < declared; sent += len(chunk) {
		if _, werr := conn.Write(chunk); werr != nil {
			t.Fatalf("the peer broke the connection instead of answering: %v", werr)
		}
	}

	resp, rerr := http.ReadResponse(bufio.NewReader(conn), nil)
	if rerr != nil {
		t.Fatalf("the client never received a status: %v", rerr)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			t.Errorf("closing the body: %v", cerr)
		}
	}()
	if _, derr := io.Copy(io.Discard, resp.Body); derr != nil {
		t.Fatalf("reading the body: %v", derr)
	}

	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("answered %d, want 413", resp.StatusCode)
	}
}

// postLoginBody sends body to the sign-in route unchanged. A reader that is
// not a strings.Reader leaves the length undeclared, so the body goes out
// chunked.
func postLoginBody(t *testing.T, base string, body io.Reader) int {
	t.Helper()

	req, err := http.NewRequest(http.MethodPost, base+"/api/v1/auth/login", body)
	if err != nil {
		t.Fatalf("building: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", base)

	resp, err := testClient().Do(req)
	if err != nil {
		t.Fatalf("requesting: %v", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			t.Errorf("closing the body: %v", cerr)
		}
	}()
	readAll(t, resp)
	return resp.StatusCode
}

// A body of two documents is refused: which one the server honoured would
// depend on which one its decoder happened to take.
func TestAJSONBodyOfTwoDocumentsIsRefused(t *testing.T) {
	t.Parallel()
	base := boot(t)

	body := strings.NewReader(`{"login":"alice","password":"x"}{"login":"root","password":"y"}`)
	if status := postLoginBody(t, base, body); status != http.StatusBadRequest {
		t.Errorf("answered %d, want 400", status)
	}
}

// A chunked body declares no length, so the bound is enforced while it is read.
func TestAnUndeclaredBodyPastTheBoundIsRefused(t *testing.T) {
	t.Parallel()
	base := boot(t)

	pad := strings.Repeat("a", limits.RequestBody+4096)
	body := io.MultiReader(strings.NewReader(`{"login":"` + pad + `","password":"x"}`))
	if status := postLoginBody(t, base, body); status != http.StatusRequestEntityTooLarge {
		t.Errorf("answered %d, want 413", status)
	}
}
