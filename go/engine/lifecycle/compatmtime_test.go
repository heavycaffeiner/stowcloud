//go:build linux && compat_nc

package lifecycle_test

import (
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"
)

// A plain PUT keeps the modification time the client declared.
//
// The phone clients send it on every upload, not only on a chunked publish,
// and the gallery orders on what a listing reports. Stamping the file with the
// time it was received sorts a camera roll by when it happened to sync rather
// than by when the pictures were taken.
func TestAPlainPutKeepsTheDeclaredModificationTime(t *testing.T) {
	t.Parallel()
	base, credential, client := uploadFixture(t)

	// A date far enough in the past that the write time cannot be mistaken
	// for it.
	const declared = "1600000000"
	want := time.Unix(1_600_000_000, 0).UTC()

	url := base + "/remote.php/dav/files/alice/documents/holiday.jpg"
	if code, body := davSend(t, client, credential, http.MethodPut, url,
		strings.NewReader("jpeg-bytes"),
		map[string]string{"X-OC-Mtime": declared}); code != http.StatusCreated {
		t.Fatalf("writing answered %d: %s", code, body)
	}

	ask := `<?xml version="1.0"?><d:propfind xmlns:d="DAV:"><d:prop><d:getlastmodified/></d:prop></d:propfind>`
	code, body := davSend(t, client, credential, "PROPFIND", url, strings.NewReader(ask),
		map[string]string{"Depth": "0", "Content-Type": "text/xml"})
	if code != http.StatusMultiStatus {
		t.Fatalf("listing answered %d: %s", code, body)
	}

	found := regexp.MustCompile(`<D:getlastmodified>([^<]*)</D:getlastmodified>`).FindStringSubmatch(body)
	if found == nil {
		t.Fatalf("the listing carried no timestamp: %s", body)
	}
	got, perr := http.ParseTime(found[1])
	if perr != nil {
		t.Fatalf("the listing reported an unparsable time %q: %v", found[1], perr)
	}
	if !got.UTC().Equal(want) {
		t.Errorf("the file reports %s, want the declared %s: an upload is dated when it synced",
			got.UTC().Format(time.RFC3339), want.Format(time.RFC3339))
	}
}
