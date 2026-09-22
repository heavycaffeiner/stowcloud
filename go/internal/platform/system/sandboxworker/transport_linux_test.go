//go:build linux

package sandboxworker

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestSocketPairCarriesBytesAndDescriptors(t *testing.T) {
	left, right, err := SocketPair()
	if err != nil {
		t.Fatalf("SocketPair: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := left.Close(); closeErr != nil {
			t.Errorf("closing left socket: %v", closeErr)
		}
	})
	t.Cleanup(func() {
		if closeErr := right.Close(); closeErr != nil {
			t.Errorf("closing right socket: %v", closeErr)
		}
	})

	in, err := os.CreateTemp(t.TempDir(), "in-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := in.Close(); closeErr != nil {
			t.Errorf("closing input file: %v", closeErr)
		}
	})
	out, err := os.CreateTemp(t.TempDir(), "out-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := out.Close(); closeErr != nil {
			t.Errorf("closing output file: %v", closeErr)
		}
	})

	want := []byte("neutral payload")
	if sendErr := SendMessage(left, want, in, out); sendErr != nil {
		t.Fatalf("SendMessage: %v", sendErr)
	}
	buf := make([]byte, MaxMessageSize)
	n, files, err := RecvMessage(right, buf, 2)
	if err != nil {
		t.Fatalf("RecvMessage: %v", err)
	}
	t.Cleanup(func() { CloseFiles(files) })
	if !bytes.Equal(buf[:n], want) {
		t.Fatalf("payload = %q, want %q", buf[:n], want)
	}
	if len(files) != 2 {
		t.Fatalf("received %d descriptors, want 2", len(files))
	}
	for i, f := range files {
		if f == nil {
			t.Fatalf("received descriptor %d is nil", i)
		}
		var fd int
		if fdErr := withFdErr(f, func(n int) error { fd = n; return nil }); fdErr != nil || fd < 0 {
			t.Fatalf("received descriptor %d is not open: %v", i, fdErr)
		}
	}
}

func TestTransportRejectsBoundsAndClosedFiles(t *testing.T) {
	left, right, err := SocketPair()
	if err != nil {
		t.Fatalf("SocketPair: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := left.Close(); closeErr != nil {
			t.Errorf("closing left socket: %v", closeErr)
		}
	})
	t.Cleanup(func() {
		if closeErr := right.Close(); closeErr != nil {
			t.Errorf("closing right socket: %v", closeErr)
		}
	})

	if sendErr := SendMessage(left, make([]byte, MaxMessageSize+1)); sendErr == nil {
		t.Fatal("oversized message was accepted")
	}
	if _, _, recvErr := RecvMessage(right, make([]byte, MaxMessageSize+1), 0); recvErr == nil {
		t.Fatal("oversized receive buffer was accepted")
	}
	if _, _, recvErr := RecvMessage(right, make([]byte, MaxMessageSize), MaxFiles+1); recvErr == nil {
		t.Fatal("oversized descriptor bound was accepted")
	}
	closed, err := os.Open("/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	if closeErr := closed.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if sendErr := SendMessage(left, []byte("x"), closed); sendErr == nil {
		t.Fatal("closed descriptor was accepted")
	}
}

func TestTransportRejectsTruncatedMessages(t *testing.T) {
	left, right, err := SocketPair()
	if err != nil {
		t.Fatalf("SocketPair: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := left.Close(); closeErr != nil {
			t.Errorf("closing left socket: %v", closeErr)
		}
	})
	t.Cleanup(func() {
		if closeErr := right.Close(); closeErr != nil {
			t.Errorf("closing right socket: %v", closeErr)
		}
	})

	if sendErr := SendMessage(left, []byte("too large for this receive buffer")); sendErr != nil {
		t.Fatal(sendErr)
	}
	_, _, err = RecvMessage(right, make([]byte, 4), 0)
	if err == nil || !strings.Contains(err.Error(), "truncated") {
		t.Fatalf("truncated payload error = %v", err)
	}
}

func TestTransportRejectsDescriptorOverflow(t *testing.T) {
	left, right, err := SocketPair()
	if err != nil {
		t.Fatalf("SocketPair: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := left.Close(); closeErr != nil {
			t.Errorf("closing left socket: %v", closeErr)
		}
	})
	t.Cleanup(func() {
		if closeErr := right.Close(); closeErr != nil {
			t.Errorf("closing right socket: %v", closeErr)
		}
	})

	files := make([]*os.File, 3)
	for i := range files {
		files[i], err = os.Open("/dev/null")
		if err != nil {
			t.Fatal(err)
		}
		f := files[i]
		t.Cleanup(func() {
			if closeErr := f.Close(); closeErr != nil {
				t.Errorf("closing file %d: %v", i, closeErr)
			}
		})
	}
	if sendErr := SendMessage(left, []byte("x"), files...); sendErr != nil {
		t.Fatal(sendErr)
	}
	_, received, err := RecvMessage(right, make([]byte, 8), 2)
	CloseFiles(received)
	if err == nil || !strings.Contains(err.Error(), "truncated") {
		t.Fatalf("descriptor overflow error = %v", err)
	}
}

func TestTransportRejectsAncillaryDataWhenNoFilesAllowed(t *testing.T) {
	left, right, err := SocketPair()
	if err != nil {
		t.Fatalf("SocketPair: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := left.Close(); closeErr != nil {
			t.Errorf("closing left socket: %v", closeErr)
		}
	})
	t.Cleanup(func() {
		if closeErr := right.Close(); closeErr != nil {
			t.Errorf("closing right socket: %v", closeErr)
		}
	})

	file, err := os.Open("/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := file.Close(); closeErr != nil {
			t.Errorf("closing file: %v", closeErr)
		}
	})
	if sendErr := SendMessage(left, []byte("x"), file); sendErr != nil {
		t.Fatal(sendErr)
	}
	_, received, err := RecvMessage(right, make([]byte, 8), 0)
	CloseFiles(received)
	if err == nil || !strings.Contains(err.Error(), "truncated") {
		t.Fatalf("unexpected ancillary data error = %v", err)
	}
}
