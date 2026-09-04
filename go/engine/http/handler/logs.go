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
