// Linux only, for the same reason as the rest of this package.
//go:build linux

// The log dashboard's projection.
//
// A record's timestamp and the store's byte count are decimal strings, for
// the reason every other large number on this wire is: a value past 2^53
// loses exactness as a JavaScript number, and a log timestamp or a byte count
// that comes back wrong is a filter that silently misses what it was asked
// to find.
package handler

import (
	"strconv"

	"github.com/heavycaffeiner/stowcloud/go/engine/service/auth"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/logbook"
)

// LogRecordView is one entry, as the dashboard reads it.
type LogRecordView struct {
	TsNs      string            `json:"ts_ns"`
	Level     string            `json:"level"`
	Msg       string            `json:"msg"`
	Subsystem string            `json:"subsystem"`
	RequestID string            `json:"request_id"`
	Attrs     map[string]string `json:"attrs"`
}

// LogRecordOf projects one record.
//
// Attrs is never nil: a client reading it as a map gets an empty one rather
// than a decode failure, on a record that happened to carry nothing beyond
// the fixed fields.
func LogRecordOf(r logbook.Record) LogRecordView {
	attrs := r.Attrs
	if attrs == nil {
		attrs = map[string]string{}
	}
	return LogRecordView{
		TsNs:      strconv.FormatInt(r.TSNs, 10),
		Level:     r.Level,
		Msg:       r.Msg,
		Subsystem: r.Subsystem,
		RequestID: r.RequestID,
		Attrs:     attrs,
	}
}

// LogPageView is one page of the log, plus what the store is holding.
type LogPageView struct {
	Records []LogRecordView `json:"records"`

	// Cursor is empty when the walk exhausted the store, which is what a
	// client tests to know it has read everything matching its filter.
	Cursor string `json:"cursor"`

	// StoredBytes and Segments describe the store as a whole rather than the
	// page, so a dashboard can show them beside a result without a second
	// request.
	StoredBytes string `json:"stored_bytes"`
	Segments    int    `json:"segments"`
}

// LogPageOf projects one page.
//
// Records is never nil: an empty result encodes as an empty array, because a
// client iterating a null gets a runtime error rather than zero rows.
func LogPageOf(p logbook.Page, stats logbook.Stats) LogPageView {
	out := LogPageView{
		Records:     make([]LogRecordView, 0, len(p.Records)),
		Cursor:      p.Cursor,
		StoredBytes: strconv.FormatInt(stats.StoredBytes, 10),
		Segments:    stats.Segments,
	}
	for _, r := range p.Records {
		out.Records = append(out.Records, LogRecordOf(r))
	}
	return out
}

// LogsTimelineBucketView is one interval of the graph above the log list.
//
// Two maps rather than one flat set of keys: the server log is counted by
// level and the audit log by outcome, and folding them together would make a
// level called "ok" collide with an outcome.
//
// A key a bucket had none of is omitted rather than sent as zero. The
// renderer already has to handle a series the whole window lacks, and sending
// every level for every bucket triples the document for no information.
type LogsTimelineBucketView struct {
	// StartNs is a decimal string: a nanosecond timestamp loses exactness as
	// a JavaScript number, which is why every other one on this path is too.
	StartNs string `json:"start_ns"`

	Server map[string]int `json:"server"`
	Audit  map[string]int `json:"audit"`
}

// LogsTimelineView is the graph's whole answer.
type LogsTimelineView struct {
	// BucketNs is the interval width the server settled on, which is not
	// always the one asked for: a window divided into more bars than a chart
	// can carry gets a width that fits.
	BucketNs string `json:"bucket_ns"`

	// Buckets is oldest first, because a chart draws left to right, and
	// covers the window with no holes: an empty interval is still an
	// interval, so a renderer never has to tell a gap from a zero.
	Buckets []LogsTimelineBucketView `json:"buckets"`

	// Truncated says a walk stopped on its ceiling, so the graph is a prefix
	// of the window rather than the whole of it. A screen that drew it
	// without saying so would be presenting a partial count as a total.
	Truncated bool `json:"truncated"`
}

// LogsTimelineOf merges the two walks into the graph's document.
//
// The two sides are counted separately and aligned here, on the frame the
// caller decided: same start, same width, same count. Aligning anywhere else
// would let the bars disagree about which hour they are.
func LogsTimelineOf(
	server []logbook.Bucket, audit []auth.AuditBucket, widthNs int64, truncated bool,
) LogsTimelineView {
	out := LogsTimelineView{
		BucketNs:  strconv.FormatInt(widthNs, 10),
		Buckets:   make([]LogsTimelineBucketView, 0, len(server)),
		Truncated: truncated,
	}
	for i, b := range server {
		bucket := LogsTimelineBucketView{
			StartNs: strconv.FormatInt(b.StartNs, 10),
			Server:  map[string]int{},
			Audit:   map[string]int{},
		}
		for level, n := range b.Levels {
			if n > 0 {
				bucket.Server[level] = n
			}
		}
		if i < len(audit) {
			if audit[i].OK > 0 {
				bucket.Audit["ok"] = audit[i].OK
			}
			if audit[i].Failed > 0 {
				bucket.Audit["failed"] = audit[i].Failed
			}
		}
		out.Buckets = append(out.Buckets, bucket)
	}
	return out
}
