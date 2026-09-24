//go:build linux

package objstore

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/heavycaffeiner/stowcloud/go/internal/platform/number"
	"github.com/heavycaffeiner/stowcloud/go/internal/platform/storage/vfs"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	maxPresignTTL     = 7 * 24 * time.Hour
	maxUploadIDBytes  = 1024
	maxMultipartParts = 10000
)

var ErrDirectTransferUnsupported = errors.New("objstore: direct transfer unsupported")

type MultipartPart struct {
	PartNumber int
	ETag       string
	Size       int64
	Checksum   string
}

// TransferReceipt is the provider's durable publication observation. It is
// intentionally neutral: callers use it to reconcile an ambiguous commit,
// not to claim equivalence with the source bytes.
type TransferReceipt struct {
	Size     uint64
	ETag     string
	Checksum string
}

type DirectTransferProvider interface {
	DirectTransfer() bool
	ObjectKey(vfs.SafePath) string
	BeginMultipart(context.Context, string, int64, string) (string, error)
	AbortMultipart(context.Context, string, string) error
	ListParts(context.Context, string, string) ([]MultipartPart, error)
	CompleteMultipart(context.Context, string, string, []MultipartPart) (TransferReceipt, error)
	PresignUploadPart(context.Context, string, string, int, int64, string, time.Duration) (string, http.Header, error)
	PresignGet(context.Context, string, time.Duration) (string, error)
	ObjectMetadata(context.Context, string) (uint64, string, string, bool, error)
}

var _ DirectTransferProvider = (*Root)(nil)

func (r *Root) DirectTransfer() bool { return r != nil && r.direct }
func validateDirectTTL(t time.Duration) (int, error) {
	if t <= 0 || t > maxPresignTTL {
		return 0, errors.New("objstore: invalid presign expiry")
	}
	return int(t / time.Second), nil
}
func validateDirectObjectKey(r *Root, k string) error {
	if r == nil || !r.DirectTransfer() {
		return ErrDirectTransferUnsupported
	}
	if k == "" || len(k) > maxObjectKeyBytes {
		return errors.New("objstore: invalid direct-transfer object key")
	}
	if r.cfg.Prefix != "" && !strings.HasPrefix(k, r.cfg.Prefix+"/") && k != r.cfg.Prefix {
		return errors.New("objstore: direct-transfer object key is outside configured prefix")
	}
	if _, e := vfs.ParseSafePath(k); e != nil {
		return e
	}
	return nil
}
func validateUploadID(id string) error {
	if id == "" || len(id) > maxUploadIDBytes {
		return errors.New("objstore: invalid multipart upload id")
	}
	return nil
}
func validateChecksum(c string) error {
	if c == "" {
		return nil
	}
	if len(c) != 64 {
		return errors.New("objstore: checksum must be a SHA-256 hex digest")
	}
	_, e := hex.DecodeString(c)
	return e
}
func normalizeETag(e string) string {
	return strings.Trim(strings.TrimPrefix(strings.TrimSpace(e), "W/"), "\"")
}
func checksumHeaderValue(c string) (string, error) {
	if c == "" {
		return "", nil
	}
	b, e := hex.DecodeString(c)
	if e != nil {
		return "", e
	}
	return base64.StdEncoding.EncodeToString(b), nil
}
func (r *Root) BeginMultipart(ctx context.Context, key string, size int64, checksum string) (string, error) {
	if e := validateDirectObjectKey(r, key); e != nil {
		return "", e
	}
	if e := validateChecksum(checksum); e != nil {
		return "", e
	}
	if e := r.ensureSDK(); e != nil {
		return "", e
	}
	in := &s3.CreateMultipartUploadInput{Bucket: aws.String(r.cfg.Bucket), Key: aws.String(key)}
	if checksum != "" {
		in.ChecksumAlgorithm = types.ChecksumAlgorithmSha256
	}
	o, e := r.s3.CreateMultipartUpload(ctx, in)
	if e != nil {
		return "", sdkError("create multipart upload", e)
	}
	if aws.ToString(o.UploadId) == "" {
		return "", errors.New("objstore: create multipart upload returned no upload id")
	}
	return aws.ToString(o.UploadId), nil
}
func (r *Root) AbortMultipart(ctx context.Context, key, id string) error {
	if e := validateDirectObjectKey(r, key); e != nil {
		return e
	}
	if e := validateUploadID(id); e != nil {
		return e
	}
	if e := r.ensureSDK(); e != nil {
		return e
	}
	_, e := r.s3.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{Bucket: aws.String(r.cfg.Bucket), Key: aws.String(key), UploadId: aws.String(id)})
	return sdkError("abort multipart upload", e)
}
func (r *Root) ListParts(ctx context.Context, key, id string) ([]MultipartPart, error) {
	if e := validateDirectObjectKey(r, key); e != nil {
		return nil, e
	}
	if e := validateUploadID(id); e != nil {
		return nil, e
	}
	if e := r.ensureSDK(); e != nil {
		return nil, e
	}
	p := s3.NewListPartsPaginator(r.s3, &s3.ListPartsInput{Bucket: aws.String(r.cfg.Bucket), Key: aws.String(key), UploadId: aws.String(id)})
	out := make([]MultipartPart, 0)
	for p.HasMorePages() {
		o, e := p.NextPage(ctx)
		if e != nil {
			return nil, sdkError("list multipart parts", e)
		}
		for _, part := range o.Parts {
			out = append(out, MultipartPart{PartNumber: int(aws.ToInt32(part.PartNumber)), ETag: normalizeETag(aws.ToString(part.ETag)), Size: aws.ToInt64(part.Size), Checksum: aws.ToString(part.ChecksumSHA256)})
			if len(out) > maxMultipartParts {
				return nil, errors.New("objstore: too many multipart parts")
			}
		}
	}
	return out, nil
}
func (r *Root) CompleteMultipart(ctx context.Context, key, id string, parts []MultipartPart) (TransferReceipt, error) {
	if e := validateDirectObjectKey(r, key); e != nil {
		return TransferReceipt{}, e
	}
	if e := validateUploadID(id); e != nil {
		return TransferReceipt{}, e
	}
	if len(parts) == 0 || len(parts) > maxMultipartParts {
		return TransferReceipt{}, errors.New("objstore: invalid multipart part list")
	}
	if e := r.ensureSDK(); e != nil {
		return TransferReceipt{}, e
	}
	in := &s3.CompleteMultipartUploadInput{Bucket: aws.String(r.cfg.Bucket), Key: aws.String(key), UploadId: aws.String(id), MultipartUpload: &types.CompletedMultipartUpload{Parts: make([]types.CompletedPart, 0, len(parts))}}
	last := 0
	for _, p := range parts {
		if p.PartNumber <= last || p.PartNumber > maxMultipartParts || normalizeETag(p.ETag) == "" {
			return TransferReceipt{}, errors.New("objstore: invalid multipart part list")
		}
		last = p.PartNumber
		partNumber, narrowErr := number.Narrow[int32](p.PartNumber)
		if narrowErr != nil {
			return TransferReceipt{}, narrowErr
		}
		in.MultipartUpload.Parts = append(in.MultipartUpload.Parts, types.CompletedPart{PartNumber: aws.Int32(partNumber), ETag: aws.String(`"` + normalizeETag(p.ETag) + `"`)})
	}
	o, e := r.s3.CompleteMultipartUpload(ctx, in)
	if e != nil {
		return TransferReceipt{}, sdkError("complete multipart upload", e)
	}
	return TransferReceipt{ETag: normalizeETag(aws.ToString(o.ETag))}, nil
}
func (r *Root) PresignUploadPart(ctx context.Context, key, id string, n int, size int64, checksum string, expiry time.Duration) (string, http.Header, error) {
	if e := validateDirectObjectKey(r, key); e != nil {
		return "", nil, e
	}
	if e := validateUploadID(id); e != nil {
		return "", nil, e
	}
	if n < 1 || n > maxMultipartParts || size < 0 {
		return "", nil, errors.New("objstore: invalid multipart part")
	}
	if e := validateChecksum(checksum); e != nil {
		return "", nil, e
	}
	if e := r.ensureSDK(); e != nil {
		return "", nil, e
	}
	secs, e := validateDirectTTL(expiry)
	if e != nil {
		return "", nil, e
	}
	partNumber, narrowErr := number.Narrow[int32](n)
	if narrowErr != nil {
		return "", nil, narrowErr
	}
	in := &s3.UploadPartInput{Bucket: aws.String(r.cfg.Bucket), Key: aws.String(key), UploadId: aws.String(id), PartNumber: aws.Int32(partNumber), ContentLength: aws.Int64(size)}
	hdr := http.Header{}
	if checksum != "" {
		v, checksumErr := checksumHeaderValue(checksum)
		if checksumErr != nil {
			return "", nil, checksumErr
		}
		in.ChecksumSHA256 = aws.String(v)
		hdr.Set("x-amz-checksum-sha256", v)
	}
	o, e := r.sdkPresign.PresignUploadPart(ctx, in, func(p *s3.PresignOptions) { p.Expires = time.Duration(secs) * time.Second })
	if e != nil {
		return "", nil, e
	}
	return o.URL, hdr, nil
}
func (r *Root) ObjectMetadata(ctx context.Context, key string) (uint64, string, string, bool, error) {
	if e := validateDirectObjectKey(r, key); e != nil {
		return 0, "", "", false, e
	}
	if e := r.ensureSDK(); e != nil {
		return 0, "", "", false, e
	}
	o, e := r.s3.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(r.cfg.Bucket), Key: aws.String(key), ChecksumMode: types.ChecksumModeEnabled})
	if e != nil {
		n := sdkError("head object metadata", e)
		if errors.Is(n, vfs.ErrNotFound) {
			return 0, "", "", false, nil
		}
		return 0, "", "", false, n
	}
	sz := aws.ToInt64(o.ContentLength)
	if sz < 0 {
		return 0, "", "", true, errors.New("objstore: invalid object size")
	}
	return uint64(sz), normalizeETag(aws.ToString(o.ETag)), aws.ToString(o.ChecksumSHA256), true, nil
}
func (r *Root) PresignGet(ctx context.Context, key string, expiry time.Duration) (string, error) {
	if e := validateDirectObjectKey(r, key); e != nil {
		return "", e
	}
	if e := r.ensureSDK(); e != nil {
		return "", e
	}
	secs, e := validateDirectTTL(expiry)
	if e != nil {
		return "", e
	}
	o, e := r.sdkPresign.PresignGetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(r.cfg.Bucket), Key: aws.String(key)}, func(p *s3.PresignOptions) { p.Expires = time.Duration(secs) * time.Second })
	if e != nil {
		return "", e
	}
	return o.URL, nil
}

var _ = strconv.Itoa
var _ = fmt.Sprintf
