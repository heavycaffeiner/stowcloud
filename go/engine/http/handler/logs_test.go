// Linux only, matching the package under test.
//go:build linux

package handler

import (
	"encoding/json"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/service/auth"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/logbook"
)

// The wire shape is exact: field names, their order and the string-typed
// numbers are the batch contract, and a dashboard built against this document
// breaks silently if any of the three drifts.
func TestALogPageMatchesTheWireContract(t *testing.T) {
	page := logbook.Page{
		Records: []logbook.Record{
			{
				TSNs:      1788490917438300000,
				Level:     "WARN",
				Msg:       "the write was refused",
				Subsystem: "dav",
				RequestID: "01J000",
				Attrs:     map[string]string{"method": "PUT", "path": "/dav/x"},
			},
		},
		Cursor: "",
	}
	stats := logbook.Stats{StoredBytes: 1234567, Segments: 4}

	raw, err := json.Marshal(LogPageOf(page, stats))
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	const want = `{"records":[{"ts_ns":"1788490917438300000","level":"WARN","msg":"the write was refused","subsystem":"dav","request_id":"01J000","attrs":{"method":"PUT","path":"/dav/x"}}],"cursor":"","stored_bytes":"1234567","segments":4}`
	if string(raw) != want {
		t.Errorf("the page encoded as:\n%s\nwant:\n%s", raw, want)
	}
}

// A timestamp and a byte count past 2^53 must round-trip exactly, which is
// the entire reason they cross as strings rather than as JSON numbers.
func TestLogNumbersCrossAsStringsPastDoublePrecision(t *testing.T) {
	const bigTs = int64(1)<<53 + 7
	const bigBytes = int64(1)<<53 + 11

	page := logbook.Page{Records: []logbook.Record{{TSNs: bigTs, Level: "INFO", Msg: "m"}}}
	stats := logbook.Stats{StoredBytes: bigBytes}

	v := LogPageOf(page, stats)
	if v.Records[0].TsNs != "9007199254740999" {
		t.Errorf("the timestamp lost exactness: %s", v.Records[0].TsNs)
	}
	if v.StoredBytes != "9007199254741003" {
		t.Errorf("the stored size lost exactness: %s", v.StoredBytes)
	}
}

// An empty page encodes its records as [] rather than null, so a client
// iterating the field does not have to test for it first.
func TestAnEmptyLogPageEncodesAnEmptyArray(t *testing.T) {
	raw, err := json.Marshal(LogPageOf(logbook.Page{}, logbook.Stats{}))
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	const want = `{"records":[],"cursor":"","stored_bytes":"0","segments":0}`
	if string(raw) != want {
		t.Errorf("an empty page encoded as %s, want %s", raw, want)
	}
}

// A record with no attributes still carries an object, not null, for the
// same reason the records list is never null.
func TestARecordWithNoAttrsEncodesAnEmptyObject(t *testing.T) {
	raw, err := json.Marshal(LogRecordOf(logbook.Record{Level: "INFO", Msg: "m"}))
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	const want = `{"ts_ns":"0","level":"INFO","msg":"m","subsystem":"","request_id":"","attrs":{}}`
	if string(raw) != want {
		t.Errorf("a record with no attrs encoded as %s, want %s", raw, want)
	}
}

// A page the walk exhausted carries no cursor, which is what a client tests
// rather than comparing the record count against a limit it may not know.
func TestAnExhaustedPageCarriesNoCursor(t *testing.T) {
	v := LogPageOf(logbook.Page{Cursor: ""}, logbook.Stats{})
	if v.Cursor != "" {
		t.Errorf("an exhausted page carries cursor %q", v.Cursor)
	}
}

// The graph's wire shape is exact for the same reason the page's is: a chart
// built against this document draws nothing at all if a field name or a
// string-typed number drifts.
func TestATimelineMatchesTheWireContract(t *testing.T) {
	server := []logbook.Bucket{
		{StartNs: 1788518100000000000, Levels: map[string]int{"INFO": 12, "WARN": 3, "DEBUG": 0}},
		{StartNs: 1788518160000000000, Levels: map[string]int{}},
	}
	audit := []auth.AuditBucket{
		{StartNs: 1788518100000000000, OK: 2, Failed: 1},
		{StartNs: 1788518160000000000},
	}

	raw, err := json.Marshal(LogsTimelineOf(server, audit, 60000000000, false))
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	const want = `{"bucket_ns":"60000000000","buckets":[` +
		`{"start_ns":"1788518100000000000","server":{"INFO":12,"WARN":3},"audit":{"failed":1,"ok":2}},` +
		`{"start_ns":"1788518160000000000","server":{},"audit":{}}` +
		`],"truncated":false}`
	if string(raw) != want {
		t.Errorf("the timeline encoded as:\n%s\nwant:\n%s", raw, want)
	}
}

// A window holding nothing is an empty array, never null: a chart iterating a
// null gets a runtime error rather than an empty axis.
func TestAnEmptyTimelineEncodesAsAnArray(t *testing.T) {
	raw, err := json.Marshal(LogsTimelineOf(nil, nil, 1000, false))
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	const want = `{"bucket_ns":"1000","buckets":[],"truncated":false}`
	if string(raw) != want {
		t.Errorf("an empty timeline encoded as %s, want %s", raw, want)
	}
}

// Either walk stopping early makes the whole graph a prefix, and the flag is
// what lets a screen say so rather than presenting a partial count as a total.
func TestTruncationSurvivesTheProjection(t *testing.T) {
	v := LogsTimelineOf([]logbook.Bucket{{StartNs: 1, Levels: map[string]int{}}}, nil, 1000, true)
	if !v.Truncated {
		t.Error("a truncated walk projected as complete")
	}
}

// The audit side is aligned by position on the frame the caller decided. A
// shorter audit slice leaves its buckets empty rather than shifting the
// counts onto the wrong intervals.
func TestAShortAuditSliceDoesNotShiftTheCounts(t *testing.T) {
	server := []logbook.Bucket{
		{StartNs: 10, Levels: map[string]int{}},
		{StartNs: 20, Levels: map[string]int{}},
	}
	v := LogsTimelineOf(server, []auth.AuditBucket{{StartNs: 10, OK: 5}}, 10, false)

	if got := v.Buckets[0].Audit["ok"]; got != 5 {
		t.Errorf("the first bucket counts %d, want the 5 the audit walk found", got)
	}
	if len(v.Buckets[1].Audit) != 0 {
		t.Errorf("the second bucket carries %v, want nothing", v.Buckets[1].Audit)
	}
}
