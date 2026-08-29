// Linux only, matching the package under test.
//go:build linux

package handler

import (
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/service/auth"
)

// Row ids and timestamps cross as strings. Paging with a rounded id skips or
// repeats a page, and an entry landing on the wrong second is one nobody can
// correlate against anything else.
func TestAuditIdsAndTimesCrossAsStrings(t *testing.T) {
	const big = int64(1)<<53 + 1
	v := AuditRowOf(auth.AuditRow{RowID: big, TsNs: 1700000000123456789})

	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	if !strings.Contains(string(raw), `"id":"9007199254740993"`) {
		t.Errorf("the row id lost exactness: %s", raw)
	}
	if !strings.Contains(string(raw), `"ts_ns":"1700000000123456789"`) {
		t.Errorf("the timestamp lost exactness: %s", raw)
	}

	var back AuditRowView
	if derr := json.Unmarshal(raw, &back); derr != nil {
		t.Fatalf("decoding: %v", derr)
	}
	got, perr := strconv.ParseInt(back.ID, 10, 64)
	if perr != nil || got != big {
		t.Errorf("the row id round-tripped to %d (%v)", got, perr)
	}
}

// A system-attributed row has no actor, which is a different thing from an
// actor whose account was deleted.
func TestASystemRowHasNoActor(t *testing.T) {
	system := AuditRowOf(auth.AuditRow{Event: "startup"})
	if system.Actor != nil || system.ActorName != nil {
		t.Errorf("a system row reports actor %v named %v", system.Actor, system.ActorName)
	}

	// An actor whose account is gone keeps the id and loses the name, so an
	// administrator can still tell two deleted accounts apart.
	id := int64(7)
	deleted := AuditRowOf(auth.AuditRow{Actor: &id, Event: "login"})
	if deleted.Actor == nil || *deleted.Actor != "7" {
		t.Errorf("a deleted actor reports %v", deleted.Actor)
	}
	if deleted.ActorName != nil {
		t.Errorf("a deleted actor reports the name %v", deleted.ActorName)
	}

	raw, err := json.Marshal(system)
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	if strings.Contains(string(raw), "actor") {
		t.Errorf("a system row encoded an actor: %s", raw)
	}
}

// An event naming nothing has no target, distinct from a target whose name is
// blank.
func TestAnEventNamingNothingHasNoTarget(t *testing.T) {
	none := AuditRowOf(auth.AuditRow{Event: "logout"})
	if none.Target != nil {
		t.Errorf("an event with no target reports %v", none.Target)
	}

	blank := ""
	named := AuditRowOf(auth.AuditRow{Event: "rename", Target: &blank})
	if named.Target == nil {
		t.Error("a blank target was dropped")
	}
}

// The outcome is a boolean, because a numeric result renders as a blank and an
// audit screen that cannot show success is not one.
func TestTheOutcomeIsAlwaysPresent(t *testing.T) {
	for _, ok := range []bool{true, false} {
		raw, err := json.Marshal(AuditRowOf(auth.AuditRow{OK: ok}))
		if err != nil {
			t.Fatalf("encoding: %v", err)
		}
		want := `"ok":` + strconv.FormatBool(ok)
		if !strings.Contains(string(raw), want) {
			t.Errorf("the outcome %v encoded as %s", ok, raw)
		}
	}
}

// The final page carries no cursor, so its absence is what a client tests
// rather than comparing the row count against a limit it may not know.
func TestTheFinalAuditPageCarriesNoCursor(t *testing.T) {
	final := AuditPageOf(nil, nil)
	raw, err := json.Marshal(final)
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	if strings.Contains(string(raw), "next") {
		t.Errorf("the final page carried a cursor: %s", raw)
	}
	if !strings.Contains(string(raw), `"rows":[]`) {
		t.Errorf("an empty page encoded as %s", raw)
	}

	cursor := int64(42)
	more := AuditPageOf([]auth.AuditRow{{Event: "login"}}, &cursor)
	if more.Next != "42" {
		t.Errorf("the cursor is %q", more.Next)
	}
}

// The service type carries no wire tags and no wire types, so a JSON format
// cannot reach back into the tier that produces the data.
//
// It had both before this projection existed: json tags on every field and a
// timestamp already formatted as a string. A service type shaped that way has
// fields that cannot be renamed without breaking a client the package does
// not know exists.
func TestTheServiceAuditRowHasNoWireShape(t *testing.T) {
	rt := reflect.TypeOf(auth.AuditRow{})
	for i := range rt.NumField() {
		f := rt.Field(i)
		if _, tagged := f.Tag.Lookup("json"); tagged {
			t.Errorf("auth.AuditRow.%s carries a json tag", f.Name)
		}
	}

	// The timestamp is a number. As a string the service would be formatting
	// for a wire it does not own, which is this projection's job.
	ts, ok := rt.FieldByName("TsNs")
	if !ok {
		t.Fatal("auth.AuditRow has no TsNs field")
	}
	if ts.Type.Kind() != reflect.Int64 {
		t.Errorf("the service timestamp is a %s", ts.Type)
	}
}
