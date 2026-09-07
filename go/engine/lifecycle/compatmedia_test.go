//go:build linux && compat_nc

package lifecycle_test

import (
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// mediaSearch is the body the photo tab sends: the image-or-video disjunction
// ANDed with a modification-time window, and the row count it wants back.
func mediaSearch(sinceNs, beforeNs int64, rows int) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<d:searchrequest xmlns:d="DAV:" xmlns:oc="http://nextcloud.com/ns">
 <d:basicsearch>
  <d:select><d:prop><d:getetag/></d:prop></d:select>
  <d:from><d:scope><d:href>/files/alice</d:href><d:depth>infinity</d:depth></d:scope></d:from>
  <d:where>
   <d:and>
    <d:or>
     <d:like><d:prop><d:getcontenttype/></d:prop><d:literal>image/%%</d:literal></d:like>
     <d:like><d:prop><d:getcontenttype/></d:prop><d:literal>video/%%</d:literal></d:like>
    </d:or>
    <d:and>
     <d:lt><d:prop><d:getlastmodified/></d:prop><d:literal>%d</d:literal></d:lt>
     <d:gt><d:prop><d:getlastmodified/></d:prop><d:literal>%d</d:literal></d:gt>
    </d:and>
   </d:and>
  </d:where>
  <d:limit><d:nresults>%d</d:nresults></d:limit>
 </d:basicsearch>
</d:searchrequest>`, beforeNs, sinceNs, rows)
}

// mediaSearchWithMtime is the same body asking for the timestamp back, which
// is what the client pages on.
func mediaSearchWithMtime(sinceNs, beforeNs int64, rows int) string {
	return strings.Replace(mediaSearch(sinceNs, beforeNs, rows),
		"<d:prop><d:getetag/></d:prop>",
		"<d:prop><d:getetag/><d:getlastmodified/></d:prop>", 1)
}

// The photo tab gets back no more rows than it asked for.
//
// It pages by narrowing the window to the oldest row it received and asking
// again, so a listing that ignores the count hands back the whole library on
// every scroll and the screen never reaches the end.
func TestMediaSearchHonoursTheRequestedRowCount(t *testing.T) {
	t.Parallel()
	base, credential, client := uploadFixture(t)

	const written = 6
	for i := range written {
		url := fmt.Sprintf("%s/remote.php/dav/files/alice/documents/photo-%d.jpg", base, i)
		if code, body := davSend(t, client, credential, http.MethodPut, url,
			strings.NewReader("jpeg-bytes"), nil); code != http.StatusCreated {
			t.Fatalf("writing photo %d answered %d: %s", i, code, body)
		}
	}

	// A window wide enough to hold every file, so the count is what bounds
	// the answer rather than the range.
	const wantRows = 2
	code, out := davSend(t, client, credential, "SEARCH", base+"/remote.php/dav/files/alice",
		strings.NewReader(mediaSearch(1, 1<<62, wantRows)),
		map[string]string{"Content-Type": "text/xml"})
	if code != http.StatusMultiStatus {
		t.Fatalf("the media search answered %d: %s", code, out)
	}
	if got := strings.Count(out, "<D:response>"); got != wantRows {
		t.Errorf("the media search reported %d rows for a request of %d", got, wantRows)
	}
}

// A window that ends before every file answers with none of them.
//
// The upper bound is how the tab asks for the next page. Ignoring it repeats
// rows the client already has, which is what makes the scroll never finish.
func TestMediaSearchHonoursTheWindowsUpperBound(t *testing.T) {
	t.Parallel()
	base, credential, client := uploadFixture(t)

	for i := range 3 {
		url := fmt.Sprintf("%s/remote.php/dav/files/alice/documents/clip-%d.mp4", base, i)
		if code, body := davSend(t, client, credential, http.MethodPut, url,
			strings.NewReader("mp4-bytes"), nil); code != http.StatusCreated {
			t.Fatalf("writing clip %d answered %d: %s", i, code, body)
		}
	}

	// Everything was written just now, so a window closing in 2001 holds none
	// of it. Seconds on the wire, as the client sends them.
	code, out := davSend(t, client, credential, "SEARCH", base+"/remote.php/dav/files/alice",
		strings.NewReader(mediaSearch(1, 1_000_000_000, 100)),
		map[string]string{"Content-Type": "text/xml"})
	if code != http.StatusMultiStatus {
		t.Fatalf("the media search answered %d: %s", code, out)
	}
	if got := strings.Count(out, "<D:response>"); got != 0 {
		t.Errorf("a window ending in the past reported %d rows: %s", got, out)
	}
}

// A truncated media answer is the newest rows, in order.
//
// The client pages by narrowing the window to the timestamp of the oldest row
// it holds, so a truncated answer has to be the newest rows and has to arrive
// newest first. Truncating an unordered set hands back an arbitrary subset,
// and the next request, keyed on that subset's oldest member, never names the
// rows that were dropped.
func TestATruncatedMediaAnswerIsTheNewestRowsInOrder(t *testing.T) {
	t.Parallel()
	base, credential, client := uploadFixture(t)
	// Distinct extensions so the answer cannot come out ordered by accident of
	// the per-extension index walk, and a day between each so "newest" has one
	// answer. Declared rather than slept for: the wire carries whole seconds,
	// and waiting out six of them would be the slowest test here.
	names := []string{"a.jpg", "b.png", "c.mp4", "d.jpg", "e.png", "f.mp4"}
	for i, name := range names {
		url := base + "/remote.php/dav/files/alice/documents/" + name
		stamp := strconv.Itoa(1_600_000_000 + i*86_400)
		if code, body := davSend(t, client, credential, http.MethodPut, url,
			strings.NewReader(name),
			map[string]string{"X-OC-Mtime": stamp}); code != http.StatusCreated {
			t.Fatalf("writing %s answered %d: %s", name, code, body)
		}
	}

	const perPage = 3
	code, out := davSend(t, client, credential, "SEARCH",
		base+"/remote.php/dav/files/alice",
		strings.NewReader(mediaSearchWithMtime(1, 1<<62, perPage)),
		map[string]string{"Content-Type": "text/xml"})
	if code != http.StatusMultiStatus {
		t.Fatalf("the media search answered %d: %s", code, out)
	}

	stamps := regexp.MustCompile(`<D:getlastmodified>([^<]*)</D:getlastmodified>`).
		FindAllStringSubmatch(out, -1)
	if len(stamps) != perPage {
		t.Fatalf("asked for %d rows and got %d: %s", perPage, len(stamps), out)
	}
	prev := int64(1) << 62
	for i, s := range stamps {
		when, perr := http.ParseTime(s[1])
		if perr != nil {
			t.Fatalf("row %d carries an unparsable timestamp %q: %v", i, s[1], perr)
		}
		if when.Unix() > prev {
			t.Errorf("row %d is newer than the one before it, so the answer is not ordered", i)
		}
		prev = when.Unix()
	}

	// The three newest were written last, so the three oldest must be absent.
	for _, gone := range names[:3] {
		if strings.Contains(out, gone) {
			t.Errorf("%s is older than the rows that fit and was returned anyway", gone)
		}
	}
}
