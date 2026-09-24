//go:build linux

package objstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/heavycaffeiner/stowcloud/go/internal/platform/number"
	"github.com/heavycaffeiner/stowcloud/go/internal/platform/storage/vfs"
)

func (r *Root) requestTarget(key string) (scheme, host, path string) {
	scheme = r.endpointScheme
	if r.cfg.PathStyle {
		host = r.endpointHost
		if key == "" {
			return scheme, host, "/" + r.cfg.Bucket
		}
		return scheme, host, "/" + r.cfg.Bucket + "/" + key
	}
	host = r.cfg.Bucket + "." + r.endpointHost
	if key == "" {
		return scheme, host, "/"
	}
	return scheme, host, "/" + key
}
func (r *Root) listObjectsV2(ctx context.Context, prefix, delimiter, token string, maxKeys int) (*listBucketResult, error) {
	if err := r.ensureSDK(); err != nil {
		return nil, err
	}
	in := &s3.ListObjectsV2Input{Bucket: aws.String(r.cfg.Bucket), Prefix: aws.String(prefix)}
	if delimiter != "" {
		in.Delimiter = aws.String(delimiter)
	}
	if token != "" {
		in.ContinuationToken = aws.String(token)
	}
	if maxKeys >= 0 {
		narrowed, narrowErr := number.Narrow[int32](maxKeys)
		if narrowErr != nil {
			return nil, narrowErr
		}
		in.MaxKeys = aws.Int32(narrowed)
	}
	out, err := r.s3.ListObjectsV2(ctx, in)
	if err != nil {
		return nil, sdkError("list objects", err)
	}
	res := &listBucketResult{IsTruncated: aws.ToBool(out.IsTruncated)}
	if out.NextContinuationToken != nil {
		res.NextContinuationToken = *out.NextContinuationToken
	}
	for _, o := range out.Contents {
		lm := ""
		if o.LastModified != nil {
			lm = o.LastModified.Format(time.RFC3339)
		}
		sz := aws.ToInt64(o.Size)
		if sz < 0 {
			sz = 0
		}
		res.Contents = append(res.Contents, listObject{Key: aws.ToString(o.Key), LastModified: lm, Size: uint64(sz), ETag: aws.ToString(o.ETag)})
	}
	for _, p := range out.CommonPrefixes {
		res.CommonPrefixes = append(res.CommonPrefixes, listCommonPrefix{Prefix: aws.ToString(p.Prefix)})
	}
	if len(res.Contents)+len(res.CommonPrefixes) > maxListEntries {
		return nil, errors.New("objstore: list response carries too many entries")
	}
	for _, o := range res.Contents {
		if _, e := validateListedKey(o.Key, prefix); e != nil {
			return nil, e
		}
	}
	for _, p := range res.CommonPrefixes {
		if _, e := validateListedKey(p.Prefix, prefix); e != nil {
			return nil, e
		}
	}
	return res, nil
}
func (r *Root) headObject(ctx context.Context, key string) (bool, uint64, int64, error) {
	if err := r.ensureSDK(); err != nil {
		return false, 0, 0, err
	}
	o, err := r.s3.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(r.cfg.Bucket), Key: aws.String(key)})
	if err != nil {
		normalized := sdkError("head object", err)
		if errors.Is(normalized, vfs.ErrNotFound) {
			return false, 0, 0, nil
		}
		return false, 0, 0, normalized
	}
	var t int64
	if o.LastModified != nil {
		t = o.LastModified.UnixNano()
	}
	sz := aws.ToInt64(o.ContentLength)
	if sz < 0 {
		sz = 0
	}
	return true, uint64(sz), t, nil
}
func parseListedTime(v string) int64 {
	if t, e := time.Parse(time.RFC3339, v); e == nil {
		return t.UnixNano()
	}
	return 0
}
func (r *Root) exists(ctx context.Context, key string) (bool, error) {
	f, _, _, e := r.headObject(ctx, key)
	return f, e
}
func (r *Root) isDirectory(ctx context.Context, key string) (bool, int64, error) {
	p := key + "/"
	res, e := r.listObjectsV2(ctx, p, "", "", 1)
	if e != nil {
		return false, 0, e
	}
	for _, c := range res.Contents {
		if c.Key == p {
			return true, parseListedTime(c.LastModified), nil
		}
	}
	return len(res.CommonPrefixes) > 0 || len(res.Contents) > 0, 0, nil
}
func (r *Root) deleteObjectForce(ctx context.Context, key string) error {
	if e := r.ensureSDK(); e != nil {
		return e
	}
	_, e := r.s3.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(r.cfg.Bucket), Key: aws.String(key)})
	return sdkError("delete object", e)
}
func (r *Root) copyObject(ctx context.Context, src, dst string) error {
	if e := r.ensureSDK(); e != nil {
		return e
	}
	o, e := r.s3.CopyObject(ctx, &s3.CopyObjectInput{Bucket: aws.String(r.cfg.Bucket), Key: aws.String(dst), CopySource: aws.String(r.cfg.Bucket + "/" + src)})
	if e != nil {
		return sdkError("copy object", e)
	}
	if o == nil || o.CopyObjectResult == nil || aws.ToString(o.CopyObjectResult.ETag) == "" {
		return errors.New("objstore: copy object returned no result")
	}
	return nil
}
func (r *Root) putEmptyObject(ctx context.Context, key string) error {
	if e := r.ensureSDK(); e != nil {
		return e
	}
	_, e := r.s3.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(r.cfg.Bucket), Key: aws.String(key), Body: io.Reader(bytes.NewReader(nil))})
	return sdkError("put object", e)
}
func (r *Root) putObject(ctx context.Context, key string, f *vfs.File, size uint64) error {
	n, e := number.Narrow[int64](size)
	if e != nil {
		return e
	}
	if e = r.ensureSDK(); e != nil {
		return e
	}
	h, e := hashSection(f.OSFile(), n)
	if e != nil {
		return e
	}
	_ = h
	body := io.NewSectionReader(f.OSFile(), 0, n)
	_, e = r.s3.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(r.cfg.Bucket), Key: aws.String(key), Body: body, ContentLength: aws.Int64(n)})
	return sdkError("put object", e)
}
func hashSection(f *os.File, size int64) (string, error) {
	h := sha256.New()
	if _, e := io.Copy(h, io.NewSectionReader(f, 0, size)); e != nil {
		return "", e
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func (r *Root) getObject(ctx context.Context, key string, f *vfs.File) (int64, error) {
	if e := r.ensureSDK(); e != nil {
		return 0, e
	}
	o, e := r.s3.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(r.cfg.Bucket), Key: aws.String(key)})
	if e != nil {
		return 0, sdkError("get object", e)
	}
	n, e := io.Copy(f.OSFile(), io.LimitReader(o.Body, maxObjectBodyBytes+1))
	closeErr := o.Body.Close()
	if e != nil {
		return 0, e
	}
	if closeErr != nil {
		return 0, fmt.Errorf("objstore: closing object body: %w", closeErr)
	}
	if n > maxObjectBodyBytes {
		return 0, fmt.Errorf("objstore: object %q exceeds download ceiling", key)
	}
	return n, nil
}
