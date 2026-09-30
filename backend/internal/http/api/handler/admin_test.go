// Linux only, matching the package under test.
//go:build linux

package handler

import (
	"reflect"
	"testing"

	"github.com/heavycaffeiner/stowcloud/backend/internal/auth"
)

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
