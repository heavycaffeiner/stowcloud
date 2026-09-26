//go:build linux

// Package directtransfer owns direct-transfer state transitions, durable quota
// reservations, provider sequencing, expiry, and restart reconciliation.
package directtransfer

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

	core "github.com/heavycaffeiner/stowcloud/backend/internal/feature/files"
	"github.com/heavycaffeiner/stowcloud/backend/internal/feature/shares/acl"
	"github.com/heavycaffeiner/stowcloud/backend/internal/platform/clock"
	"github.com/heavycaffeiner/stowcloud/backend/internal/platform/number"
	"github.com/heavycaffeiner/stowcloud/backend/internal/storage/objstore"
	"github.com/heavycaffeiner/stowcloud/backend/internal/storage/vfs"
	"github.com/heavycaffeiner/stowcloud/backend/internal/store/state"
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

type Dependencies struct {
	State                 *state.DB
	Resolve               func(core.UserID, string, acl.Perms) (core.Resolved, error)
	ShareEncrypted        func(context.Context, core.ShareID) (bool, error)
	GuardLock             func(context.Context, uint32, string, int64) error
	ProviderForRow        func(context.Context, state.DirectTransferReservation) (objstore.DirectTransferProvider, bool, error)
	RevalidateDestination func(context.Context, state.DirectTransferReservation) error
	Now                   func() int64
	Logger                *slog.Logger
}

type Service struct {
	d Dependencies
}

func NewService(d Dependencies) *Service {
	if d.Now == nil {
		d.Now = clock.System().Nanos
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	return &Service{d: d}
}

type CreateRequest struct {
	Path     string
	Size     uint64
	Checksum string
	IfMatch  string
}

type PartResult struct {
	Number  uint64
	URL     string
	Headers map[string]string
}

type CompletePart struct {
	Number   uint64
	ETag     string
	Checksum string
	Size     uint64
}

func Provider(root vfs.Root) (objstore.DirectTransferProvider, bool) {
	provider, ok := root.(objstore.DirectTransferProvider)
	return provider, ok
}

func ProviderForRow(
	coreSvc *core.Core,
	resolve func(core.UserID, string, acl.Perms) (core.Resolved, error),
) func(context.Context, state.DirectTransferReservation) (objstore.DirectTransferProvider, bool, error) {
	return func(_ context.Context, row state.DirectTransferReservation) (objstore.DirectTransferProvider, bool, error) {
		sharePath, err := vfs.ParseSharePath(row.Path)
		if err != nil {
			return nil, false, core.ErrNotFound
		}
		shareID, err := number.Narrow[uint32](row.Share)
		if err != nil {
			return nil, false, core.ErrNotFound
		}
		vpath, err := coreSvc.VpathFor(core.UserID(row.Owner), core.ShareID(shareID), sharePath)
		if err != nil {
			return nil, false, core.ErrNotFound
		}
		resolved, err := resolve(core.UserID(row.Owner), vpath.String(), acl.Write|acl.Create)
		if err != nil {
			return nil, false, err
		}
		provider, ok := Provider(resolved.Root())
		return provider, ok, nil
	}
}

func RevalidateDestination(
	coreSvc *core.Core,
	resolve func(core.UserID, string, acl.Perms) (core.Resolved, error),
	guard func(context.Context, uint32, string, int64) error,
) func(context.Context, state.DirectTransferReservation) error {
	return func(ctx context.Context, row state.DirectTransferReservation) error {
		sharePath, err := vfs.ParseSharePath(row.Path)
		if err != nil {
			return core.ErrNotFound
		}
		shareID, err := number.Narrow[uint32](row.Share)
		if err != nil {
			return core.ErrNotFound
		}
		vpath, err := coreSvc.VpathFor(core.UserID(row.Owner), core.ShareID(shareID), sharePath)
		if err != nil {
			return core.ErrNotFound
		}
		resolved, err := resolve(core.UserID(row.Owner), vpath.String(), acl.Write|acl.Create)
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
				return core.ErrUnprocessable
			}
			etag, _ := core.FileETag(st)
			if etag != row.PriorETag {
				return core.ErrPrecondition
			}
			return nil
		}
		if row.PriorETag != "" {
			return core.ErrPrecondition
		}
		return nil
	}
}

func (s *Service) Create(ctx context.Context, owner core.UserID, req CreateRequest) (state.DirectTransferReservation, error) {
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
		return state.DirectTransferReservation{}, core.ErrUnprocessable
	}
	provider, ok := Provider(resolved.Root())
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
			return state.DirectTransferReservation{}, core.ErrUnprocessable
		}
		priorETag, _ = core.FileETag(st)
		priorSize = st.Size
		if req.IfMatch != "" && req.IfMatch != priorETag {
			return state.DirectTransferReservation{}, core.ErrPrecondition
		}
	} else if req.IfMatch != "" {
		return state.DirectTransferReservation{}, core.ErrPrecondition
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
		return state.DirectTransferReservation{}, core.ErrQuotaExceeded
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

func (s *Service) Status(ctx context.Context, owner core.UserID, id string) (state.DirectTransferReservation, []state.DirectTransferPart, error) {
	row, err := s.d.State.GetDirectTransferOf(ctx, int64(owner), id)
	if err != nil {
		return state.DirectTransferReservation{}, nil, err
	}
	parts, err := s.d.State.ListDirectTransferParts(ctx, id)
	return row, parts, err
}

func (s *Service) PresignPart(ctx context.Context, owner core.UserID, id string, part uint64, parse func(expected uint64) (string, error)) (PartResult, error) {
	row, err := s.d.State.GetDirectTransferOf(ctx, int64(owner), id)
	if err != nil {
		return PartResult{}, err
	}
	if row.State != state.DirectTransferPending || s.d.Now() >= row.ExpiresNs {
		return PartResult{}, ErrExpired
	}
	provider, ok, err := s.d.ProviderForRow(ctx, row)
	if err != nil {
		return PartResult{}, err
	}
	if !ok || !provider.DirectTransfer() {
		return PartResult{}, ErrUnsupported
	}
	expected := PartSizeFor(row.ExpectedSize, part)
	if expected == 0 {
		return PartResult{}, core.ErrUnprocessable
	}
	partNumber, err := number.Narrow[int](part)
	if err != nil {
		return PartResult{}, err
	}
	checksum, err := parse(expected)
	if err != nil {
		return PartResult{}, err
	}
	partSize, err := number.Narrow[int64](expected)
	if err != nil {
		return PartResult{}, err
	}
	url, headers, err := provider.PresignUploadPart(ctx, row.ObjectKey, row.UploadID, partNumber, partSize, checksum, 10*time.Minute)
	if err != nil {
		return PartResult{}, err
	}
	return PartResult{Number: part, URL: url, Headers: singleHeaders(headers)}, nil
}

func (s *Service) Complete(
	ctx context.Context,
	owner core.UserID,
	id string,
	partsOf func() ([]CompletePart, error),
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
		return state.DirectTransferReservation{}, nil, core.ErrUnprocessable
	}
	client, err := partsFromInput(id, input, row.ExpectedSize)
	if err != nil {
		return state.DirectTransferReservation{}, nil, core.ErrUnprocessable
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

func (s *Service) reconcileCompletion(ctx context.Context, owner core.UserID, row state.DirectTransferReservation, parts []state.DirectTransferPart, provider objstore.DirectTransferProvider) (state.DirectTransferReservation, []state.DirectTransferPart, error) {
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

func (s *Service) publish(ctx context.Context, owner core.UserID, row state.DirectTransferReservation, parts []state.DirectTransferPart, receipt objstore.TransferReceipt) (state.DirectTransferReservation, []state.DirectTransferPart, error) {
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
func (s *Service) Cancel(ctx context.Context, owner core.UserID, id string) error {
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

type SweepReport struct {
	Expired    int
	Reconciled int
}

func Sweep(ctx context.Context, db *state.DB, now int64, resolve func(context.Context, state.DirectTransferReservation) (objstore.DirectTransferProvider, bool, error), logger *slog.Logger) (SweepReport, error) {
	if logger == nil {
		logger = slog.Default()
	}
	rows, err := db.ListExpiredDirectTransfers(ctx, now, 100)
	if err != nil {
		return SweepReport{}, fmt.Errorf("listing expired direct transfers: %w", err)
	}
	report := SweepReport{}
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

func PartSizeFor(total, part uint64) uint64 {
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

func partsFromInput(id string, input []CompletePart, total uint64) ([]state.DirectTransferPart, error) {
	parts := make([]state.DirectTransferPart, 0, len(input))
	seen := make(map[uint64]struct{}, len(input))
	for _, part := range input {
		if part.Number == 0 || part.Number > MaxParts || part.Size != PartSizeFor(total, part.Number) || part.ETag == "" {
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
