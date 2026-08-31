package preview

import (
	"errors"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/limits"
)

func TestRequestRoundTrips(t *testing.T) {
	want := Request{
		Kind:       JobImage,
		Preset:     PresetMedium,
		Flags:      FlagStripEXIF,
		MaxPixels:  1 << 20,
		DeadlineMs: 4321,
		Width:      640,
		Height:     480,
	}
	raw := want.Encode()
	if len(raw) != RequestSize {
		t.Fatalf("encoded %d bytes, want %d", len(raw), RequestSize)
	}
	if raw[0] != WireVersion {
		t.Errorf("the first byte is %d, want the version %d", raw[0], WireVersion)
	}

	got, err := DecodeRequest(raw)
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}
	if got != want {
		t.Errorf("round trip gave %+v, want %+v", got, want)
	}
}

func TestResponseRoundTrips(t *testing.T) {
	want := Response{Status: StatusOK, Width: 256, Height: 128, Bytes: 9001, Err: "none"}
	got, err := DecodeResponse(want.Encode())
	if err != nil {
		t.Fatalf("DecodeResponse: %v", err)
	}
	if got != want {
		t.Errorf("round trip gave %+v, want %+v", got, want)
	}
}

// A version mismatch is a worker from another build, which is a dead worker
// rather than a negotiation.
func TestAVersionMismatchIsRefused(t *testing.T) {
	raw := Request{Kind: JobImage, Preset: PresetSmall}.Encode()
	raw[0] = WireVersion + 1
	if _, err := DecodeRequest(raw); !errors.Is(err, ErrProtocol) {
		t.Errorf("a request from another build: %v", err)
	}

	resp := Response{Status: StatusOK}.Encode()
	resp[0] = WireVersion + 1
	if _, err := DecodeResponse(resp); !errors.Is(err, ErrProtocol) {
		t.Errorf("a response from another build: %v", err)
	}
}

// Every field is validated at the worker's trust boundary, because the bytes
// came off a socket and the worker acts on whatever this returns.
func TestDecodeRequestValidatesEveryField(t *testing.T) {
	base := Request{Kind: JobImage, Preset: PresetSmall}.Encode()

	cases := []struct {
		name   string
		mutate func([]byte)
	}{
		{"an unknown job kind", func(b []byte) { b[1] = 9 }},
		{"a zero job kind", func(b []byte) { b[1] = 0 }},
		{"an invalid preset", func(b []byte) { b[2] = 99 }},
		{"a zero preset", func(b []byte) { b[2] = 0 }},
		{"an undefined flag bit", func(b []byte) { b[3] = 0x80 }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			raw := make([]byte, len(base))
			copy(raw, base)
			c.mutate(raw)
			if _, err := DecodeRequest(raw); !errors.Is(err, ErrProtocol) {
				t.Errorf("got %v, want ErrProtocol", err)
			}
		})
	}

	// A short or long message is the same fatal case.
	for _, n := range []int{0, RequestSize - 1, RequestSize + 1} {
		if _, err := DecodeRequest(make([]byte, n)); !errors.Is(err, ErrProtocol) {
			t.Errorf("a %d-byte request: %v", n, err)
		}
	}
}

// A probe carries its number where a preset would be, so the preset is only
// range-checked for the kinds that use it as one.
func TestAProbeCarriesItsNumberInThePresetField(t *testing.T) {
	raw := Request{Kind: JobProbe, Preset: Preset(42)}.Encode()
	got, err := DecodeRequest(raw)
	if err != nil {
		t.Fatalf("a probe was refused: %v", err)
	}
	if got.Preset != Preset(42) {
		t.Errorf("the probe number came through as %d", got.Preset)
	}
}

// An error string exceeding the cap is truncated rather than rejected, since the
// worker is already reporting a failure, and losing the end beats turning a
// diagnostic into a killed connection.
func TestALongErrorStringIsTruncatedNotRefused(t *testing.T) {
	long := strings.Repeat("x", MaxErrorLen*3)
	got, err := DecodeResponse(Response{Status: StatusInternal, Err: long}.Encode())
	if err != nil {
		t.Fatalf("a long error string killed the protocol: %v", err)
	}
	if len(got.Err) != MaxErrorLen {
		t.Errorf("the error string is %d bytes, want it truncated to %d", len(got.Err), MaxErrorLen)
	}
}

// The declared length has to be exactly what is there: trailing bytes are not
// a message this build wrote and a short one is truncated.
func TestResponseLengthMustMatchExactly(t *testing.T) {
	raw := Response{Status: StatusOK, Err: "hi"}.Encode()

	short := raw[:len(raw)-1]
	if _, err := DecodeResponse(short); !errors.Is(err, ErrProtocol) {
		t.Errorf("a truncated response: %v", err)
	}
	long := append(raw, 'x')
	if _, err := DecodeResponse(long); !errors.Is(err, ErrProtocol) {
		t.Errorf("a response with trailing bytes: %v", err)
	}
	if _, err := DecodeResponse(make([]byte, ResponseHeaderSize-1)); !errors.Is(err, ErrProtocol) {
		t.Errorf("a response shorter than a header: %v", err)
	}
	if _, err := DecodeResponse(make([]byte, limits.WorkerWireMessage+1)); !errors.Is(err, ErrProtocol) {
		t.Errorf("a response past the message bound: %v", err)
	}
}

func TestDecodeResponseRefusesAnUnknownStatus(t *testing.T) {
	raw := Response{Status: StatusOK}.Encode()
	raw[1] = 99
	if _, err := DecodeResponse(raw); !errors.Is(err, ErrProtocol) {
		t.Errorf("got %v, want ErrProtocol", err)
	}
}

// A whole response stays inside the seqpacket message bound, which is what
// makes a message a message rather than a short read.
func TestAMaximalResponseFitsTheMessageBound(t *testing.T) {
	raw := Response{Status: StatusInternal, Err: strings.Repeat("e", MaxErrorLen)}.Encode()
	if len(raw) > limits.WorkerWireMessage {
		t.Errorf("a maximal response is %d bytes, past the %d bound", len(raw), limits.WorkerWireMessage)
	}
}

func TestKindAndStatusNames(t *testing.T) {
	for k, want := range map[JobKind]string{
		JobImage: "image", JobVideo: "video", JobProbe: "probe", JobKind(9): "unknown",
	} {
		if got := k.String(); got != want {
			t.Errorf("%d.String() = %q, want %q", k, got, want)
		}
	}
	for s, want := range map[Status]string{
		StatusOK: "ok", StatusUnsupported: "unsupported", StatusNotImplemented: "not implemented",
		StatusTooLarge: "too large", StatusDecodeFailed: "decode failed",
		StatusInternal: "internal", Status(99): "unknown",
	} {
		if got := s.String(); got != want {
			t.Errorf("%d.String() = %q, want %q", s, got, want)
		}
	}
	if JobKind(0).Valid() || !JobImage.Valid() {
		t.Error("JobKind.Valid does not match the three kinds")
	}
	if Status(99).Valid() || !StatusInternal.Valid() {
		t.Error("Status.Valid does not match the defined statuses")
	}
}

func TestPresetBoundsAndValidity(t *testing.T) {
	for _, p := range Presets() {
		if !p.Valid() {
			t.Errorf("%v is in Presets and is not valid", p)
		}
		w, h := p.Bounds()
		if w <= 0 || h <= 0 {
			t.Errorf("%v has a %dx%d box", p, w, h)
		}
	}
	if Preset(0).Valid() || Preset(4).Valid() {
		t.Error("an out-of-range preset reported itself valid")
	}
	if w, h := Preset(9).Bounds(); w != 0 || h != 0 {
		t.Errorf("an invalid preset has a %dx%d box", w, h)
	}
	// The numbering is fixed: these are wire values and cache keys.
	if PresetSmall != 1 || PresetMedium != 2 || PresetLarge != 3 {
		t.Error("the preset numbering changed, which invalidates every cache key")
	}
	// Larger presets have larger boxes, which is what makes the smallest
	// covering preset a meaningful choice.
	sw, _ := PresetSmall.Bounds()
	mw, _ := PresetMedium.Bounds()
	lw, _ := PresetLarge.Bounds()
	if sw >= mw || mw >= lw {
		t.Errorf("the preset boxes are not ordered: %d, %d, %d", sw, mw, lw)
	}
}

func FuzzDecodeRequestNeverPanics(f *testing.F) {
	f.Add(Request{Kind: JobImage, Preset: PresetSmall}.Encode())
	f.Add(make([]byte, RequestSize))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, in []byte) {
		req, err := DecodeRequest(in)
		if err != nil {
			return
		}
		// Anything that parsed carries a kind this build has, and a preset
		// that is valid unless the kind is a probe.
		if !req.Kind.Valid() {
			t.Errorf("an invalid kind %d passed the boundary", req.Kind)
		}
		if req.Kind != JobProbe && !req.Preset.Valid() {
			t.Errorf("an invalid preset %d passed the boundary", req.Preset)
		}
		if req.Flags&^FlagStripEXIF != 0 {
			t.Errorf("an undefined flag %#x passed the boundary", req.Flags)
		}
	})
}

func FuzzDecodeResponseNeverPanics(f *testing.F) {
	f.Add(Response{Status: StatusOK, Err: "x"}.Encode())
	f.Add(make([]byte, ResponseHeaderSize))
	f.Fuzz(func(t *testing.T, in []byte) {
		resp, err := DecodeResponse(in)
		if err != nil {
			return
		}
		if !resp.Status.Valid() {
			t.Errorf("an invalid status %d passed the boundary", resp.Status)
		}
		if len(resp.Err) > MaxErrorLen {
			t.Errorf("an error string of %d bytes passed the cap", len(resp.Err))
		}
	})
}
