package preview

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/limits"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
)

// The RPC codec joining the parent to the jailed worker.
//
//	request   ver:u8 kind:u8 preset:u8 flags:u8 maxpix:u32 deadline_ms:u32
//	response  ver:u8 status:u8 w:u16 h:u16 nbytes:u32 errlen:u16 err:[errlen]u8
//
// A fixed big-endian layout built on encoding/binary with an explicit version
// byte. Neither gob nor JSON, chosen on threat-model grounds rather than
// performance: a reflective decoder sizes its allocations from what the peer
// sends, and that peer may already be running an attacker's decoder bug.
//
// This codec is internal to two halves of one process tree. It is not among the
// presentation layer's wire formats and remains in this package.
//
// A message that fails to parse exactly kills both the job and the worker. A
// partially valid message from the jailed process offers nothing to recover
// from.

// WireVersion identifies the protocol revision. Parent and worker ship inside a
// single binary, so a mismatch means something is impersonating one of them, and
// the pool treats it as a dead worker rather than negotiating.
const WireVersion = 1

// Message sizes, fixed apart from the error string, which is capped to keep the
// entire message within the seqpacket bound.
const (
	// RequestSize covers every fixed field including the exact output box.
	RequestSize = 16
	// ResponseHeaderSize covers everything preceding the error string.
	ResponseHeaderSize = 12
	// MaxErrorLen holds a response comfortably within the socket's message
	// bound. A worker's error text is a diagnostic rather than a payload.
	MaxErrorLen = 1024
)

// ErrProtocol is a message that did not parse exactly. It is fatal to the
// worker by contract, so it is distinct from every other failure a job can
// have.
var ErrProtocol = errors.New("preview: a wire message did not parse exactly")

// JobKind states what the worker is being asked to do.
type JobKind uint8

const (
	// JobImage requests a still image thumbnail.
	JobImage JobKind = 1
	// JobVideo exists so a client requesting one receives a truthful rejection
	// instead of a generic failure. It is never implemented here.
	JobVideo JobKind = 2
	// JobProbe directs the worker to attempt something the jail should block and
	// report the kernel's response. It exists because a security claim that
	// cannot be executed is merely a comment.
	//
	// The probe number rides in the preset field, unused for this kind, since
	// adding a dedicated field would enlarge a message every job pays for to
	// carry something only the proof sends.
	JobProbe JobKind = 3
)

// Valid reports whether k is one of the three kinds.
func (k JobKind) Valid() bool { return k == JobImage || k == JobVideo || k == JobProbe }

func (k JobKind) String() string {
	switch k {
	case JobImage:
		return "image"
	case JobVideo:
		return "video"
	case JobProbe:
		return "probe"
	}
	return "unknown"
}

// Status is how a job ended.
type Status uint8

const (
	StatusOK Status = iota
	// StatusUnsupported reports a format this build has no decoder for.
	StatusUnsupported
	// StatusNotImplemented reports video.
	StatusNotImplemented
	// StatusTooLarge reports a decode limit rejecting the job. The worker
	// survived, which is the entire purpose of a graceful limit.
	StatusTooLarge
	// StatusDecodeFailed reports a file that is not what it claimed.
	StatusDecodeFailed
	// StatusInternal covers anything else the worker could identify.
	StatusInternal
)

// Valid reports whether s is a status this build defines.
func (s Status) Valid() bool { return s <= StatusInternal }

func (s Status) String() string {
	switch s {
	case StatusOK:
		return "ok"
	case StatusUnsupported:
		return "unsupported"
	case StatusNotImplemented:
		return "not implemented"
	case StatusTooLarge:
		return "too large"
	case StatusDecodeFailed:
		return "decode failed"
	case StatusInternal:
		return "internal"
	}
	return "unknown"
}

// FlagStripEXIF instructs the worker to apply orientation and propagate no other
// metadata. This build always sets it; the bit exists so a worker declining to
// honour it becomes visible rather than assumed.
const FlagStripEXIF = 1 << 0

// The worker's test hooks, named here rather than in the worker package so the
// pool and its tests can name them without importing the half that imports
// this one. Named constants rather than bare strings, so a typo is a compile
// error rather than a silently normal worker.
//
// Empty in the product: the two failures they produce, dying mid-job and never
// answering, are what the parent's reap and deadline paths exist to handle and
// cannot otherwise be reached on demand.
const (
	ModeDie  = "die"
	ModeHang = "hang"
)

// Request is one job.
type Request struct {
	Kind   JobKind
	Preset Preset
	Flags  uint8
	// MaxPixels sets this job's source-pixel ceiling, letting the parent's limit
	// accompany the request rather than being compiled into two places capable
	// of disagreeing. The worker clamps it, so a request may lower the
	// compiled-in ceiling but never raise it.
	MaxPixels  uint32
	DeadlineMs uint32
	// Width and Height are an exact output box for the compatibility content
	// route. Zero means the preset's own bounds, which is every other caller.
	// They travel in the request for the same reason MaxPixels does: the size
	// the worker scales to should live in one place, not in two that can
	// disagree.
	Width  uint16
	Height uint16
}

// Response is what came back.
type Response struct {
	Status Status
	Width  uint16
	Height uint16
	// Bytes reports how much the worker wrote to the output descriptor.
	Bytes uint32
	Err   string
}

// Encode serializes a request. It cannot fail, since every field is fixed
// width.
func (r Request) Encode() []byte {
	out := make([]byte, RequestSize)
	out[0] = WireVersion
	out[1] = byte(r.Kind)
	out[2] = byte(r.Preset)
	out[3] = r.Flags
	binary.BigEndian.PutUint32(out[4:], r.MaxPixels)
	binary.BigEndian.PutUint32(out[8:], r.DeadlineMs)
	binary.BigEndian.PutUint16(out[12:], r.Width)
	binary.BigEndian.PutUint16(out[14:], r.Height)
	return out
}

// DecodeRequest parses a request message.
//
// Validation of every field happens here rather than in the caller, because this
// is the worker-side trust boundary: the bytes arrived over a socket, and the
// worker acts on whatever this produces.
func DecodeRequest(b []byte) (Request, error) {
	if len(b) != RequestSize {
		return Request{}, fmt.Errorf("%w: a request of %d bytes, want %d",
			ErrProtocol, len(b), RequestSize)
	}
	if b[0] != WireVersion {
		return Request{}, fmt.Errorf("%w: protocol version %d, want %d",
			ErrProtocol, b[0], WireVersion)
	}
	r := Request{
		Kind:       JobKind(b[1]),
		Preset:     Preset(b[2]),
		Flags:      b[3],
		MaxPixels:  binary.BigEndian.Uint32(b[4:]),
		DeadlineMs: binary.BigEndian.Uint32(b[8:]),
		Width:      binary.BigEndian.Uint16(b[12:]),
		Height:     binary.BigEndian.Uint16(b[14:]),
	}
	if !r.Kind.Valid() {
		return Request{}, fmt.Errorf("%w: job kind %d", ErrProtocol, b[1])
	}
	// A probe stores its number where a preset would sit, so the preset is
	// range-checked only for the kinds that treat it as one.
	if r.Kind != JobProbe && !r.Preset.Valid() {
		return Request{}, fmt.Errorf("%w: preset %d", ErrProtocol, b[2])
	}
	// An undefined flag bit indicates the peer speaks a protocol this build does
	// not implement. Ignoring it would mean acting on a request nobody wrote.
	if r.Flags&^FlagStripEXIF != 0 {
		return Request{}, fmt.Errorf("%w: unknown request flags %#x", ErrProtocol, r.Flags)
	}
	return r, nil
}

// Encode serializes a response.
//
// An error string exceeding the cap is truncated rather than rejected: the
// worker is already reporting a failure, and losing the end of the text beats
// converting a diagnostic into a protocol kill.
func (r Response) Encode() []byte {
	msg := r.Err
	if len(msg) > MaxErrorLen {
		msg = msg[:MaxErrorLen]
	}
	// The truncation above keeps this comfortably within a uint16, so the
	// conversion cannot shorten the length.
	errLen, nerr := num.Narrow[uint16](len(msg))
	if nerr != nil {
		errLen, msg = 0, ""
	}
	out := make([]byte, ResponseHeaderSize+len(msg))
	out[0] = WireVersion
	out[1] = byte(r.Status)
	binary.BigEndian.PutUint16(out[2:], r.Width)
	binary.BigEndian.PutUint16(out[4:], r.Height)
	binary.BigEndian.PutUint32(out[6:], r.Bytes)
	binary.BigEndian.PutUint16(out[10:], errLen)
	copy(out[ResponseHeaderSize:], msg)
	return out
}

// DecodeResponse parses a response message.
func DecodeResponse(b []byte) (Response, error) {
	if len(b) < ResponseHeaderSize {
		return Response{}, fmt.Errorf("%w: a response of %d bytes, want at least %d",
			ErrProtocol, len(b), ResponseHeaderSize)
	}
	if len(b) > limits.WorkerWireMessage {
		return Response{}, fmt.Errorf("%w: a response of %d bytes, past the %d-byte message bound",
			ErrProtocol, len(b), limits.WorkerWireMessage)
	}
	if b[0] != WireVersion {
		return Response{}, fmt.Errorf("%w: protocol version %d, want %d",
			ErrProtocol, b[0], WireVersion)
	}

	r := Response{
		Status: Status(b[1]),
		Width:  binary.BigEndian.Uint16(b[2:]),
		Height: binary.BigEndian.Uint16(b[4:]),
		Bytes:  binary.BigEndian.Uint32(b[6:]),
	}
	if !r.Status.Valid() {
		return Response{}, fmt.Errorf("%w: status %d", ErrProtocol, b[1])
	}

	errLen := int(binary.BigEndian.Uint16(b[10:]))
	if errLen > MaxErrorLen {
		return Response{}, fmt.Errorf("%w: an error string of %d bytes, past the %d cap",
			ErrProtocol, errLen, MaxErrorLen)
	}
	// The declared length must match exactly what is present. Trailing bytes mean
	// a message this build never wrote, and a shortfall means truncation; both
	// are the same fatal condition.
	if ResponseHeaderSize+errLen != len(b) {
		return Response{}, fmt.Errorf("%w: an error length of %d in a %d-byte message",
			ErrProtocol, errLen, len(b))
	}
	r.Err = string(b[ResponseHeaderSize:])
	return r, nil
}
