package secret

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

const rawValue = "correct-horse-battery-staple"

func TestFormatVerbsAllRedact(t *testing.T) {
	s := New([]byte(rawValue))
	verbs := []string{"%v", "%+v", "%s", "%q", "%x", "%X", "%#v"}
	for _, verb := range verbs {
		rendered := fmt.Sprintf(verb, s)
		if strings.Contains(rendered, rawValue) {
			t.Errorf("verb %s leaked the secret: %q", verb, rendered)
		}
	}
}

func TestPointerReceiverAlsoRedacts(t *testing.T) {
	s := New([]byte(rawValue))
	rendered := fmt.Sprintf("%v", &s)
	if strings.Contains(rendered, rawValue) {
		t.Errorf("*Secret leaked through %%v: %q", rendered)
	}
}

func TestMarshalJSONRedacts(t *testing.T) {
	type wrapper struct {
		Key Secret `json:"key"`
	}
	out, err := json.Marshal(wrapper{Key: New([]byte(rawValue))})
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}
	if strings.Contains(string(out), rawValue) {
		t.Errorf("JSON leaked the secret: %s", out)
	}
	if !strings.Contains(string(out), "[redacted]") {
		t.Errorf("JSON does not contain the redacted marker: %s", out)
	}
}

func TestEqualMatchesIdenticalBytes(t *testing.T) {
	a := New([]byte("shared-value"))
	b := New([]byte("shared-value"))
	if !a.Equal(b) {
		t.Error("Equal returned false for identical byte content")
	}
}

func TestEqualRejectsDifferentBytes(t *testing.T) {
	a := New([]byte("value-one"))
	b := New([]byte("value-two"))
	if a.Equal(b) {
		t.Error("Equal returned true for different byte content")
	}
}

func TestEqualRejectsDifferentLengths(t *testing.T) {
	a := New([]byte("short"))
	b := New([]byte("much longer value"))
	if a.Equal(b) {
		t.Error("Equal returned true for differing lengths")
	}
}

func TestLenReflectsUnderlyingBytes(t *testing.T) {
	s := New([]byte("abcde"))
	if s.Len() != 5 {
		t.Errorf("Len() = %d, want 5", s.Len())
	}
}

func TestRevealAliasesTheUnderlyingBuffer(t *testing.T) {
	buf := []byte(rawValue)
	s := New(buf)
	if string(s.Reveal()) != rawValue {
		t.Fatalf("Reveal() = %q, want %q", s.Reveal(), rawValue)
	}
	buf[0] = 'X'
	if s.Reveal()[0] != 'X' {
		t.Error("Reveal() did not alias the original buffer")
	}
}

func TestDestroyZeroesAndClearsReveal(t *testing.T) {
	buf := []byte(rawValue)
	s := New(buf)
	s.Destroy()
	for i, b := range buf {
		if b != 0 {
			t.Fatalf("byte %d of the owned buffer was not zeroed: %v", i, buf)
		}
	}
	if s.Reveal() != nil {
		t.Errorf("Reveal() after Destroy = %v, want nil", s.Reveal())
	}
	if s.Len() != 0 {
		t.Errorf("Len() after Destroy = %d, want 0", s.Len())
	}
}
