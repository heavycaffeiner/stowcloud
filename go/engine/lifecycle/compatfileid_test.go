//go:build linux && compat_nc

package lifecycle_test

import (
	"net/http"
	"regexp"
	"strings"
	"testing"
)

// fileIDSearch is the body the reference client sends to open a file it knows
// only by id: a notification, a deep link, or a transfer it is resuming.
func fileIDSearch(id string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<d:searchrequest xmlns:d="DAV:" xmlns:oc="http://nextcloud.com/ns">
 <d:basicsearch>
  <d:select><d:prop><d:getetag/></d:prop></d:select>
  <d:from><d:scope><d:href>/files/alice</d:href><d:depth>infinity</d:depth></d:scope></d:from>
  <d:where><d:eq><d:prop><oc:fileid/></d:prop><d:literal>` + id + `</d:literal></d:eq></d:where>
 </d:basicsearch>
</d:searchrequest>`
}

// A lookup by file id answers with that file and nothing else.
//
// The client takes the first row without checking which file it names, so a
// listing of anything else is not an empty result: it is the wrong file opened
// under the name the person tapped. An id naming nothing has to answer with no
// rows rather than fall back to a listing.
func TestSearchingByFileIDAnswersThatFileAlone(t *testing.T) {
	t.Parallel()
	base, credential, client := uploadFixture(t)

	// Written last so the newest-first fallback would put a different file
	// first, which is what made the wrong answer look like a plausible one.
	target := base + "/remote.php/dav/files/alice/documents/target.txt"
	for _, name := range []string{"target.txt", "noise-1.txt", "noise-2.txt"} {
		url := base + "/remote.php/dav/files/alice/documents/" + name
		if code, body := davSend(t, client, credential, http.MethodPut, url,
			strings.NewReader(name), nil); code != http.StatusCreated {
			t.Fatalf("writing %s answered %d: %s", name, code, body)
		}
	}

	ask := `<?xml version="1.0"?><d:propfind xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns"><d:prop><oc:fileid/></d:prop></d:propfind>`
	code, listed := davSend(t, client, credential, "PROPFIND", target, strings.NewReader(ask),
		map[string]string{"Depth": "0", "Content-Type": "text/xml"})
	if code != http.StatusMultiStatus {
		t.Fatalf("reading the id answered %d: %s", code, listed)
	}
	found := regexp.MustCompile(`<[^>]*fileid[^>]*>(\d+)<`).FindStringSubmatch(listed)
	if found == nil {
		t.Fatalf("the listing carried no file id: %s", listed)
	}

	code, out := davSend(t, client, credential, "SEARCH", base+"/remote.php/dav/files/alice",
		strings.NewReader(fileIDSearch(found[1])), map[string]string{"Content-Type": "text/xml"})
	if code != http.StatusMultiStatus {
		t.Fatalf("the lookup answered %d: %s", code, out)
	}
	if got := strings.Count(out, "<D:response>"); got != 1 {
		t.Fatalf("the lookup reported %d files, want exactly 1: %s", got, out)
	}
	if !strings.Contains(out, "target.txt") {
		t.Errorf("the lookup named a different file than the id it was given: %s", out)
	}

	// An id nobody holds is not an invitation to list something else.
	code, missing := davSend(t, client, credential, "SEARCH", base+"/remote.php/dav/files/alice",
		strings.NewReader(fileIDSearch("999999999999")), map[string]string{"Content-Type": "text/xml"})
	if code != http.StatusMultiStatus {
		t.Fatalf("the unknown-id lookup answered %d: %s", code, missing)
	}
	if got := strings.Count(missing, "<D:response>"); got != 0 {
		t.Errorf("an unknown id answered with %d files, want none: %s", got, missing)
	}
}
