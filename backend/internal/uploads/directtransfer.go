//go:build linux

// Package directtransfer owns direct-transfer state transitions, durable quota
// reservations, provider sequencing, expiry, and restart reconciliation.
package uploads

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/heavycaffeiner/stowcloud/backend/internal/db/state"
	"github.com/heavycaffeiner/stowcloud/backend/internal/files"
	"github.com/heavycaffeiner/stowcloud/backend/internal/fs/objstore"
	"github.com/heavycaffeiner/stowcloud/backend/internal/fs/vfs"
	"github.com/heavycaffeiner/stowcloud/backend/internal/platform/clock"
	"github.com/heavycaffeiner/stowcloud/backend/internal/platform/number"
	"github.com/heavycaffeiner/stowcloud/backend/internal/shares/acl"
)

const (
	PartSize = uint64(8 << 20)
	MaxParts = uint64(10000)
	Lifetime = 30 * time.Minute
)

var (
	ErrUnsupported     = errors.New("direct transfer is unsupported")
	ErrUnsupportedSize = errors.New("direct transfer size is unsupported")
	ErrLocked          = errors.New("direct transfer destination is locked")
	ErrExpired         = errors.New("direct transfer is expired")
	ErrPartsMismatch   = errors.New("direct transfer parts do not match")
)

type DirectDependencies struct {
	State                 *state.DB
	Resolve               func(files.UserID, string, acl.Perms) (files.Resolved, error)
	ShareEncrypted        func(context.Context, files.ShareID) (bool, error)
	GuardLock             func(context.Context, uint32, string, int64) error
	ProviderForRow        func(context.Context, state.DirectTransferReservation) (objstore.DirectTransferProvider, bool, error)
	RevalidateDestination func(context.Context, state.DirectTransferReservation) error
	Now                   func() int64
	Logger                *slog.Logger
}

type DirectService struct {
	d DirectDependencies
}

func NewDirectService(d DirectDependencies) *DirectService {
	if d.Now == nil {
		d.Now = clock.System().Nanos
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	return &DirectService{d: d}
}

type DirectCreateRequest struct {
	Path     string
	Size     uint64
	Checksum string
	IfMatch  string
}

type DirectPartResult struct {
	Number  uint64
	URL     string
	Headers map[string]string
}

type DirectCompletedPart struct {
	Number   uint64
	ETag     string
	Checksum string
	Size     uint64
}

func DirectProvider(root vfs.Root) (objstore.DirectTransferProvider, bool) {
	provider, ok := root.(objstore.DirectTransferProvider)
	return provider, ok
}

func DirectProviderForRow(
	coreSvc *files.Core,
	resolve func(files.UserID, string, acl.Perms) (files.Resolved, error),
) func(context.Context, state.DirectTransferReservation) (objstore.DirectTransferProvider, bool, error) {
	return func(_ context.Context, row state.DirectTransferReservation) (objstore.DirectTransferProvider, bool, error) {
		sharePath, err := vfs.ParseSharePath(row.Path)
		if err != nil {
			return nil, false, files.ErrNotFound
		}
		shareID, err := number.Narrow[uint32](row.Share)
		if err != nil {
			return nil, false, files.ErrNotFound
		}
		vpath, err := coreSvc.VpathFor(files.UserID(row.Owner), files.ShareID(shareID), sharePath)
		if err != nil {
			return nil, false, files.ErrNotFound
		}
		resolved, err := resolve(files.UserID(row.Owner), vpath.String(), acl.Write|acl.Create)
		if err != nil {
			return nil, false, err
		}
		provider, ok := DirectProvider(resolved.Root())
		return provider, ok, nil
	}
}

func RevalidateDirectDestination(
	coreSvc *files.Core,
	resolve func(files.UserID, string, acl.Perms) (files.Resolved, error),
	guard func(context.Context, uint32, string, int64) error,
) func(context.Context, state.DirectTransferReservation) error {
	return func(ctx context.Context, row state.DirectTransferReservation) error {
		sharePath, err := vfs.ParseSharePath(row.Path)
		if err != nil {
			return files.ErrNotFound
		}
		shareID, err := number.Narrow[uint32](row.Share)
		if err != nil {
			return files.ErrNotFound
		}
		vpath, err := coreSvc.VpathFor(files.UserID(row.Owner), files.ShareID(shareID), sharePath)
		if err != nil {
			return files.ErrNotFound
		}
		resolved, err := resolve(files.UserID(row.Owner), vpath.String(), acl.Write|acl.Create)
		if err != nil {
			return err
		}
		if guard != nil {
			if guardErr := guard(ctx, uint32(resolved.Share()), resolved.Path().String(), row.Owner); guardErr != nil {
				return guardErr
			}
		}
		st, err := resolved.Root().Stat(resolved.Path())
		if err == nil {
			if st.Kind.IsDir() {
				return files.ErrUnprocessable
			}
			etag, _ := files.FileETag(st)
			if etag != row.PriorETag {
				return files.ErrPrecondition
			}
			return nil
		}
		if row.PriorETag != "" {
			return files.ErrPrecondition
		}
		return nil
	}
}

func (s *DirectService) Create(ctx context.Context, owner files.UserID, req DirectCreateRequest) (state.DirectTransferReservation, error) {
	if req.Size == 0 || req.Size > PartSize*MaxParts {
		return state.DirectTransferReservation{}, ErrUnsupportedSize
	}
	resolved, err := s.d.Resolve(owner, req.Path, acl.Write|acl.Create)
	if err != nil {
		return state.DirectTransferReservation{}, err
	}
	if encrypted, encryptionErr := s.d.ShareEncrypted(ctx, resolved.Share()); encryptionErr != nil {
		return state.DirectTransferReservation{}, encryptionErr
	} else if encrypted {
		return state.DirectTransferReservation{}, files.ErrUnprocessable
	}
	provider, ok := DirectProvider(resolved.Root())
	if !ok || !provider.DirectTransfer() {
		return state.DirectTransferReservation{}, ErrUnsupported
	}
	if guardErr := s.d.GuardLock(ctx, uint32(resolved.Share()), resolved.Path().String(), int64(owner)); guardErr != nil {
		return state.DirectTransferReservation{}, ErrLocked
	}

	var priorETag string
	var priorSize uint64
	if st, statErr := resolved.Root().Stat(resolved.Path()); statErr == nil {
		if st.Kind.IsDir() {
			return state.DirectTransferReservation{}, files.ErrUnprocessable
		}
		priorETag, _ = files.FileETag(st)
		priorSize = st.Size
		if req.IfMatch != "" && req.IfMatch != priorETag {
			return state.DirectTransferReservation{}, files.ErrPrecondition
		}
	} else if req.IfMatch != "" {
		return state.DirectTransferReservation{}, files.ErrPrecondition
	}

	key := provider.ObjectKey(resolved.Path())
	id, err := newID()
	if err != nil {
		return state.DirectTransferReservation{}, err
	}
	signedSize, err := number.Narrow[int64](req.Size)
	if err != nil {
		return state.DirectTransferReservation{}, err
	}
	uploadID, err := provider.BeginMultipart(ctx, key, signedSize, req.Checksum)
	if err != nil {
		return state.DirectTransferReservation{}, err
	}
	quota := state.NewQuota(s.d.State)
	reserved, reserveErr := quota.Reserve(ctx, int64(owner), req.Size)
	if reserveErr != nil || !reserved {
		if abortErr := provider.AbortMultipart(ctx, key, uploadID); abortErr != nil {
			s.d.Logger.Warn("aborting multipart upload failed", "error", abortErr)
		}
		if reserveErr != nil {
			return state.DirectTransferReservation{}, reserveErr
		}
		return state.DirectTransferReservation{}, files.ErrQuotaExceeded
	}

	now := s.d.Now()
	row := state.DirectTransferReservation{
		ID: id, Owner: int64(owner), Share: int64(resolved.Share()), Path: resolved.Path().String(),
		ObjectKey: key, UploadID: uploadID, ExpectedSize: req.Size, ExpectedChecksum: req.Checksum,
		IfMatch: req.IfMatch, PriorETag: priorETag, PriorSize: priorSize,
		QuotaReservation: req.Size, CreatedNs: now, UpdatedNs: now,
		ExpiresNs: now + Lifetime.Nanoseconds(), State: state.DirectTransferPending,
	}
	if err := s.d.State.CreateDirectTransfer(ctx, row); err != nil {
		if abortErr := provider.AbortMultipart(ctx, key, uploadID); abortErr != nil {
			s.d.Logger.Warn("aborting multipart upload failed", "error", abortErr)
		}
		if releaseErr := quota.Release(ctx, int64(owner), signedSize); releaseErr != nil {
			s.d.Logger.Warn("releasing quota failed", "error", releaseErr)
		}
		return state.DirectTransferReservation{}, err
	}
	return row, nil
}

func (s *DirectService) Status(ctx context.Context, owner files.UserID, id string) (state.DirectTransferReservation, []state.DirectTransferPart, error) {
	row, err := s.d.State.GetDirectTransferOf(ctx, int64(owner), id)
	if err != nil {
		return state.DirectTransferReservation{}, nil, err
	}
	parts, err := s.d.State.ListDirectTransferParts(ctx, id)
	return row, parts, err
}

func (s *DirectService) PresignPart(ctx context.Context, owner files.UserID, id string, part uint64, parse func(expected uint64) (string, error)) (DirectPartResult, error) {
	row, err := s.d.State.GetDirectTransferOf(ctx, int64(owner), id)
	if err != nil {
		return DirectPartResult{}, err
	}
	if row.State != state.DirectTransferPending || s.d.Now() >= row.ExpiresNs {
		return DirectPartResult{}, ErrExpired
	}
	provider, ok, err := s.d.ProviderForRow(ctx, row)
	if err != nil {
		return DirectPartResult{}, err
	}
	if !ok || !provider.DirectTransfer() {
		return DirectPartResult{}, ErrUnsupported
	}
	expected := DirectPartSizeFor(row.ExpectedSize, part)
	if expected == 0 {
		return DirectPartResult{}, files.ErrUnprocessable
	}
	partNumber, err := number.Narrow[int](part)
	if err != nil {
		return DirectPartResult{}, err
	}
	checksum, err := parse(expected)
	if err != nil {
		return DirectPartResult{}, err
	}
	partSize, err := number.Narrow[int64](expected)
	if err != nil {
		return DirectPartResult{}, err
	}
	url, headers, err := provider.PresignUploadPart(ctx, row.ObjectKey, row.UploadID, partNumber, partSize, checksum, 10*time.Minute)
	if err != nil {
		return DirectPartResult{}, err
	}
	return DirectPartResult{Number: part, URL: url, Headers: singleHeaders(headers)}, nil
}

func (s *DirectService) Complete(
	ctx context.Context,
	owner files.UserID,
	id string,
	partsOf func() ([]DirectCompletedPart, error),
) (state.DirectTransferReservation, []state.DirectTransferPart, error) {
	row, err := s.d.State.GetDirectTransferOf(ctx, int64(owner), id)
	if err != nil {
		return state.DirectTransferReservation{}, nil, err
	}
	if row.State == state.DirectTransferComplete {
		parts, listErr := s.d.State.ListDirectTransferParts(ctx, id)
		return row, parts, listErr
	}
	if s.d.Now() >= row.ExpiresNs {
		return state.DirectTransferReservation{}, nil, ErrExpired
	}
	provider, ok, err := s.d.ProviderForRow(ctx, row)
	if err != nil {
		return state.DirectTransferReservation{}, nil, err
	}
	if !ok || !provider.DirectTransfer() {
		return state.DirectTransferReservation{}, nil, ErrUnsupported
	}
	if row.State != state.DirectTransferPending && row.State != state.DirectTransferCompleting {
		return state.DirectTransferReservation{}, nil, ErrExpired
	}
	persisted, err := s.d.State.ListDirectTransferParts(ctx, id)
	if err != nil {
		return state.DirectTransferReservation{}, nil, err
	}
	input, err := partsOf()
	if err != nil {
		return state.DirectTransferReservation{}, nil, files.ErrUnprocessable
	}
	client, err := partsFromInput(id, input, row.ExpectedSize)
	if err != nil {
		return state.DirectTransferReservation{}, nil, files.ErrUnprocessable
	}
	if row.State == state.DirectTransferCompleting {
		return s.reconcileCompletion(ctx, owner, row, persisted, provider)
	}
	listed, err := provider.ListParts(ctx, row.ObjectKey, row.UploadID)
	if err != nil {
		return state.DirectTransferReservation{}, nil, err
	}
	if validationErr := validateParts(row, client, persisted, listed); validationErr != nil {
		return state.DirectTransferReservation{}, nil, ErrPartsMismatch
	}
	for _, part := range client {
		if persistErr := s.d.State.PutDirectTransferPartOf(ctx, int64(owner), part); persistErr != nil {
			return state.DirectTransferReservation{}, nil, persistErr
		}
	}
	if destinationErr := s.d.RevalidateDestination(ctx, row); destinationErr != nil {
		return state.DirectTransferReservation{}, nil, destinationErr
	}
	if transitionErr := s.d.State.BeginDirectTransferCompletion(ctx, id, int64(owner), s.d.Now()); transitionErr != nil {
		if errors.Is(transitionErr, state.ErrDirectTransferExpired) {
			return state.DirectTransferReservation{}, nil, ErrExpired
		}
		if errors.Is(transitionErr, state.ErrDirectTransferConflict) {
			return state.DirectTransferReservation{}, nil, ErrLocked
		}
		return state.DirectTransferReservation{}, nil, transitionErr
	}
	if destinationErr := s.d.RevalidateDestination(ctx, row); destinationErr != nil {
		if resetErr := s.d.State.ResetDirectTransferCompletion(ctx, id, int64(owner), s.d.Now()); resetErr != nil {
			return state.DirectTransferReservation{}, nil, errors.Join(destinationErr, resetErr)
		}
		return state.DirectTransferReservation{}, nil, destinationErr
	}
	receipt, completeErr := provider.CompleteMultipart(ctx, row.ObjectKey, row.UploadID, providerParts(client))
	if completeErr != nil {
		size, etag, checksum, found, metadataErr := provider.ObjectMetadata(ctx, row.ObjectKey)
		if metadataErr == nil && !found {
			if resetErr := s.d.State.ResetDirectTransferCompletion(ctx, id, int64(owner), s.d.Now()); resetErr != nil {
				return state.DirectTransferReservation{}, nil, errors.Join(completeErr, resetErr)
			}
			return state.DirectTransferReservation{}, nil, completeErr
		}
		if metadataErr != nil || !metadataMatches(row, size, checksum, found) {
			return state.DirectTransferReservation{}, nil, completeErr
		}
		receipt = objstore.TransferReceipt{Size: size, ETag: etag, Checksum: checksum}
	}
	size, etag, checksum, found, err := provider.ObjectMetadata(ctx, row.ObjectKey)
	if err != nil {
		return state.DirectTransferReservation{}, nil, err
	}
	if !metadataMatches(row, size, checksum, found) {
		return state.DirectTransferReservation{}, nil, fmt.Errorf("direct transfer completed with unexpected object metadata")
	}
	if receipt.Size == 0 {
		receipt = objstore.TransferReceipt{Size: size, ETag: etag, Checksum: checksum}
	}
	return s.publish(ctx, owner, row, client, receipt)
}

func (s *DirectService) reconcileCompletion(ctx context.Context, owner files.UserID, row state.DirectTransferReservation, parts []state.DirectTransferPart, provider objstore.DirectTransferProvider) (state.DirectTransferReservation, []state.DirectTransferPart, error) {
	size, etag, checksum, found, err := provider.ObjectMetadata(ctx, row.ObjectKey)
	if err != nil {
		return state.DirectTransferReservation{}, nil, err
	}
	if !found {
		if rerr := s.d.State.ResetDirectTransferCompletion(ctx, row.ID, int64(owner), s.d.Now()); rerr != nil {
			return state.DirectTransferReservation{}, nil, fmt.Errorf("direct transfer publication is unresolved: %w", rerr)
		}
		return state.DirectTransferReservation{}, nil, fmt.Errorf("direct transfer publication is unresolved")
	}
	if !metadataMatches(row, size, checksum, found) {
		return state.DirectTransferReservation{}, nil, fmt.Errorf("direct transfer publication is unresolved")
	}
	return s.publish(ctx, owner, row, parts, objstore.TransferReceipt{Size: size, ETag: etag, Checksum: checksum})
}

func (s *DirectService) publish(ctx context.Context, owner files.UserID, row state.DirectTransferReservation, parts []state.DirectTransferPart, receipt objstore.TransferReceipt) (state.DirectTransferReservation, []state.DirectTransferPart, error) {
	now := s.d.Now()
	if err := s.d.State.PublishDirectTransferAndReleaseUsage(ctx, row.ID, int64(owner), receipt.Size, receipt.ETag, receipt.Checksum, now, publishRelease(row)); err != nil {
		return state.DirectTransferReservation{}, nil, err
	}
	row.State = state.DirectTransferComplete
	row.CompletedNs = &now
	return row, parts, nil
}

// publishRelease is what publication returns to the owner's usage. Usage
// already counts the replaced file and Create reserved the new size, so the
// replaced bytes are what must come back.
func publishRelease(row state.DirectTransferReservation) uint64 {
	return min(row.QuotaReservation, row.PriorSize)
}
func (s *DirectService) Cancel(ctx context.Context, owner files.UserID, id string) error {
	row, err := s.d.State.GetDirectTransferOf(ctx, int64(owner), id)
	if err != nil {
		return err
	}
	if row.State != state.DirectTransferPending {
		return ErrExpired
	}
	if cancelErr := s.d.State.CancelDirectTransferAndReleaseUsage(ctx, id, int64(owner), s.d.Now(), row.QuotaReservation); cancelErr != nil {
		return cancelErr
	}
	provider, ok, err := s.d.ProviderForRow(ctx, row)
	if err != nil {
		return err
	}
	if ok && provider.DirectTransfer() {
		if abortErr := provider.AbortMultipart(ctx, row.ObjectKey, row.UploadID); abortErr != nil && !errors.Is(abortErr, objstore.ErrDirectTransferUnsupported) {
			return abortErr
		}
	}
	return nil
}

type DirectSweepReport struct {
	Expired    int
	Reconciled int
}

func SweepDirect(ctx context.Context, db *state.DB, now int64, resolve func(context.Context, state.DirectTransferReservation) (objstore.DirectTransferProvider, bool, error), logger *slog.Logger) (DirectSweepReport, error) {
	if logger == nil {
		logger = slog.Default()
	}
	rows, err := db.ListExpiredDirectTransfers(ctx, now, 100)
	if err != nil {
		return DirectSweepReport{}, fmt.Errorf("listing expired direct transfers: %w", err)
	}
	report := DirectSweepReport{}
	for _, row := range rows {
		if provider, ok, resolveErr := resolve(ctx, row); resolveErr == nil && ok && provider.DirectTransfer() {
			if abortErr := provider.AbortMultipart(ctx, row.ObjectKey, row.UploadID); abortErr != nil && !errors.Is(abortErr, objstore.ErrDirectTransferUnsupported) {
				logger.Warn("aborting expired direct transfer failed", "error", abortErr)
				continue
			}
		}
		if expireErr := db.ExpireDirectTransferAndReleaseUsage(ctx, row.ID, now, row.Owner, row.QuotaReservation); expireErr != nil {
			continue
		}
		report.Expired++
	}
	rows, err = db.ListCompletingDirectTransfers(ctx, 100)
	if err != nil {
		return report, fmt.Errorf("listing completing direct transfers: %w", err)
	}
	for _, row := range rows {
		provider, ok, resolveErr := resolve(ctx, row)
		if resolveErr != nil || !ok || !provider.DirectTransfer() {
			continue
		}
		size, etag, checksum, found, metadataErr := provider.ObjectMetadata(ctx, row.ObjectKey)
		if metadataErr != nil {
			continue
		}
		if !found {
			// A completion that left no object is retryable, not stuck.
			if resetErr := db.ResetDirectTransferCompletion(ctx, row.ID, row.Owner, now); resetErr != nil {
				logger.Warn("resetting an unresolved direct transfer failed", "error", resetErr)
			}
			continue
		}
		if !metadataMatches(row, size, checksum, found) {
			continue
		}
		if err := db.PublishDirectTransferAndReleaseUsage(ctx, row.ID, row.Owner, size, etag, checksum, now, publishRelease(row)); err != nil {
			logger.Warn("recording reconciled direct transfer failed", "error", err)
			continue
		}
		report.Reconciled++
	}
	return report, nil
}

func newID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", fmt.Errorf("direct transfer id: %w", err)
	}
	return hex.EncodeToString(id[:]), nil
}

func normalizeETag(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "W/")
	return strings.Trim(value, "\"")
}

func DirectPartSizeFor(total, part uint64) uint64 {
	if part == 0 {
		return 0
	}
	start := (part - 1) * PartSize
	if start >= total {
		return 0
	}
	remaining := total - start
	if remaining > PartSize {
		return PartSize
	}
	return remaining
}

func partsFromInput(id string, input []DirectCompletedPart, total uint64) ([]state.DirectTransferPart, error) {
	parts := make([]state.DirectTransferPart, 0, len(input))
	seen := make(map[uint64]struct{}, len(input))
	for _, part := range input {
		if part.Number == 0 || part.Number > MaxParts || part.Size != DirectPartSizeFor(total, part.Number) || part.ETag == "" {
			return nil, ErrPartsMismatch
		}
		if _, exists := seen[part.Number]; exists {
			return nil, ErrPartsMismatch
		}
		seen[part.Number] = struct{}{}
		parts = append(parts, state.DirectTransferPart{
			TransferID: id, PartNumber: int64(part.Number), ETag: part.ETag,
			Size: part.Size, Checksum: part.Checksum, State: state.DirectTransferPartUploaded,
		})
	}
	sort.Slice(parts, func(i, j int) bool { return parts[i].PartNumber < parts[j].PartNumber })
	return parts, nil
}

func validateParts(row state.DirectTransferReservation, client, persisted []state.DirectTransferPart, listed []objstore.MultipartPart) error {
	if len(client) != len(listed) || len(client) == 0 {
		return ErrPartsMismatch
	}
	byNumber := make(map[int64]state.DirectTransferPart, len(client))
	for _, part := range client {
		if row.ExpectedChecksum != "" && part.Checksum == "" {
			return ErrPartsMismatch
		}
		byNumber[part.PartNumber] = part
	}
	for _, part := range persisted {
		if clientPart, ok := byNumber[part.PartNumber]; ok && part.ETag != "" && normalizeETag(part.ETag) != clientPart.ETag {
			return ErrPartsMismatch
		}
	}
	var total uint64
	for _, part := range listed {
		if part.Size < 0 {
			return ErrPartsMismatch
		}
		size, err := number.Narrow[uint64](part.Size)
		if err != nil {
			return ErrPartsMismatch
		}
		clientPart, ok := byNumber[int64(part.PartNumber)]
		if !ok || normalizeETag(part.ETag) != clientPart.ETag || size != clientPart.Size {
			return ErrPartsMismatch
		}
		if clientPart.Checksum != "" && !checksumsEqual(part.Checksum, clientPart.Checksum) {
			return ErrPartsMismatch
		}
		total += size
	}
	if total != row.ExpectedSize {
		return ErrPartsMismatch
	}
	return nil
}

func providerParts(parts []state.DirectTransferPart) []objstore.MultipartPart {
	out := make([]objstore.MultipartPart, 0, len(parts))
	for _, part := range parts {
		size, err := number.Narrow[int64](part.Size)
		if err != nil {
			size = 0
		}
		out = append(out, objstore.MultipartPart{
			PartNumber: int(part.PartNumber), ETag: part.ETag,
			Size: size, Checksum: part.Checksum,
		})
	}
	return out
}

func metadataMatches(row state.DirectTransferReservation, size uint64, checksum string, found bool) bool {
	return found && size == row.ExpectedSize && (row.ExpectedChecksum == "" || checksumsEqual(checksum, row.ExpectedChecksum))
}

func checksumBytes(value string) ([]byte, bool) {
	value = strings.TrimSpace(value)
	if dash := strings.LastIndexByte(value, '-'); dash >= 0 {
		value = value[:dash]
	}
	if len(value) == 64 {
		decoded, err := hex.DecodeString(value)
		if err == nil {
			return decoded, true
		}
	}
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil || len(decoded) != 32 {
		return nil, false
	}
	return decoded, true
}

func checksumsEqual(left, right string) bool {
	a, aOK := checksumBytes(left)
	b, bOK := checksumBytes(right)
	return aOK && bOK && bytes.Equal(a, b)
}

func singleHeaders(headers map[string][]string) map[string]string {
	out := make(map[string]string, len(headers))
	for name, values := range headers {
		if len(values) == 1 {
			out[name] = values[0]
		}
	}
	return out
}
