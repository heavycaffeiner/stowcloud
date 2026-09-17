//go:build linux

package objstore

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
)

const (
	maxPresignTTL       = 7 * 24 * time.Hour
	maxUploadIDBytes    = 1024
	maxMultipartXMLBody = 8 << 20
	maxMultipartParts   = 10_000
)

// ErrDirectTransferUnsupported means this backend cannot issue direct object
// store capabilities. It is intentionally distinct from transport and S3
// errors so callers can fall back to the ordinary proxy/TUS path.
var ErrDirectTransferUnsupported = errors.New("objstore: direct transfer unsupported")

// MultipartPart is the provider-neutral representation of one S3 multipart
// part. ETag is normalized without surrounding quotes. Checksum is an
// optional lowercase SHA-256 hex digest.
type MultipartPart struct {
	PartNumber int
	ETag       string
	Size       int64
	Checksum   string
}

// DirectTransferProvider is the narrow object-store contract consumed by the
// lifecycle layer. Object keys are exact S3 keys, including the configured
// prefix; callers must obtain them from ObjectKey after validating a path.
type DirectTransferProvider interface {
	DirectTransfer() bool
	ObjectKey(vfs.SafePath) string
	BeginMultipart(context.Context, string, int64, string) (string, error)
	AbortMultipart(context.Context, string, string) error
	ListParts(context.Context, string, string) ([]MultipartPart, error)
	CompleteMultipart(context.Context, string, string, []MultipartPart) error
	PresignUploadPart(context.Context, string, string, int, int64, string, time.Duration) (string, http.Header, error)
	PresignGet(context.Context, string, time.Duration) (string, error)
	ObjectMetadata(context.Context, string) (uint64, string, string, bool, error)
}

var _ DirectTransferProvider = (*Root)(nil)

// DirectTransfer reports whether this Root has the credentials required to
// issue authenticated direct-transfer URLs and multipart requests. It never
// performs a network probe and never exposes the credentials.
func (r *Root) DirectTransfer() bool { return r != nil && r.direct }

func unsupportedDirect() error { return ErrDirectTransferUnsupported }

func validateDirectTTL(ttl time.Duration) (int, error) {
	if ttl <= 0 || ttl > maxPresignTTL {
		return 0, fmt.Errorf("objstore: presign expiry must be between 1 second and %s", maxPresignTTL)
	}
	seconds := int(ttl / time.Second)
	if seconds <= 0 {
		return 0, errors.New("objstore: presign expiry is less than one second")
	}
	return seconds, nil
}

func validateDirectObjectKey(r *Root, key string) error {
	if r == nil || !r.DirectTransfer() {
		return unsupportedDirect()
	}
	if key == "" || len(key) > maxObjectKeyBytes {
		return errors.New("objstore: invalid direct-transfer object key")
	}
	if r.cfg.Prefix != "" && key != r.cfg.Prefix && !strings.HasPrefix(key, r.cfg.Prefix+"/") {
		return errors.New("objstore: direct-transfer object key is outside the configured prefix")
	}
	for i := range key {
		if key[i] < 0x20 || key[i] == 0x7f {
			return errors.New("objstore: direct-transfer object key contains a control character")
		}
	}
	if _, err := vfs.ParseSafePath(key); err != nil {
		return fmt.Errorf("objstore: invalid direct-transfer object key: %w", err)
	}
	return nil
}

func validateUploadID(id string) error {
	if id == "" || len(id) > maxUploadIDBytes {
		return errors.New("objstore: invalid multipart upload id")
	}
	for i := range id {
		if id[i] < 0x21 || id[i] == 0x7f {
			return errors.New("objstore: invalid multipart upload id")
		}
	}
	return nil
}

func validateChecksum(checksum string) error {
	if checksum == "" {
		return nil
	}
	if len(checksum) != 64 {
		return errors.New("objstore: checksum must be a SHA-256 hex digest")
	}
	for i := range checksum {
		c := checksum[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return errors.New("objstore: checksum must be a SHA-256 hex digest")
		}
	}
	return nil
}

func checksumHeaderValue(checksum string) (string, error) {
	if err := validateChecksum(checksum); err != nil {
		return "", err
	}
	if checksum == "" {
		return "", nil
	}
	decoded, err := hex.DecodeString(checksum)
	if err != nil {
		return "", fmt.Errorf("objstore: decode checksum: %w", err)
	}
	return base64.StdEncoding.EncodeToString(decoded), nil
}

func normalizeETag(etag string) string {
	etag = strings.TrimSpace(etag)
	etag = strings.TrimPrefix(etag, "W/")
	return strings.Trim(etag, "\"")
}

func checksumFromHeader(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil || len(decoded) != 32 {
		return "", errors.New("objstore: invalid S3 SHA-256 checksum")
	}
	return hex.EncodeToString(decoded), nil
}

func (r *Root) directRequest(ctx context.Context, method, key string, query [][2]string, body io.Reader, size int64, payload string, headers http.Header) (*http.Request, error) {
	if err := validateDirectObjectKey(r, key); err != nil {
		return nil, err
	}
	return r.newRequestWithHeaders(ctx, method, key, query, body, size, payload, headers)
}

// BeginMultipart starts an S3 multipart upload without staging object bytes on
// the server. checksum is the optional full-object SHA-256 hex digest.
func (r *Root) BeginMultipart(ctx context.Context, key string, size int64, checksum string) (string, error) {
	if err := validateDirectObjectKey(r, key); err != nil {
		return "", err
	}
	if size < 0 {
		return "", errors.New("objstore: multipart size must not be negative")
	}
	if err := validateChecksum(checksum); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	callCtx, cancel := context.WithTimeout(ctx, metadataRequestTimeout)
	defer cancel()
	req, err := r.directRequest(callCtx, http.MethodPost, key, [][2]string{{"uploads", ""}}, http.NoBody, 0, emptyPayloadHash(), nil)
	if err != nil {
		return "", err
	}
	res, err := r.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("objstore: create multipart upload: %w", err)
	}
	defer res.Body.Close()
	body, err := readBounded(res.Body, maxMetadataBodyBytes, "create multipart response body")
	if err != nil {
		return "", err
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return "", classifyS3Error(res.StatusCode, body)
	}
	var out createMultipartResult
	if err := xml.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("objstore: parse create multipart response: %w", err)
	}
	if out.UploadID == "" {
		return "", errors.New("objstore: create multipart response has no upload id")
	}
	if err := validateUploadID(out.UploadID); err != nil {
		return "", err
	}
	return out.UploadID, nil
}

// AbortMultipart cancels an S3 multipart upload and discards its uncommitted
// parts. It is safe for callers to invoke this during cancellation cleanup.
func (r *Root) AbortMultipart(ctx context.Context, key, uploadID string) error {
	if err := validateDirectObjectKey(r, key); err != nil {
		return err
	}
	if err := validateUploadID(uploadID); err != nil {
		return err
	}
	callCtx, cancel := context.WithTimeout(ctx, metadataRequestTimeout)
	defer cancel()
	req, err := r.directRequest(callCtx, http.MethodDelete, key, [][2]string{{"uploadId", uploadID}}, nil, 0, emptyPayloadHash(), nil)
	if err != nil {
		return err
	}
	res, err := r.http.Do(req)
	if err != nil {
		return fmt.Errorf("objstore: abort multipart upload: %w", err)
	}
	defer res.Body.Close()
	body, err := readBounded(res.Body, maxMetadataBodyBytes, "abort multipart response body")
	if err != nil {
		return err
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return classifyS3Error(res.StatusCode, body)
	}
	return nil
}

// ListParts returns every currently uploaded part, following S3 pagination.
func (r *Root) ListParts(ctx context.Context, key, uploadID string) ([]MultipartPart, error) {
	if err := validateDirectObjectKey(r, key); err != nil {
		return nil, err
	}
	if err := validateUploadID(uploadID); err != nil {
		return nil, err
	}
	var out []MultipartPart
	marker := ""
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		callCtx, cancel := context.WithTimeout(ctx, metadataRequestTimeout)
		query := [][2]string{{"uploadId", uploadID}, {"max-parts", "1000"}}
		if marker != "" {
			query = append(query, [2]string{"part-number-marker", marker})
		}
		req, err := r.directRequest(callCtx, http.MethodGet, key, query, nil, 0, emptyPayloadHash(), nil)
		if err != nil {
			cancel()
			return nil, err
		}
		res, err := r.http.Do(req)
		if err != nil {
			cancel()
			return nil, fmt.Errorf("objstore: list multipart parts: %w", err)
		}
		body, berr := readBounded(res.Body, maxListResponseBytes, "list multipart response body")
		cerr := res.Body.Close()
		cancel()
		if berr != nil {
			return nil, berr
		}
		if cerr != nil {
			return nil, fmt.Errorf("objstore: close list multipart response: %w", cerr)
		}
		if res.StatusCode < 200 || res.StatusCode > 299 {
			return nil, classifyS3Error(res.StatusCode, body)
		}
		var page listPartsResult
		if err := xml.Unmarshal(body, &page); err != nil {
			return nil, fmt.Errorf("objstore: parse list multipart response: %w", err)
		}
		if len(out)+len(page.Parts) > maxMultipartParts {
			return nil, errors.New("objstore: multipart upload has too many parts")
		}
		for _, p := range page.Parts {
			if p.PartNumber <= 0 || p.PartNumber > maxMultipartParts || p.Size < 0 {
				return nil, errors.New("objstore: list multipart response has invalid part")
			}
			etag := normalizeETag(p.ETag)
			if etag == "" {
				return nil, errors.New("objstore: list multipart response has empty ETag")
			}
			checksum, err := checksumFromHeader(p.ChecksumSHA256)
			if err != nil {
				return nil, err
			}
			out = append(out, MultipartPart{PartNumber: p.PartNumber, ETag: etag, Size: p.Size, Checksum: checksum})
		}
		if !page.IsTruncated {
			break
		}
		next := page.NextPartNumberMarker
		if next == "" {
			if len(page.Parts) == 0 {
				return nil, errors.New("objstore: truncated list multipart response has no continuation marker")
			}
			next = strconv.Itoa(page.Parts[len(page.Parts)-1].PartNumber)
		}
		if next == marker {
			return nil, errors.New("objstore: list multipart response repeated continuation marker")
		}
		marker = next
	}
	return out, nil
}

// CompleteMultipart validates the caller's parts and asks S3 to publish the
// object. The XML request contains no object bytes, only part metadata.
func (r *Root) CompleteMultipart(ctx context.Context, key, uploadID string, parts []MultipartPart) error {
	if err := validateDirectObjectKey(r, key); err != nil {
		return err
	}
	if err := validateUploadID(uploadID); err != nil {
		return err
	}
	if len(parts) == 0 || len(parts) > maxMultipartParts {
		return errors.New("objstore: invalid multipart part count")
	}
	requestParts := make([]completePartXML, len(parts))
	last := 0
	for i, p := range parts {
		if p.PartNumber <= last || p.PartNumber > maxMultipartParts || p.Size < 0 || p.ETag == "" {
			return errors.New("objstore: multipart parts must be ascending and valid")
		}
		if err := validateChecksum(p.Checksum); err != nil {
			return err
		}
		if strings.ContainsAny(p.ETag, "\r\n") {
			return errors.New("objstore: invalid multipart ETag")
		}
		requestParts[i] = completePartXML{PartNumber: p.PartNumber, ETag: `"` + normalizeETag(p.ETag) + `"`}
		if p.Checksum != "" {
			v, err := checksumHeaderValue(p.Checksum)
			if err != nil {
				return err
			}
			requestParts[i].ChecksumSHA256 = v
		}
		last = p.PartNumber
	}
	xmlBody, err := xml.Marshal(completeMultipartXML{Parts: requestParts})
	if err != nil {
		return fmt.Errorf("objstore: marshal complete multipart request: %w", err)
	}
	if len(xmlBody) > maxMultipartXMLBody {
		return errors.New("objstore: complete multipart request is too large")
	}
	callCtx, cancel := context.WithTimeout(ctx, metadataRequestTimeout)
	defer cancel()
	body := bytes.NewReader(xmlBody)
	req, err := r.directRequest(callCtx, http.MethodPost, key, [][2]string{{"uploadId", uploadID}}, body, int64(len(xmlBody)), sha256Hex(xmlBody), http.Header{"Content-Type": []string{"application/xml"}})
	if err != nil {
		return err
	}
	res, err := r.http.Do(req)
	if err != nil {
		return fmt.Errorf("objstore: complete multipart upload: %w", err)
	}
	defer res.Body.Close()
	responseBody, err := readBounded(res.Body, maxMetadataBodyBytes, "complete multipart response body")
	if err != nil {
		return err
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return classifyS3Error(res.StatusCode, responseBody)
	}
	var result completeMultipartResult
	if err := xml.Unmarshal(responseBody, &result); err != nil {
		return fmt.Errorf("objstore: parse complete multipart response: %w", err)
	}
	if result.ETag == "" {
		return errors.New("objstore: complete multipart response has no ETag")
	}
	return nil
}

// PresignUploadPart creates a short-lived query-signed PUT URL. The returned
// checksum header is part of the signature and must be sent unchanged by the
// client. No signing key or secret is included in the URL or headers.
func (r *Root) PresignUploadPart(ctx context.Context, key, uploadID string, partNumber int, size int64, checksum string, expiry time.Duration) (string, http.Header, error) {
	if err := validateDirectObjectKey(r, key); err != nil {
		return "", nil, err
	}
	if err := validateUploadID(uploadID); err != nil {
		return "", nil, err
	}
	if partNumber <= 0 || partNumber > maxMultipartParts || size < 0 {
		return "", nil, errors.New("objstore: invalid multipart part number or size")
	}
	if err := validateChecksum(checksum); err != nil {
		return "", nil, err
	}
	seconds, err := validateDirectTTL(expiry)
	if err != nil {
		return "", nil, err
	}
	if err := ctx.Err(); err != nil {
		return "", nil, err
	}
	headers := http.Header{}
	signed := map[string]string{"host": ""}
	if checksum != "" {
		value, err := checksumHeaderValue(checksum)
		if err != nil {
			return "", nil, err
		}
		headers.Set("x-amz-checksum-sha256", value)
		signed["x-amz-checksum-sha256"] = value
	}
	url, err := r.presign(ctx, http.MethodPut, key, [][2]string{{"partNumber", strconv.Itoa(partNumber)}, {"uploadId", uploadID}}, seconds, signed, headers)
	return url, headers, err
}

// PresignGet creates a short-lived query-signed GET URL bound to the exact
// object key and configured endpoint.
func (r *Root) PresignGet(ctx context.Context, key string, expiry time.Duration) (string, error) {
	if err := validateDirectObjectKey(r, key); err != nil {
		return "", err
	}
	seconds, err := validateDirectTTL(expiry)
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return r.presign(ctx, http.MethodGet, key, nil, seconds, map[string]string{"host": ""}, nil)
}

func (r *Root) presign(ctx context.Context, method, key string, extra [][2]string, seconds int, signed map[string]string, headers http.Header) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	scheme, host, path := r.requestTarget(key)
	signed["host"] = host
	names := make([]string, 0, len(signed))
	for name := range signed {
		name = strings.ToLower(strings.TrimSpace(name))
		if name != "host" && name != "x-amz-checksum-sha256" {
			return "", errors.New("objstore: refusing an unsigned presign header")
		}
		names = append(names, name)
	}
	sort.Strings(names)
	amzDate := r.clk.Now().UTC().Format("20060102T150405Z")
	dateStamp := amzDate[:8]
	scope := dateStamp + "/" + r.signer.region + "/s3/aws4_request"
	params := append([][2]string(nil), extra...)
	params = append(params,
		[2]string{"X-Amz-Algorithm", awsAlgorithm},
		[2]string{"X-Amz-Credential", r.signer.accessKey + "/" + scope},
		[2]string{"X-Amz-Date", amzDate},
		[2]string{"X-Amz-Expires", strconv.Itoa(seconds)},
		[2]string{"X-Amz-SignedHeaders", strings.Join(names, ";")},
	)
	query := canonicalQuery(params)
	headerBlock := canonicalHeaderBlock(names, signed)
	creq := canonicalRequest(method, awsURIEncode(path, false), query, headerBlock, strings.Join(names, ";"), "UNSIGNED-PAYLOAD")
	sts := stringToSign(amzDate, scope, sha256Hex([]byte(creq)))
	sig := hex.EncodeToString(hmacSHA256(signingKey(string(r.signer.secret), dateStamp, r.signer.region, "s3"), sts))
	params = append(params, [2]string{"X-Amz-Signature", sig})
	u := &url.URL{Scheme: scheme, Host: host, Path: path, RawPath: awsURIEncode(path, false), RawQuery: canonicalQuery(params)}
	return u.String(), nil
}

// ObjectMetadata performs a bounded HEAD request and returns object size,
// normalized ETag, and optional lowercase SHA-256 checksum. found is false
// only for a normal S3 not-found response.
func (r *Root) ObjectMetadata(ctx context.Context, key string) (size uint64, etag, checksum string, found bool, err error) {
	if err := validateDirectObjectKey(r, key); err != nil {
		return 0, "", "", false, err
	}
	callCtx, cancel := context.WithTimeout(ctx, metadataRequestTimeout)
	defer cancel()
	req, err := r.directRequest(callCtx, http.MethodHead, key, nil, nil, 0, emptyPayloadHash(), nil)
	if err != nil {
		return 0, "", "", false, err
	}
	res, err := r.http.Do(req)
	if err != nil {
		return 0, "", "", false, fmt.Errorf("objstore: head direct object: %w", err)
	}
	defer res.Body.Close()
	body, err := readBounded(res.Body, maxMetadataBodyBytes, "head direct object response body")
	if err != nil {
		return 0, "", "", false, err
	}
	if res.StatusCode == http.StatusNotFound {
		return 0, "", "", false, nil
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return 0, "", "", false, classifyS3Error(res.StatusCode, body)
	}
	checksum, err = checksumFromHeader(res.Header.Get("x-amz-checksum-sha256"))
	if err != nil {
		return 0, "", "", false, err
	}
	return parseContentLength(res.Header.Get("Content-Length")), normalizeETag(res.Header.Get("ETag")), checksum, true, nil
}

type createMultipartResult struct {
	XMLName  xml.Name `xml:"InitiateMultipartUploadResult"`
	UploadID string   `xml:"UploadId"`
}

type listPartsResult struct {
	XMLName              xml.Name        `xml:"ListPartsResult"`
	IsTruncated          bool            `xml:"IsTruncated"`
	NextPartNumberMarker string          `xml:"NextPartNumberMarker"`
	Parts                []listedPartXML `xml:"Part"`
}

type listedPartXML struct {
	PartNumber     int    `xml:"PartNumber"`
	ETag           string `xml:"ETag"`
	Size           int64  `xml:"Size"`
	ChecksumSHA256 string `xml:"ChecksumSHA256"`
}

type completeMultipartXML struct {
	XMLName xml.Name          `xml:"CompleteMultipartUpload"`
	Parts   []completePartXML `xml:"Part"`
}

type completePartXML struct {
	PartNumber     int    `xml:"PartNumber"`
	ETag           string `xml:"ETag"`
	ChecksumSHA256 string `xml:"ChecksumSHA256,omitempty"`
}

type completeMultipartResult struct {
	XMLName xml.Name `xml:"CompleteMultipartUploadResult"`
	ETag    string   `xml:"ETag"`
}
