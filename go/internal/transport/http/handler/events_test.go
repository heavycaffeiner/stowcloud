// Linux only, matching the package under test.
//go:build linux

package handler

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// A frame is one frame. A newline in the payload would end it early and the
// rest would arrive as a second, malformed event.
func TestAnSSEFrameCannotSplitInTwo(t *testing.T) {
	got, err := SSEFrame(SSEHit, map[string]string{"path": "a\nb: injected"})
	if err != nil {
		t.Fatalf("SSEFrame: %v", err)
	}
	// The encoder escapes the newline, so the frame has exactly the two the
	// format calls for at its end and none inside the data line.
	body := strings.TrimSuffix(got, "\n\n")
	if strings.Count(body, "\n") != 1 {
		t.Errorf("the frame carries extra line breaks: %q", got)
	}
	if !strings.HasPrefix(got, "event: hit\ndata: ") {
		t.Errorf("the frame shape is %q", got)
	}

	// An event name that is not a token is refused, since it sits before a
	// colon in the wire format.
	if _, nerr := SSEFrame("hit\ndata: injected", nil); !errors.Is(nerr, ErrInvalid) {
		t.Errorf("an event name with a break returned %v", nerr)
	}
	if _, cerr := SSEFrame("hit: extra", nil); !errors.Is(cerr, ErrInvalid) {
		t.Errorf("an event name with a colon returned %v", cerr)
	}
}

// The stream opens with a comment, so the client and any proxy see it
// established rather than waiting on a first result seconds away.
func TestTheStreamOpensImmediately(t *testing.T) {
	if !strings.HasPrefix(SSEComment(), ":") {
		t.Errorf("the opening frame is not a comment: %q", SSEComment())
	}
	if !strings.HasSuffix(SSEComment(), "\n\n") {
		t.Errorf("the opening frame is not terminated: %q", SSEComment())
	}
}

// A failure after the stream is committed arrives as a done event, because the
// status line went out long before.
func TestAPostCommitmentFailureEndsTheStream(t *testing.T) {
	got, err := SSEFrame(SSEDone, SSEDoneView{Error: "search.unavailable"})
	if err != nil {
		t.Fatalf("SSEFrame: %v", err)
	}
	if !strings.Contains(got, `"error":"search.unavailable"`) {
		t.Errorf("the done frame is %q", got)
	}

	// An ordinary end carries no error field at all, so a client testing for
	// its presence is not comparing against an empty string.
	done, derr := SSEFrame(SSEDone, SSEDoneView{Tier: "index"})
	if derr != nil {
		t.Fatalf("SSEFrame: %v", derr)
	}
	if strings.Contains(done, "error") {
		t.Errorf("a clean end carried an error: %q", done)
	}
}

// A websocket frame carries no content, no etag and no metadata. The client
// re-fetches, which is what re-applies permission at delivery time rather than
// at subscribe time.
func TestAWebSocketFrameCarriesNoContent(t *testing.T) {
	rt := reflect.TypeOf(WSFrame{})
	for i := range rt.NumField() {
		name := strings.ToLower(rt.Field(i).Name)
		for _, banned := range []string{"content", "body", "etag", "data", "size", "token"} {
			if strings.Contains(name, banned) {
				t.Errorf("WSFrame carries the field %s", rt.Field(i).Name)
			}
		}
	}

	raw, err := json.Marshal(InvalidationFrame("docs/reports"))
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	if string(raw) != `{"t":"inval","path":"docs/reports"}` {
		t.Errorf("an invalidation encoded as %s", raw)
	}
}

// The bound is applied before the decode, so a frame naming a hundred thousand
// paths does not cost a resolve each before being refused.
func TestFramesAreBoundedBeforeTheyAreDecoded(t *testing.T) {
	huge := `{"t":"sub","paths":["` + strings.Repeat("a", 4096) + `"]}`
	if _, err := ParseWSFrame([]byte(huge), 1024, 100); !errors.Is(err, ErrInvalid) {
		t.Errorf("an oversized frame returned %v", err)
	}

	var paths []string
	for range 200 {
		paths = append(paths, "docs")
	}
	body, err := json.Marshal(WSFrame{Type: WSSubscribe, Paths: paths})
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	if _, perr := ParseWSFrame(body, 1<<20, 100); !errors.Is(perr, ErrInvalid) {
		t.Errorf("a frame past the path bound returned %v", perr)
	}
	// At the bound it is accepted: the refusal is for crossing it.
	if _, aerr := ParseWSFrame(body, 1<<20, 200); aerr != nil {
		t.Errorf("a frame at the path bound returned %v", aerr)
	}
}

// A client cannot send an invalidation. That would be asking the server to
// tell other clients something changed.
func TestAClientCannotSendAnInvalidation(t *testing.T) {
	raw, err := json.Marshal(InvalidationFrame("docs"))
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	_, perr := ParseWSFrame(raw, 1<<20, 100)
	if !errors.Is(perr, ErrInvalid) {
		t.Fatalf("a client invalidation returned %v", perr)
	}
	if !strings.Contains(perr.Error(), "does not send") {
		t.Errorf("the refusal says %q", perr)
	}
}

// The ordinary frames parse, and malformed ones are refused rather than
// treated as something.
func TestTheClientFramesParse(t *testing.T) {
	for _, body := range []string{
		`{"t":"sub","paths":["docs"]}`,
		`{"t":"unsub","paths":["docs","photos"]}`,
		`{"t":"ping"}`,
		`{"t":"pong"}`,
	} {
		if _, err := ParseWSFrame([]byte(body), 1<<20, 100); err != nil {
			t.Errorf("%s returned %v", body, err)
		}
	}

	for _, c := range []struct{ what, body string }{
		{"an unknown type", `{"t":"whatever"}`},
		{"no type at all", `{"paths":["docs"]}`},
		{"a subscribe with no paths", `{"t":"sub","paths":[]}`},
		{"a ping carrying a path", `{"t":"ping","paths":["docs"]}`},
		{"an unknown field", `{"t":"ping","extra":1}`},
		{"not an object", `["sub"]`},
		{"nothing", ``},
	} {
		if _, err := ParseWSFrame([]byte(c.body), 1<<20, 100); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s (%s) returned %v", c.what, c.body, err)
		}
	}
}
