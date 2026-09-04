// Linux only, matching the package under test.
//go:build linux

package handler

import (
	"encoding/json"
	"testing"

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
