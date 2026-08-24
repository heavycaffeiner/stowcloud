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

// D5 asks each bound to be a named constant with a test proving what it is.
// These are the values themselves: a bound that silently changes is a bound
// nobody agreed to, and the tests that prove a bound is what refuses live with
// the code that enforces it.

func TestTheWebDavBoundsAreWhatWasAgreed(t *testing.T) {
	for _, tc := range []struct {
		name string
		got  int
		want int
	}{
		{"DavElements", DavElements, 10_000},
		{"DavDepth", DavDepth, 64},
		{"DavNameLength", DavNameLength, 256},
		{"DavTextBytes", DavTextBytes, 64 << 10},
		{"DavLocksPerUser", DavLocksPerUser, 256},
		{"DavPropsPerResource", DavPropsPerResource, 256},
		{"DavInfinityEntries", DavInfinityEntries, 100_000},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %d, want %d", tc.name, tc.got, tc.want)
		}
	}
}

func TestTheUploadBoundsAreWhatWasAgreed(t *testing.T) {
	for _, tc := range []struct {
		name string
		got  int64
		want int64
	}{
		{"UploadIntervalRuns", UploadIntervalRuns, 4096},
		{"UploadSpooledNames", UploadSpooledNames, 4096},
		{"UploadChunkFloor", UploadChunkFloor, 5 << 20},
		{"UploadReservedBytesPerUser", UploadReservedBytesPerUser, 100 << 30},
		{"UploadFreeSpaceMargin", UploadFreeSpaceMargin, 2 << 30},
		{"UploadsInFlightPerUser", UploadsInFlightPerUser, 32},
		{"UploadSessionsPerUser", UploadSessionsPerUser, 256},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %d, want %d", tc.name, tc.got, tc.want)
		}
	}
}

// The XML body cap is smaller than the general one. An XML body is parsed
// rather than streamed to disk, so it is the more expensive byte.
func TestTheXMLBodyCapIsTighterThanTheGeneralOne(t *testing.T) {
	if RequestBodyXML >= RequestBody {
		t.Fatalf("RequestBodyXML = %d is not tighter than RequestBody = %d",
			RequestBodyXML, RequestBody)
	}
}
