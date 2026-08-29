//go:build linux

// What a transfer, a rollup and a recent listing look like on the wire.
package handler

import (
	"strconv"

	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
)

// MoveView reports where an entry landed and how it got there.
//
// The destination is echoed from the result rather than from the request,
// because a rename policy lands the entry at a suffixed name the caller never
// asked for. A client that assumed its own path would then be pointing at
// nothing.
type MoveView struct {
	Path string `json:"path"`

	// Copied marks a move that crossed a boundary a rename cannot, so it was
	// carried out as a copy and a delete. Visible because the two differ in
	// how long they take and in what a partial failure leaves behind.
	Copied bool `json:"copied"`

	// Skipped is a taken destination left alone by policy. Distinct from
	// success: nothing was written, and a client that treated this as done
	// would report a move that never happened.
	Skipped bool `json:"skipped"`
}

// MoveOf projects a completed move.
func MoveOf(r core.MoveResult) MoveView {
	return MoveView{
		Path:    r.Created.String(),
		Copied:  r.WillCopy,
		Skipped: r.Skipped,
	}
}

// CopyStartView is a copy that was accepted, not one that finished.
//
// A recursive copy runs for as long as it takes, so the answer is a job to
// poll. The id is decimal for the same reason every other id is.
type CopyStartView struct {
	// ID is absent when nothing was started, which is the skip case: there is
	// no row to poll and a client waiting on one would wait forever.
	ID   string `json:"id,omitempty"`
	Path string `json:"path"`

	Started bool `json:"started"`
	Skipped bool `json:"skipped"`
}

// CopyStartOf projects an accepted copy.
func CopyStartOf(s core.CopyStart) CopyStartView {
	v := CopyStartView{
		Path:    s.Dest.Path().String(),
		Started: s.Started,
		Skipped: s.Skipped,
	}
	if s.Started {
		v.ID = strconv.FormatInt(int64(s.ID), 10)
	}
	return v
}

// AggregateView is a directory's recursive rollup.
type AggregateView struct {
	// ETag changes when anything beneath the directory does, which is what a
	// client polls instead of walking the tree again.
	ETag string `json:"etag"`

	// Size and Count are decimal strings. A tree past 2^53 bytes loses
	// exactness as a JavaScript number, and a total that comes back wrong is
	// a quota decision made on the wrong figure.
	Size  string `json:"size"`
	Count string `json:"count"`
}

// AggregateOf projects a rollup.
func AggregateOf(a core.Aggregate) AggregateView {
	return AggregateView{
		ETag:  a.Etag,
		Size:  strconv.FormatUint(a.RSize, 10),
		Count: strconv.FormatUint(a.RCount, 10),
	}
}

// RecentView is one entry this account wrote.
type RecentView struct {
	Path string `json:"path"`
	Name string `json:"name"`

	// Op names what happened: an upload, a move, a copy. A listing that only
	// said "changed" cannot tell a restore from an overwrite.
	Op string `json:"op"`

	Size string `json:"size"`

	// AtNs is when the write happened, which is not the modification time
	// after a restore or a copy that preserved timestamps. A client asking
	// "what did I just do" needs this one.
	AtNs    string `json:"at_ns"`
	MTimeNs string `json:"mtime_ns"`
}

// RecentOf projects one hit.
func RecentOf(h core.RecentHit) RecentView {
	return RecentView{
		Path:    h.Vpath.String(),
		Name:    h.Name,
		Op:      h.Op.String(),
		Size:    strconv.FormatUint(h.Size, 10),
		AtNs:    strconv.FormatInt(h.AtNs, 10),
		MTimeNs: strconv.FormatInt(h.MTimeNs, 10),
	}
}

// RecentListOf projects a listing.
//
// Never nil: an account that has written nothing encodes as an empty array,
// because a client iterating a null gets a runtime error rather than zero rows.
func RecentListOf(hits []core.RecentHit) []RecentView {
	out := make([]RecentView, 0, len(hits))
	for _, h := range hits {
		out = append(out, RecentOf(h))
	}
	return out
}
