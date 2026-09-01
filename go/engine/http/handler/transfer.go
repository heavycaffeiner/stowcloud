//go:build linux

// What a transfer, a rollup and a recent listing look like on the wire.
package handler

import (
	"strconv"

	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/preview"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/search"
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

// ArchiveListingView is what is inside a zip, read from its own directory.
type ArchiveListingView struct {
	Entries []ArchiveEntryView `json:"entries"`

	// Truncated marks a listing that stopped at the ceiling. Without it a
	// client shows a partial archive as though it were the whole one.
	Truncated bool `json:"truncated,omitempty"`

	// Skipped counts members whose names were refused. They exist in the
	// archive and are not shown, which is different from not being there.
	Skipped int `json:"skipped,omitempty"`

	// TotalUncompressed is what extraction would cost, as a decimal string.
	// A caller weighs it against the file's own size to spot a bomb before
	// extracting anything.
	TotalUncompressed string `json:"total_uncompressed"`
}

// ArchiveEntryView is one member.
type ArchiveEntryView struct {
	Name  string `json:"name"`
	IsDir bool   `json:"is_dir"`

	Size       string `json:"size"`
	Compressed string `json:"compressed"`

	ModTimeNs string `json:"mtime_ns,omitempty"`
}

// ArchiveListingOf projects a parsed directory.
//
// Entries are never nil: an empty archive encodes as an empty array, because
// a client iterating a null gets a runtime error rather than zero members.
func ArchiveListingOf(l preview.ArchiveListing) ArchiveListingView {
	out := ArchiveListingView{
		Entries:           make([]ArchiveEntryView, 0, len(l.Entries)),
		Truncated:         l.Truncated,
		Skipped:           l.Skipped,
		TotalUncompressed: strconv.FormatUint(l.TotalUncompressed, 10),
	}
	for _, e := range l.Entries {
		v := ArchiveEntryView{
			Name:       e.Name,
			IsDir:      e.IsDir,
			Size:       strconv.FormatUint(e.Size, 10),
			Compressed: strconv.FormatUint(e.Compressed, 10),
		}
		if e.ModTimeNs != 0 {
			v.ModTimeNs = strconv.FormatInt(e.ModTimeNs, 10)
		}
		out.Entries = append(out.Entries, v)
	}
	return out
}

// ArchiveTicketView points a client at a prepared archive.
//
// The archive is built and held before this is answered, so the size is the
// real one: the client knows what it is about to download, and the fetch it
// follows with carries a Content-Length and answers ranges.
type ArchiveTicketView struct {
	Token string `json:"token"`
	Name  string `json:"name"`

	// Size is the built archive's length in bytes. A number rather than a
	// decimal string: it is bounded by what this server will hold in memory,
	// far inside what a JavaScript number holds exactly.
	Size int64 `json:"size"`

	// URL is where to get it, absolute from the site root. Built by the
	// server so a client does not assemble the route itself and get it wrong.
	URL string `json:"url"`
}

// IndexEstimateView is what building a name index would cost.
type IndexEstimateView struct {
	// IndexBytes is the estimate, as a decimal string: a large corpus runs
	// past 2^53 and the figure an operator is deciding on would round.
	IndexBytes string `json:"index_bytes"`

	// Confidence says how much to trust the number. An estimate presented
	// without it invites an operator to plan against a figure the estimator
	// itself is unsure of.
	Confidence string `json:"confidence"`

	// Formula records the derivation term by term, so a wrong estimate shows
	// which term was wrong to somebody checking the arithmetic.
	Formula string `json:"formula"`

	// Files and NameBytes are what was measured.
	Files     string `json:"files"`
	NameBytes string `json:"name_bytes"`

	// Partial marks a scan that hit its bound, so the figures describe a
	// sample. Presenting a fraction as the whole is how an index is sized at
	// a tenth of what it needs.
	Partial bool `json:"partial"`
}

// IndexEstimateOf projects a scan and its estimate.
func IndexEstimateOf(r search.ScanResult, e search.IndexEstimate) IndexEstimateView {
	return IndexEstimateView{
		IndexBytes: strconv.FormatUint(e.IndexBytes, 10),
		Confidence: e.Confidence.String(),
		Formula:    e.Formula,
		Files:      strconv.FormatUint(r.Stats.Files, 10),
		NameBytes:  strconv.FormatUint(r.Stats.NameBytesTotal, 10),
		Partial:    r.Partial,
	}
}
