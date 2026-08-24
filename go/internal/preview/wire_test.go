package preview

import (
	"errors"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/internal/limits"
)

// The peer on the other end of this codec is the least trusted process in the
// system: it may already be executing an attacker's decoder bug. So a message
// that does not parse exactly is fatal, and every field is validated here
// rather than by a caller that might not.

func TestARequestRoundTrips(t *testing.T) {
	want := Request{
		Kind: JobImage, Preset: PresetMedium, Flags: FlagStripEXIF,
		MaxPixels: 40_000_000, DeadlineMs: 5000,
	}
	got, err := DecodeRequest(want.Encode())
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestAResponseRoundTrips(t *testing.T) {
	want := Response{Status: StatusOK, Width: 256, Height: 192, Bytes: 4096}
	got, err := DecodeResponse(want.Encode())
	if err != nil {
		t.Fatalf("DecodeResponse: %v", err)
	}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestAResponseCarriesItsError(t *testing.T) {
	want := Response{Status: StatusDecodeFailed, Err: "not a jpeg after all"}
	got, err := DecodeResponse(want.Encode())
	if err != nil {
		t.Fatalf("DecodeResponse: %v", err)
	}
	if got.Err != want.Err || got.Status != want.Status {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// A message of the wrong length is not a message this build wrote.
func TestARequestOfTheWrongLengthIsRefused(t *testing.T) {
	good := Request{Kind: JobImage, Preset: PresetSmall}.Encode()
	for _, bad := range [][]byte{
		{}, good[:len(good)-1], append(append([]byte{}, good...), 0),
	} {
		if _, err := DecodeRequest(bad); !errors.Is(err, ErrProtocol) {
			t.Fatalf("a %d-byte request returned %v, want ErrProtocol", len(bad), err)
		}
	}
}

func TestAWrongVersionIsRefused(t *testing.T) {
	req := Request{Kind: JobImage, Preset: PresetSmall}.Encode()
	req[0] = WireVersion + 1
	if _, err := DecodeRequest(req); !errors.Is(err, ErrProtocol) {
		t.Fatalf("a wrong-version request returned %v, want ErrProtocol", err)
	}

	res := Response{Status: StatusOK}.Encode()
	res[0] = WireVersion + 1
	if _, err := DecodeResponse(res); !errors.Is(err, ErrProtocol) {
		t.Fatalf("a wrong-version response returned %v, want ErrProtocol", err)
	}
}

// An undefined enum value means the peer is speaking a protocol this build
// does not have. Acting on it would be acting on a request nobody wrote.
func TestAnUnknownKindPresetOrStatusIsRefused(t *testing.T) {
	req := Request{Kind: JobImage, Preset: PresetSmall}.Encode()
	req[1] = 99
	if _, err := DecodeRequest(req); !errors.Is(err, ErrProtocol) {
		t.Fatalf("an unknown job kind returned %v, want ErrProtocol", err)
	}

	req = Request{Kind: JobImage, Preset: PresetSmall}.Encode()
	req[2] = 99
	if _, err := DecodeRequest(req); !errors.Is(err, ErrProtocol) {
		t.Fatalf("an unknown preset returned %v, want ErrProtocol", err)
	}

	res := Response{Status: StatusOK}.Encode()
	res[1] = 99
	if _, err := DecodeResponse(res); !errors.Is(err, ErrProtocol) {
		t.Fatalf("an unknown status returned %v, want ErrProtocol", err)
	}
}

func TestAnUnknownFlagBitIsRefused(t *testing.T) {
	req := Request{Kind: JobImage, Preset: PresetSmall}.Encode()
	req[3] = 0x80
	if _, err := DecodeRequest(req); !errors.Is(err, ErrProtocol) {
		t.Fatalf("an unknown flag bit returned %v, want ErrProtocol", err)
	}
}

// The declared error length has to be exactly what is there. A message with
// trailing bytes was not written by this build, and a short one is truncated:
// both are the same fatal case.
func TestAnErrorLengthThatDisagreesWithTheMessageIsRefused(t *testing.T) {
	base := Response{Status: StatusInternal, Err: "abc"}.Encode()

	short := append([]byte{}, base...)
	short = short[:len(short)-1]
	if _, err := DecodeResponse(short); !errors.Is(err, ErrProtocol) {
		t.Fatalf("a truncated error string returned %v, want ErrProtocol", err)
	}

	long := append(append([]byte{}, base...), 'x')
	if _, err := DecodeResponse(long); !errors.Is(err, ErrProtocol) {
		t.Fatalf("a message with trailing bytes returned %v, want ErrProtocol", err)
	}
}

// A declared length past the cap must be refused before it sizes anything.
func TestAnAbsurdErrorLengthIsRefused(t *testing.T) {
	res := Response{Status: StatusInternal}.Encode()
	res[10], res[11] = 0xff, 0xff
	if _, err := DecodeResponse(res); !errors.Is(err, ErrProtocol) {
		t.Fatalf("an absurd error length returned %v, want ErrProtocol", err)
	}
}

// A worker reporting a failure should not have its diagnostic turned into a
// protocol kill, so an over-long error string is truncated on the way out.
func TestAnOverlongErrorIsTruncatedRatherThanRefused(t *testing.T) {
	huge := strings.Repeat("e", MaxErrorLen*4)
	got, err := DecodeResponse(Response{Status: StatusInternal, Err: huge}.Encode())
	if err != nil {
		t.Fatalf("an over-long error string was refused: %v", err)
	}
	if len(got.Err) != MaxErrorLen {
		t.Fatalf("the error is %d bytes, want it truncated to %d", len(got.Err), MaxErrorLen)
	}
}

// Every message this build can produce fits the socket's bound, or a legal
// response would be unsendable.
func TestEveryEncodableMessageFitsTheSocketBound(t *testing.T) {
	huge := Response{Status: StatusInternal, Err: strings.Repeat("e", MaxErrorLen*10)}.Encode()
	if len(huge) > limits.WorkerWireMessage {
		t.Fatalf("the largest response is %d bytes, past the %d-byte bound",
			len(huge), limits.WorkerWireMessage)
	}
	if len(Request{Kind: JobImage, Preset: PresetSmall}.Encode()) != RequestSize {
		t.Fatal("a request is not its fixed size")
	}
}

// A response past the socket bound cannot have come from this build.
func TestAResponsePastTheSocketBoundIsRefused(t *testing.T) {
	oversized := make([]byte, limits.WorkerWireMessage+1)
	oversized[0] = WireVersion
	if _, err := DecodeResponse(oversized); !errors.Is(err, ErrProtocol) {
		t.Fatalf("an oversized response returned %v, want ErrProtocol", err)
	}
}

// Video is answered honestly rather than as a generic failure, so the kind
// survives the round trip and the status has its own name.
func TestVideoIsAKindAndNotImplementedIsAStatus(t *testing.T) {
	got, err := DecodeRequest(Request{Kind: JobVideo, Preset: PresetSmall}.Encode())
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}
	if got.Kind != JobVideo {
		t.Fatalf("kind = %v, want video", got.Kind)
	}
	if StatusNotImplemented.String() != "not implemented" {
		t.Fatalf("the status is named %q", StatusNotImplemented.String())
	}
}

func TestPresetBoundsGrow(t *testing.T) {
	var prev int
	for _, p := range Presets() {
		w, h := p.Bounds()
		if w <= 0 || h <= 0 {
			t.Fatalf("%v has bounds %dx%d", p, w, h)
		}
		if w <= prev {
			t.Fatalf("%v is not larger than the preset before it", p)
		}
		prev = w
	}
	if _, h := Preset(99).Bounds(); h != 0 {
		t.Fatal("an unknown preset has bounds")
	}
}

// The codec reads bytes off a socket from the least trusted process in the
// system, so it is fuzzed. Nothing may panic, and anything that parses must
// re-encode to exactly what was read.
func FuzzDecodeRequest(f *testing.F) {
	f.Add(Request{Kind: JobImage, Preset: PresetSmall, Flags: FlagStripEXIF}.Encode())
	f.Add(Request{Kind: JobVideo, Preset: PresetLarge}.Encode())
	f.Add(make([]byte, RequestSize))
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, b []byte) {
		r, err := DecodeRequest(b)
		if err != nil {
			return
		}
		if got := r.Encode(); string(got) != string(b) {
			t.Fatalf("DecodeRequest(%x) re-encodes to %x", b, got)
		}
		if !r.Kind.valid() || !r.Preset.valid() {
			t.Fatalf("DecodeRequest(%x) accepted kind %d preset %d", b, r.Kind, r.Preset)
		}
	})
}

func FuzzDecodeResponse(f *testing.F) {
	f.Add(Response{Status: StatusOK, Width: 1, Height: 1}.Encode())
	f.Add(Response{Status: StatusTooLarge, Err: "too big"}.Encode())
	f.Add(make([]byte, ResponseHeaderSize))
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, b []byte) {
		r, err := DecodeResponse(b)
		if err != nil {
			return
		}
		if got := r.Encode(); string(got) != string(b) {
			t.Fatalf("DecodeResponse(%x) re-encodes to %x", b, got)
		}
		if !r.Status.valid() {
			t.Fatalf("DecodeResponse(%x) accepted status %d", b, r.Status)
		}
		if len(r.Err) > MaxErrorLen {
			t.Fatalf("DecodeResponse(%x) produced a %d-byte error string", b, len(r.Err))
		}
	})
}
