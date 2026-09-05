package objstore

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/limits"
)

// Bounds this package holds itself to. None of these come from
// go/engine/kit/limits: that package is shared across every subsystem, and
// an S3-shaped bound (a response the size of a metadata page, a key the
// length S3 itself refuses past) belongs beside the code that enforces it,
// not folded into a table three other subsystems also read.
const (
	dialTimeout           = 10 * time.Second
	tlsHandshakeTimeout   = 10 * time.Second
	responseHeaderTimeout = 30 * time.Second
	idleConnTimeout       = 90 * time.Second

	// metadataRequestTimeout bounds a call that answers from a header or a
	// short XML body: HEAD, DELETE, COPY and one page of LIST. A GET or PUT
	// carries file content instead and is bounded by scratch space and the
	// byte ceilings below, not by a clock, since a legitimate transfer of a
	// large file can legitimately take longer than any fixed deadline.
	metadataRequestTimeout = 30 * time.Second

	// maxMetadataBodyBytes bounds a HEAD, DELETE, COPY or error response
	// body, all of which are a small XML document or nothing at all on a
	// well-behaved endpoint.
	maxMetadataBodyBytes = 1 << 20

	// maxListResponseBytes bounds one ListObjectsV2 page's XML body. A page
	// holds at most maxListEntries entries, so this is generous headroom
	// for long keys, not an expectation of actually filling it.
	maxListResponseBytes = 8 << 20

	// maxListEntries bounds how many Contents and CommonPrefixes rows one
	// parsed page may carry combined, independent of the byte bound above:
	// a server answering with an unreasonable number of short entries inside
	// a small body is refused on count, not just on size.
	maxListEntries = 2_000

	// maxObjectKeyBytes is the length S3 itself refuses a key past. A listed
	// key beyond it did not come from S3 behaving normally.
	maxObjectKeyBytes = 1024

	// maxRenameEntries bounds how many objects a directory Rename walks. A
	// tree larger than this is refused before anything is copied, rather
	// than copying some fraction of it and refusing partway.
	maxRenameEntries = 10_000

	// maxObjectBodyBytes is a sanity ceiling on a GetObject download, far
	// above anything a real deployment's scratch space would hold anyway:
	// the practical limit is scratch space filling up and the write
	// failing with ErrNoSpace, not this constant.
	maxObjectBodyBytes = 64 << 30
)

// defaultHTTPClient is what Options.Client nil falls back to: connect and
// handshake timeouts so a wedged endpoint fails instead of hanging a
// request forever, and a refusal of any redirect to a different host, since
// a redirect is this package's only way an endpoint could point it
// somewhere the admin never configured.
func defaultHTTPClient() *http.Client {
	return &http.Client{
		CheckRedirect: refuseCrossHostRedirect,
		Transport: &http.Transport{
			DialContext:           (&net.Dialer{Timeout: dialTimeout}).DialContext,
			TLSHandshakeTimeout:   tlsHandshakeTimeout,
			ResponseHeaderTimeout: responseHeaderTimeout,
			IdleConnTimeout:       idleConnTimeout,
			ForceAttemptHTTP2:     true,
		},
	}
}

func refuseCrossHostRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 5 {
		return errors.New("objstore: too many redirects")
	}
	if req.URL.Host != via[0].URL.Host {
		return fmt.Errorf("objstore: refusing a redirect from %s to a different host %s", via[0].URL.Host, req.URL.Host)
	}
	return nil
}

// readBounded reads at most limit+1 bytes so a body exactly at the limit is
// distinguishable from one exceeding it, and refuses the latter rather than
// silently truncating it.
func readBounded(r io.Reader, limit int64, what string) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, fmt.Errorf("objstore: reading %s: %w", what, err)
	}
	if int64(len(body)) > limit {
		return nil, limits.Exceed(what, limit, int64(len(body)))
	}
	return body, nil
}

// s3ErrorXML is the small subset of S3's <Error> document this package
// reads. A body that fails to parse as this leaves Code and Message empty,
// which classifyS3Error's default case still turns into a refusal, so a
// malformed error body from a broken endpoint is never mistaken for
// success.
type s3ErrorXML struct {
	XMLName xml.Name `xml:"Error"`
	Code    string   `xml:"Code"`
	Message string   `xml:"Message"`
}

// classifyS3Error maps an unsuccessful S3 response onto this domain's own
// sentinel set, by HTTP status first and by the S3 error code second, since
// a compatible endpoint that gets one of the two conventions slightly wrong
// still resolves correctly against the other.
func classifyS3Error(status int, body []byte) error {
	var e s3ErrorXML
	if err := xml.Unmarshal(body, &e); err != nil {
		// A body that fails to parse leaves e at its zero value: Code and
		// Message stay empty, which the default case below still turns
		// into a refusal, so a malformed error body from a broken endpoint
		// is never mistaken for success.
		e = s3ErrorXML{}
	}
	detail := describeS3Error(status, e)
	switch {
	case status == http.StatusNotFound, e.Code == "NoSuchKey", e.Code == "NoSuchBucket":
		return fmt.Errorf("objstore: %s: %w", detail, vfs.ErrNotFound)
	case status == http.StatusForbidden, status == http.StatusUnauthorized, e.Code == "AccessDenied":
		return fmt.Errorf("objstore: %s: %w", detail, vfs.ErrDenied)
	case status == http.StatusConflict, e.Code == "BucketAlreadyExists", e.Code == "BucketAlreadyOwnedByYou":
		return fmt.Errorf("objstore: %s: %w", detail, vfs.ErrExists)
	default:
		return fmt.Errorf("objstore: %s", detail)
	}
}

func describeS3Error(status int, e s3ErrorXML) string {
	if e.Code == "" {
		return fmt.Sprintf("status %d", status)
	}
	return fmt.Sprintf("status %d, %s: %s", status, e.Code, e.Message)
}
