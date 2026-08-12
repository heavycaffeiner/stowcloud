package limits

import (
	"errors"
	"strings"
	"testing"
)

func TestExceedMatchesTheSentinel(t *testing.T) {
	err := Exceed("directory entries, buffered read", DirEntriesBuffered, DirEntriesBuffered+1)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
}

// A refusal that does not say which bound refused is one an operator cannot
// act on, which is why the limit is a field and not just a message.
func TestExceedNamesTheLimit(t *testing.T) {
	err := Exceed("request body, XML", RequestBodyXML, RequestBodyXML+1)
	var e *Exceeded
	if !errors.As(err, &e) {
		t.Fatalf("err = %v, want *Exceeded", err)
	}
	if e.Limit != "request body, XML" || e.Bound != RequestBodyXML {
		t.Fatalf("Exceeded = %+v, want the named limit and its bound", e)
	}
	if !strings.Contains(err.Error(), "request body, XML") {
		t.Fatalf("Error() = %q, want the limit named", err.Error())
	}
}
