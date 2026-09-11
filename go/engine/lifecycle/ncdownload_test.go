//go:build linux && compat_nc

package lifecycle_test

import (
	"strconv"
	"strings"
	"testing"
)

// Downloading, and the two headers a download is refused without.

// A download carries the validator and the length. One client aborts a
// download whose response has no etag, reporting a broken proxy, so the
// header is part of the contract rather than an optimisation.
func TestADownloadCarriesItsValidator(t *testing.T) {
	t.Parallel()
	const content = "the bytes a client expects"
	f := newNCFixture(t, []byte(content))

	resp, body := f.request(t, "GET", f.filePath("doc.bin"), nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("answered %d, want 200", resp.StatusCode)
	}
	if string(body) != content {
		t.Errorf("the body is %q", body)
	}
	if resp.Header.Get("ETag") == "" {
		t.Error("no ETag, and one client aborts the transfer without one")
	}
	if got := resp.Header.Get("Content-Length"); got != strconv.Itoa(len(content)) {
		t.Errorf("Content-Length is %q, want %d", got, len(content))
	}
	if got := resp.Header.Get("Accept-Ranges"); got != "bytes" {
		t.Errorf("Accept-Ranges is %q", got)
	}
	if resp.Header.Get("OC-FileId") == "" {
		t.Error("no OC-FileId on a download")
	}
}

func TestCompatDownloadsDoNotRenderActiveContentOnTheAppOrigin(t *testing.T) {
	t.Parallel()
	f := newNCFixture(t, []byte("plain"))

	if resp, body := f.request(t, "PUT", f.filePath("active.html"),
		strings.NewReader("<script src=\"payload.js\"></script>"), nil); resp.StatusCode != 201 {
		t.Fatalf("uploading active content answered %d: %s", resp.StatusCode, body)
	}
	active, _ := f.request(t, "GET", f.filePath("active.html"), nil, nil)
	if !strings.HasPrefix(active.Header.Get("Content-Disposition"), "attachment;") {
		t.Errorf("active content was not forced to attachment: %q", active.Header.Get("Content-Disposition"))
	}
	if got := active.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("active content carries X-Content-Type-Options %q", got)
	}

	passive, _ := f.request(t, "GET", f.filePath("doc.bin"), nil, nil)
	if policies := strings.Join(passive.Header.Values("Content-Security-Policy"), "; "); !strings.Contains(policies, "sandbox") {
		t.Errorf("inline passive content carries CSP values %q", policies)
	}
}

// A resumed download asks for the tail it is missing.
func TestARangeRequestAnswersThePartialContent(t *testing.T) {
	t.Parallel()
	const content = "0123456789abcdef"
	f := newNCFixture(t, []byte(content))

	resp, body := f.request(t, "GET", f.filePath("doc.bin"), nil,
		map[string]string{"Range": "bytes=4-9"})
	if resp.StatusCode != 206 {
		t.Fatalf("answered %d, want 206", resp.StatusCode)
	}
	if string(body) != "456789" {
		t.Errorf("the body is %q", body)
	}
	want := "bytes 4-9/" + strconv.Itoa(len(content))
	if got := resp.Header.Get("Content-Range"); got != want {
		t.Errorf("Content-Range is %q, want %q", got, want)
	}
}

// A range naming nothing inside the file is refused with the real size, which
// is what lets a client ask again correctly.
func TestAnUnsatisfiableCompatRangeReportsTheSize(t *testing.T) {
	t.Parallel()
	const content = "short"
	f := newNCFixture(t, []byte(content))

	resp, _ := f.request(t, "GET", f.filePath("doc.bin"), nil,
		map[string]string{"Range": "bytes=500-600"})
	if resp.StatusCode != 416 {
		t.Fatalf("answered %d, want 416", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Range"); got != "bytes */"+strconv.Itoa(len(content)) {
		t.Errorf("Content-Range is %q", got)
	}
}

// A conditional read answers 304 with no body, which is how the preview and
// avatar paths avoid re-sending an image on every screen.
func TestAConditionalReadAnswersNotModified(t *testing.T) {
	t.Parallel()
	f := newNCFixture(t, []byte("cacheable"))

	first, _ := f.request(t, "GET", f.filePath("doc.bin"), nil, nil)
	etag := first.Header.Get("ETag")
	if etag == "" {
		t.Fatal("no etag to condition on")
	}

	second, body := f.request(t, "GET", f.filePath("doc.bin"), nil,
		map[string]string{"If-None-Match": etag})
	if second.StatusCode != 304 {
		t.Fatalf("answered %d, want 304", second.StatusCode)
	}
	if len(body) != 0 {
		t.Errorf("a 304 carried %d bytes", len(body))
	}
}

// A HEAD answers the metadata and no bytes: one client uses it to decide
// whether a file is there at all.
func TestAHeadAnswersMetadataWithoutBytes(t *testing.T) {
	t.Parallel()
	f := newNCFixture(t, []byte("has bytes"))

	resp, body := f.request(t, "HEAD", f.filePath("doc.bin"), nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("answered %d, want 200", resp.StatusCode)
	}
	if len(body) != 0 {
		t.Errorf("HEAD returned %d bytes", len(body))
	}
	if resp.Header.Get("Content-Length") == "" {
		t.Error("HEAD reported no length")
	}
}

// A collection answers a read with 200 and no body. It has no bytes to send,
// but it does exist, and one client probes a folder this way before every
// upload into it and accepts only 200: a refusal here aborts the transfer
// before it starts.
func TestReadingACollectionAnswersItsMetadata(t *testing.T) {
	t.Parallel()
	f := newNCFixture(t, []byte("hello"))

	resp, body := f.request(t, "GET", f.filePath(""), nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("answered %d, want 200", resp.StatusCode)
	}
	if len(body) != 0 {
		t.Errorf("a collection returned %d bytes", len(body))
	}
	if resp.Header.Get("ETag") == "" {
		t.Error("a collection read carried no validator")
	}
	if got := resp.Header.Get("Content-Type"); got != "httpd/unix-directory" {
		t.Errorf("the content type is %q", got)
	}
}

// A refusal carries the error document, and its content type is on the short
// list one client will parse at all. Anything else is reported to a person as
// an unexpected response.
func TestARefusalCarriesTheErrorDocument(t *testing.T) {
	t.Parallel()
	f := newNCFixture(t, []byte("hello"))

	resp, body := f.request(t, "GET", f.filePath("absent.bin"), nil, nil)
	if resp.StatusCode != 404 {
		t.Fatalf("answered %d, want 404", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != "application/xml; charset=utf-8" {
		t.Errorf("the content type is %q", got)
	}
	if !strings.Contains(string(body), "<s:exception>") {
		t.Errorf("the body is not the error document:\n%s", body)
	}
}
