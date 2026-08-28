package preview

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/limits"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
)

// The RPC codec between the parent and the jailed worker.
//
//	request:  ver:u8 kind:u8 preset:u8 flags:u8 maxpix:u32 deadline_ms:u32
//	response: ver:u8 status:u8 w:u16 h:u16 nbytes:u32 errlen:u16 err:[errlen]u8
//
// A fixed layout over encoding/binary, big-endian, with an explicit version
// byte. Deliberately not gob and not JSON, and the reason is the threat model
// rather than performance: a reflective decoder allocates based on what the
// peer sends, and the peer is a process that may already be executing an
// attacker's decoder bug.
//
// This is an internal codec between two halves of one process tree. It is not
// one of the presentation layer's wire shapes and it stays in this package.
//
// A message that does not parse exactly kills the job and the worker. A
// partially valid message from the jailed process is not a thing to recover
// from.

// WireVersion is the protocol revision. Parent and worker are shipped in one
// binary, so a mismatch means something is impersonating one of them, and the
// pool reads it as a dead worker rather than negotiating.
const WireVersion = 1

// Message sizes. Both are fixed except the error string, which is capped so
// the whole message stays inside the seqpacket bound.
const (
	// RequestSize covers every fixed field including the exact output box.
	RequestSize = 16
	// ResponseHeaderSize is everything before the error string.
	ResponseHeaderSize = 12
	// MaxErrorLen keeps a response inside the socket's message bound with room
	// to spare. A worker's error text is a diagnostic, not a payload.
	MaxErrorLen = 1024
)

// ErrProtocol is a message that did not parse exactly. It is fatal to the
// worker by contract, so it is distinct from every other failure a job can
// have.
var ErrProtocol = errors.New("preview: a wire message did not parse exactly")

// JobKind is what the worker is being asked to do.
type JobKind uint8

const (
	// JobImage is a still image thumbnail.
	JobImage JobKind = 1
	// JobVideo exists so a client asking for one gets an honest refusal
	// rather than a generic failure. It is never implemented here.
	JobVideo JobKind = 2
	// JobProbe asks the worker to attempt something the jail should prevent,
	// and report what the kernel said. It exists because a security claim that
	// cannot be executed is a comment.
	//
	// The probe number travels in the preset field, which is otherwise unused
	// for this kind: adding a field would widen a message every job pays for,
	// to carry something only the proof sends.
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
	// StatusUnsupported is a format with no decoder in this build.
	StatusUnsupported
	// StatusNotImplemented is video.
	StatusNotImplemented
	// StatusTooLarge is a decode limit refusing. The worker survived, which
	// is the whole point of having a graceful limit.
	StatusTooLarge
	// StatusDecodeFailed is a file that is not what it claimed to be.
	StatusDecodeFailed
	// StatusInternal is anything else the worker could name.
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

// FlagStripEXIF asks the worker to apply orientation and carry no other
// metadata across. It is always set by this build; the bit exists so a worker
// refusing to honour it is visible rather than assumed.
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
	// MaxPixels is the source-pixel ceiling for this job, so the parent's
	// limit travels with the request rather than being compiled into two
	// places that can disagree. The worker clamps it: a request may lower the
	// compiled-in ceiling and never raise it.
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
	// Bytes is how much the worker wrote to the output descriptor.
	Bytes uint32
	Err   string
}

// Encode renders a request. It cannot fail: every field is fixed width.
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

// DecodeRequest parses a request.
//
// Every field is validated here rather than by the caller, because this is the
// worker-side trust boundary: the bytes came off a socket, and the worker acts
// on whatever this returns.
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
	// A probe carries its number where a preset would be, so the preset is
	// only range-checked for the kinds that use it as one.
	if r.Kind != JobProbe && !r.Preset.Valid() {
		return Request{}, fmt.Errorf("%w: preset %d", ErrProtocol, b[2])
	}
	// An undefined flag bit means the peer is speaking a protocol this build
	// does not have. Ignoring it would be acting on a request nobody wrote.
	if r.Flags&^FlagStripEXIF != 0 {
		return Request{}, fmt.Errorf("%w: unknown request flags %#x", ErrProtocol, r.Flags)
	}
	return r, nil
}

// Encode renders a response.
//
// An error string past the cap is truncated rather than refused: the worker is
// reporting a failure and losing the tail of the text is better than turning a
// diagnostic into a protocol kill.
func (r Response) Encode() []byte {
	msg := r.Err
	if len(msg) > MaxErrorLen {
		msg = msg[:MaxErrorLen]
	}
	// The truncation above bounds this well inside a uint16, so the length
	// cannot be cut short by the conversion.
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

// DecodeResponse parses a response.
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
	// The declared length has to be exactly what is there. A message with
	// trailing bytes is not a message this build wrote, and a short one is
	// truncated: both are the same fatal case.
	if ResponseHeaderSize+errLen != len(b) {
		return Response{}, fmt.Errorf("%w: an error length of %d in a %d-byte message",
			ErrProtocol, errLen, len(b))
	}
	r.Err = string(b[ResponseHeaderSize:])
	return r, nil
}
