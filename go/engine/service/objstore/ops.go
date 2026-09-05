//go:build linux

package objstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
)

// requestTarget resolves the scheme, host and path one S3 call is made
// against, for an object named objectKey ("" for a bucket-level call such
// as ListObjectsV2). Path style puts the bucket in the path; virtual-hosted
// style puts it in the host. Both are configured per share, since which one
// an S3-compatible endpoint actually answers to varies by deployment.
func (r *Root) requestTarget(objectKey string) (scheme, host, path string) {
	scheme = r.endpointScheme
	if r.cfg.PathStyle {
		host = r.endpointHost
		if objectKey == "" {
			return scheme, host, "/" + r.cfg.Bucket
		}
		return scheme, host, "/" + r.cfg.Bucket + "/" + objectKey
	}
	host = r.cfg.Bucket + "." + r.endpointHost
	if objectKey == "" {
		return scheme, host, "/"
	}
	return scheme, host, "/" + objectKey
}

// newRequest builds and signs one S3 request. The URL is assembled by hand
// rather than through url.Parse of a joined string, because Path and
// RawPath are both set here to the same bytes under two different
// encodings: this is what makes (*url.URL).EscapedPath, which the signer
// reads, return exactly the percent-encoding this package chose rather than
// one net/url recomputes on its own.
func (r *Root) newRequest(
	ctx context.Context, method, objectKey string, query [][2]string,
	body io.Reader, size int64, payloadHashHex string,
) (*http.Request, error) {
	scheme, host, path := r.requestTarget(objectKey)
	u := &url.URL{
		Scheme:   scheme,
		Host:     host,
		Path:     path,
		RawPath:  awsURIEncode(path, false),
		RawQuery: canonicalQuery(query),
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://objstore.invalid/", body)
	if err != nil {
		return nil, fmt.Errorf("objstore: build request: %w", err)
	}
	req.URL = u
	req.Host = host
	req.ContentLength = size
	r.signer.sign(req, payloadHashHex, r.clk.Now())
	return req, nil
}

// listObjectsV2 issues one page of a bucket listing. maxKeys < 0 omits the
// parameter, leaving the endpoint's own default in effect; maxKeys == 0 is
// a valid, deliberate request (Alive's cheap probe) and is sent as such.
func (r *Root) listObjectsV2(ctx context.Context, prefix, delimiter, continuationToken string, maxKeys int) (result *listBucketResult, err error) {
	query := [][2]string{{"list-type", "2"}, {"prefix", prefix}}
	if delimiter != "" {
		query = append(query, [2]string{"delimiter", delimiter})
	}
	if continuationToken != "" {
		query = append(query, [2]string{"continuation-token", continuationToken})
	}
	if maxKeys >= 0 {
		query = append(query, [2]string{"max-keys", strconv.Itoa(maxKeys)})
	}
	req, err := r.newRequest(ctx, http.MethodGet, "", query, nil, 0, emptyPayloadHash())
	if err != nil {
		return nil, err
	}
	res, err := r.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("objstore: list objects: %w", err)
	}
	defer func() { err = errors.Join(err, res.Body.Close()) }()
	body, err := readBounded(res.Body, maxListResponseBytes, "list response body")
	if err != nil {
		return nil, err
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return nil, classifyS3Error(res.StatusCode, body)
	}
	return parseListBucketResult(body, prefix)
}

// headObject reports whether key exists and, when it does, its size and
// modification time. A 404 is not an error: it is the answer "no", which
// every caller of this function needs to tell apart from a transport or
// server failure.
func (r *Root) headObject(ctx context.Context, key string) (found bool, size uint64, mtimeNs int64, err error) {
	req, err := r.newRequest(ctx, http.MethodHead, key, nil, nil, 0, emptyPayloadHash())
	if err != nil {
		return false, 0, 0, err
	}
	res, err := r.http.Do(req)
	if err != nil {
		return false, 0, 0, fmt.Errorf("objstore: head object: %w", err)
	}
	defer func() { err = errors.Join(err, res.Body.Close()) }()
	if res.StatusCode == http.StatusNotFound {
		return false, 0, 0, nil
	}
	body, berr := readBounded(res.Body, maxMetadataBodyBytes, "head response body")
	if berr != nil {
		return false, 0, 0, berr
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return false, 0, 0, classifyS3Error(res.StatusCode, body)
	}
	return true, parseContentLength(res.Header.Get("Content-Length")), parseLastModified(res.Header.Get("Last-Modified")), nil
}

// parseContentLength bounds an untrusted response header: a value that does
// not parse as a non-negative integer is treated as absent rather than
// panicking or wrapping to a huge unsigned number.
func parseContentLength(v string) uint64 {
	n, err := strconv.ParseUint(v, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// parseLastModified reads the HTTP Last-Modified header, which is an
// HTTP-date. It is equally forgiving: an endpoint that sends a malformed
// timestamp gets a zero modification time, not a crash.
func parseLastModified(v string) int64 {
	t, err := http.ParseTime(v)
	if err != nil {
		return 0
	}
	return t.UnixNano()
}

// parseListedTime reads a listing's own LastModified element, which S3
// spells as RFC 3339 rather than as an HTTP-date. The two are different
// formats for the same fact, which is why they are different functions: the
// header parser silently returns zero for an RFC 3339 value, and reusing it
// here made every directory in a bucket report the epoch.
func parseListedTime(v string) int64 {
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return 0
	}
	return t.UnixNano()
}

func (r *Root) exists(ctx context.Context, key string) (bool, error) {
	found, _, _, err := r.headObject(ctx, key)
	return found, err
}

// isDirectory reports whether key names a prefix with at least one child or
// marker, without assuming a marker object exists: another tool writing
// "a/b.txt" with no "a/" marker still makes "a" a directory.
//
// The second return is the marker object's own modification time when this
// listing contained it, and zero otherwise. It comes free: the marker's key
// is exactly the prefix being listed, so a directory this server created has
// its time in the page already, and asking for it separately would be a
// second round trip for a value this one carries. A prefix that exists only
// because something wrote a file beneath it has no marker and therefore no
// time of its own, which is a fact about object storage rather than a value
// worth inventing.
func (r *Root) isDirectory(ctx context.Context, key string) (bool, int64, error) {
	prefix := key + "/"
	result, err := r.listObjectsV2(ctx, prefix, "/", "", 1)
	if err != nil {
		return false, 0, err
	}
	for _, c := range result.Contents {
		if c.Key == prefix {
			return true, parseListedTime(c.LastModified), nil
		}
	}
	return len(result.CommonPrefixes) > 0 || len(result.Contents) > 0, 0, nil
}

func (r *Root) deleteObjectForce(ctx context.Context, key string) (err error) {
	req, err := r.newRequest(ctx, http.MethodDelete, key, nil, nil, 0, emptyPayloadHash())
	if err != nil {
		return err
	}
	res, err := r.http.Do(req)
	if err != nil {
		return fmt.Errorf("objstore: delete object: %w", err)
	}
	defer func() { err = errors.Join(err, res.Body.Close()) }()
	body, err := readBounded(res.Body, maxMetadataBodyBytes, "delete response body")
	if err != nil {
		return err
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return classifyS3Error(res.StatusCode, body)
	}
	return nil
}

// copyObject issues a server-side CopyObject from srcKey to destKey.
// x-amz-copy-source is added after signing rather than folded into the
// signed-header set: it names an operation this package itself issues and
// controls, so there is nothing an intermediary tampering with an unsigned
// header here could make this package do that it would not already do.
func (r *Root) copyObject(ctx context.Context, srcKey, destKey string) (err error) {
	req, err := r.newRequest(ctx, http.MethodPut, destKey, nil, nil, 0, emptyPayloadHash())
	if err != nil {
		return err
	}
	req.Header.Set("x-amz-copy-source", "/"+r.cfg.Bucket+"/"+awsURIEncode(srcKey, false))
	res, err := r.http.Do(req)
	if err != nil {
		return fmt.Errorf("objstore: copy object: %w", err)
	}
	defer func() { err = errors.Join(err, res.Body.Close()) }()
	body, err := readBounded(res.Body, maxMetadataBodyBytes, "copy response body")
	if err != nil {
		return err
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return classifyS3Error(res.StatusCode, body)
	}
	return nil
}

// putEmptyObject writes the zero-byte directory marker Mkdir and the
// directory model both rely on.
func (r *Root) putEmptyObject(ctx context.Context, key string) error {
	req, err := r.newRequest(ctx, http.MethodPut, key, nil, http.NoBody, 0, emptyPayloadHash())
	if err != nil {
		return err
	}
	return r.doPut(req)
}

// putObject uploads the first size bytes of f's underlying descriptor,
// already durable on scratch storage, as key. size is a byte count
// (vfs.Stat.Size is unsigned) and is narrowed to the signed int64
// net/http.Request.ContentLength needs through num.Narrow rather than a
// bare cast, so a file bigger than an int64 can address is refused instead
// of silently wrapping to a negative content length. The body is a
// SectionReader over the descriptor rather than the *vfs.File itself, so
// nothing here can advance the handle's shared cursor or trigger an
// accidental Close through net/http's usual "close the body when done"
// behavior.
func (r *Root) putObject(ctx context.Context, key string, f *vfs.File, size uint64) error {
	contentLength, err := num.Narrow[int64](size)
	if err != nil {
		return fmt.Errorf("objstore: upload body size: %w", err)
	}
	hashHex, err := hashSection(f.OSFile(), contentLength)
	if err != nil {
		return fmt.Errorf("objstore: hash upload body: %w", err)
	}
	body := io.NopCloser(io.NewSectionReader(f.OSFile(), 0, contentLength))
	req, err := r.newRequest(ctx, http.MethodPut, key, nil, body, contentLength, hashHex)
	if err != nil {
		return err
	}
	return r.doPut(req)
}

func (r *Root) doPut(req *http.Request) (err error) {
	res, err := r.http.Do(req)
	if err != nil {
		return fmt.Errorf("objstore: put object: %w", err)
	}
	defer func() { err = errors.Join(err, res.Body.Close()) }()
	body, err := readBounded(res.Body, maxMetadataBodyBytes, "put response body")
	if err != nil {
		return err
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return classifyS3Error(res.StatusCode, body)
	}
	return nil
}

func hashSection(f *os.File, size int64) (string, error) {
	h := sha256.New()
	if _, err := io.Copy(h, io.NewSectionReader(f, 0, size)); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// getObject downloads key's whole content into f's underlying descriptor.
// The download is bounded at maxObjectBodyBytes as a sanity ceiling; the
// practical limit an operator experiences is scratch space filling up,
// which surfaces as an ordinary write failure from the kernel.
func (r *Root) getObject(ctx context.Context, key string, f *vfs.File) (n int64, err error) {
	req, err := r.newRequest(ctx, http.MethodGet, key, nil, nil, 0, emptyPayloadHash())
	if err != nil {
		return 0, err
	}
	res, err := r.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("objstore: get object: %w", err)
	}
	defer func() { err = errors.Join(err, res.Body.Close()) }()
	if res.StatusCode < 200 || res.StatusCode > 299 {
		body, berr := readBounded(res.Body, maxMetadataBodyBytes, "get response body")
		if berr != nil {
			return 0, berr
		}
		return 0, classifyS3Error(res.StatusCode, body)
	}
	n, err = io.Copy(f.OSFile(), io.LimitReader(res.Body, maxObjectBodyBytes+1))
	if err != nil {
		return 0, fmt.Errorf("objstore: download body: %w", err)
	}
	if n > maxObjectBodyBytes {
		return 0, fmt.Errorf("objstore: object %q exceeds the %d byte download ceiling", key, maxObjectBodyBytes)
	}
	return n, nil
}
