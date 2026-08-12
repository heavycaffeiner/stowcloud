package apierr

import (
	"encoding/json"
	"testing"
)

func TestErrorRendersTheKeyNotASentence(t *testing.T) {
	e := NewError("not_found", "error.not_found", Arg{Name: "path", Value: "a/b"})
	if got, want := e.Error(), "not_found: error.not_found path=a/b"; got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}

func TestMarshalledShape(t *testing.T) {
	e := NewError("too_large", "error.too_large", Arg{Name: "limit", Value: "1048576"})
	e.TraceID = "t-1"
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"code":"too_large","msg":"error.too_large",` +
		`"args":[{"name":"limit","value":"1048576"}],"trace":"t-1"}`
	if string(b) != want {
		t.Fatalf("marshalled = %s\nwant       = %s", b, want)
	}
}

func TestArgsAreOmittedWhenEmpty(t *testing.T) {
	b, err := json.Marshal(NewError("denied", "error.denied"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"code":"denied","msg":"error.denied","trace":""}` {
		t.Fatalf("marshalled = %s", b)
	}
}
