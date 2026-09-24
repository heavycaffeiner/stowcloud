//go:build linux

package objstore

import (
	"context"
	"net/http"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/internal/platform/storage/vfs"
	publics3 "github.com/stowcloud/storage/s3"
)

type MultipartPart = publics3.MultipartPart
type TransferReceipt = publics3.TransferReceipt

var ErrDirectTransferUnsupported = publics3.ErrDirectTransferUnsupported

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

func (r *Root) DirectTransfer() bool { return r.backend.DirectTransfer() }
func (r *Root) BeginMultipart(ctx context.Context, key string, size int64, checksum string) (string, error) {
	return r.backend.BeginMultipart(ctx, key, size, checksum)
}
func (r *Root) AbortMultipart(ctx context.Context, key, id string) error {
	return r.backend.AbortMultipart(ctx, key, id)
}
func (r *Root) ListParts(ctx context.Context, key, id string) ([]MultipartPart, error) {
	return r.backend.ListParts(ctx, key, id)
}
func (r *Root) CompleteMultipart(ctx context.Context, key, id string, p []MultipartPart) (TransferReceipt, error) {
	return r.backend.CompleteMultipart(ctx, key, id, p)
}
func (r *Root) PresignUploadPart(ctx context.Context, key, id string, n int, size int64, checksum string, expiry time.Duration) (string, http.Header, error) {
	return r.backend.PresignUploadPart(ctx, key, id, n, size, checksum, expiry)
}
func (r *Root) PresignGet(ctx context.Context, key string, expiry time.Duration) (string, error) {
	return r.backend.PresignGet(ctx, key, expiry)
}
func (r *Root) ObjectMetadata(ctx context.Context, key string) (uint64, string, string, bool, error) {
	return r.backend.ObjectMetadata(ctx, key)
}
