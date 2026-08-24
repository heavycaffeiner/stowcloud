package secret

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

const plaintext = "hunter2"

// Every verb that routes through a method has to redact, including %#v, which
// ignores String and would otherwise print the struct and its bytes.
func TestEveryRenderingRedacts(t *testing.T) {
	s := New([]byte(plaintext))
	for _, verb := range []string{"%v", "%s", "%q", "%x", "%X", "%#v"} {
		out := fmt.Sprintf(verb, s)
		if strings.Contains(out, plaintext) {
			t.Errorf("%s rendered the plaintext: %s", verb, out)
		}
	}
}

func TestMarshalJSONRedacts(t *testing.T) {
	b, err := json.Marshal(struct {
		Password Secret `json:"password"`
	}{New([]byte(plaintext))})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), plaintext) {
		t.Fatalf("marshalled the plaintext: %s", b)
	}
}

func TestEqual(t *testing.T) {
	a, b := New([]byte(plaintext)), New([]byte(plaintext))
	if !a.Equal(b) {
		t.Error("identical secrets compared unequal")
	}
	if a.Equal(New([]byte("hunter3"))) {
		t.Error("different secrets compared equal")
	}
	if a.Equal(New(nil)) {
		t.Error("a secret compared equal to an empty one")
	}
}

func TestDestroyZeroesTheBuffer(t *testing.T) {
	buf := []byte(plaintext)
	s := New(buf)
	s.Destroy()
	for i, c := range buf {
		if c != 0 {
			t.Fatalf("byte %d survived Destroy: %q", i, c)
		}
	}
	if s.Len() != 0 {
		t.Fatalf("Len after Destroy = %d, want 0", s.Len())
	}
}
