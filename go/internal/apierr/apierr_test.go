package apierr

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestErrorRendersTheKeyNotASentence(t *testing.T) {
	e := NewError("fs.not_found", "not found", "fs.no_such_path", Arg{Name: "path", Value: "a/b"})
	if got, want := e.Error(), "fs.not_found: not found fs.no_such_path path=a/b"; got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}

// The wire shape, exactly. The outer error object, the three field names, and
// nothing from the type that is not one of them.
func TestMarshalledShape(t *testing.T) {
	e := NewError("fs.invalid_name", "invalid name", "share.name_empty", Arg{Name: "field", Value: "name"})
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"error":{"code":"fs.invalid_name","message":"invalid name",` +
		`"detail":{"reason_key":"share.name_empty","reason_params":{"field":"name"}}}}`
	if string(b) != want {
		t.Fatalf("marshalled = %s\nwant       = %s", b, want)
	}
}

// The fields the Phase 0 shape carried and this one does not. A client that
// still reads them would read them from something else's response.
func TestTheOldFieldNamesAreGone(t *testing.T) {
	b, err := json.Marshal(NewError("fs.denied", "permission denied", "acl.denied"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{`"trace"`, `"msg"`, `"args"`} {
		if strings.Contains(string(b), name) {
			t.Errorf("%s is still on the wire: %s", name, b)
		}
	}
}

// No catalogue key is the ordinary case for a code the client already phrases,
// and an absent detail is absent rather than null or empty.
func TestDetailIsOmittedWithoutAKey(t *testing.T) {
	b, err := json.Marshal(NewError("fs.denied", "permission denied", ""))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"error":{"code":"fs.denied","message":"permission denied"}}` {
		t.Fatalf("marshalled = %s", b)
	}
}

// A 500 says what went wrong to the log and nothing to the client. Attaching a
// key and arguments to one has to change nothing on the wire, because the
// classification and the detail are set in different places and the later one
// wins.
func TestAnInternalErrorCannotCarryDetail(t *testing.T) {
	e := Internal()
	e.Key = "fs.disk_path"
	e.Args = []Arg{{Name: "path", Value: "/srv/shares/private/secret"}}

	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"error":{"code":"internal","message":"internal error"}}` {
		t.Fatalf("an internal error serialized detail: %s", b)
	}
}
