//go:build linux && compat_nc

package lifecycle_test

import (
	"fmt"
	"net/http"
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
