// Linux only, for the same reason as the rest of this package.
//go:build linux

// The upload family's projection.
//
// Two numbers describe an upload in flight and they are not the same number.
// Offset is where a resume continues from; Received is how many bytes exist.
// A random-access client that wrote past a hole has more received than its
// offset, and showing one where the other belongs either loses a progress bar
// or corrupts a resume.
package handler

import (
	"strconv"

	"github.com/heavycaffeiner/stowcloud/go/engine/service/upload"
)

// UploadSessionView is one upload in flight.
type UploadSessionView struct {
	ID    string `json:"id"`
	Dest  string `json:"dest"`
	State string `json:"state"`

	// Offset is where a resume continues from: the end of the contiguous run
	// starting at zero. Received is how many bytes have landed anywhere.
	Offset   string `json:"offset"`
	Received string `json:"received"`

	// TotalLength is absent for a deferred-length upload, whose size the
	// client has not declared yet. Zero would be a real declared length.
	TotalLength *string `json:"total_length,omitempty"`

	// ChunkSize is what the client should send per request, and Mode which
	// addressing scheme this session takes.
	ChunkSize string `json:"chunk_size"`
	Mode      string `json:"mode"`

	// Gaps is how many separate runs of received bytes exist. One means the
	// upload is contiguous; more means a resume from Offset will re-send bytes
	// that already landed, which is what makes the count worth showing.
	Gaps int `json:"gaps"`

	RandomAccess bool   `json:"random_access"`
	ExpiresNs    string `json:"expires_ns"`

	// Terminal says the session will take no more bytes, decided by the
	// service rather than by a client comparing state names.
	Terminal bool `json:"terminal"`
}

// UploadSessionOf projects one session.
func UploadSessionOf(s upload.Session) UploadSessionView {
	v := UploadSessionView{
		ID:           s.ID.String(),
		Dest:         s.Dest.String(),
		State:        s.State.StateName(),
		Offset:       strconv.FormatUint(s.Offset, 10),
		Received:     strconv.FormatUint(s.Received, 10),
		ChunkSize:    strconv.FormatUint(s.ChunkSize, 10),
		Mode:         s.Mode.ModeName(),
		Gaps:         s.RunCount,
		RandomAccess: s.RandomAccess,
		ExpiresNs:    strconv.FormatInt(s.ExpiresNs, 10),
		Terminal:     s.State.Terminal(),
	}
	if s.TotalLen != nil {
		t := strconv.FormatUint(*s.TotalLen, 10)
		v.TotalLength = &t
	}
	return v
}

// UploadSessionsOf projects a listing.
func UploadSessionsOf(sessions []upload.Session) []UploadSessionView {
	out := make([]UploadSessionView, 0, len(sessions))
	for _, s := range sessions {
		out = append(out, UploadSessionOf(s))
	}
	return out
}

// TerminalUploadState reports whether a state name means the session is
// finished.
//
// Over the name, for the tier that reads names rather than stored numbers. It
// is checked against the service's own list, so the two cannot drift.
func TerminalUploadState(state string) (terminal, known bool) {
	switch state {
	case "done", "aborted", "expired":
		return true, true
	case "receiving", "finalizing":
		return false, true
	default:
		// Unknown counts as finished, and says so. A fallback that could not
		// be told from a listed answer would make the list untestable, since
		// removing an entry would change nothing observable.
		return true, false
	}
}
