//go:build linux

package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/heavycaffeiner/stowcloud/go/internal/feature/files"
	"github.com/heavycaffeiner/stowcloud/go/internal/feature/shares/acl"
	"github.com/heavycaffeiner/stowcloud/go/internal/kit/num"
	"github.com/heavycaffeiner/stowcloud/go/internal/platform/database/state"
	"github.com/heavycaffeiner/stowcloud/go/internal/platform/storage/objstore"
	"github.com/heavycaffeiner/stowcloud/go/internal/platform/storage/vfs"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/apierr"
)

const (
	directTransferLifetime = 30 * time.Minute
	directTransferPartSize = 8 << 20
	directTransferMaxParts = 10000
)

type directUploadRequest struct {
	Path     string          `json:"path"`
	Size     json.RawMessage `json:"size"`
	Checksum string          `json:"checksum"`
	IfMatch  string          `json:"if_match"`
}

type directPartRequest struct {
	PartNumber json.RawMessage `json:"part_number"`
	Size       json.RawMessage `json:"size"`
	Checksum   string          `json:"checksum"`
}

type directCompleteRequest struct {
	Parts []directCompletePart `json:"parts"`
}

type directCompletePart struct {
	PartNumber json.RawMessage `json:"part_number"`
	ETag       string          `json:"etag"`
	Checksum   string          `json:"checksum"`
	Size       json.RawMessage `json:"size"`
}

type directUploadView struct {
	ID         string                 `json:"id"`
	State      string                 `json:"state"`
	PartSize   string                 `json:"part_size"`
	Size       string                 `json:"size"`
	ExpiresAt  string                 `json:"expires_at"`
	Capability bool                   `json:"capability"`
	Parts      []directUploadPartView `json:"parts,omitempty"`
}

type directUploadPartView struct {
	PartNumber string `json:"part_number"`
	ETag       string `json:"etag,omitempty"`
	Checksum   string `json:"checksum,omitempty"`
	Size       string `json:"size"`
	State      string `json:"state,omitempty"`
}

type directPartURLView struct {
	PartNumber string            `json:"part_number"`
	URL        string            `json:"url"`
	Headers    map[string]string `json:"headers"`
}

func (e *Engine) directUploadCreate(c *gin.Context) {
	owner, ok := ownerOf(c)
	if !ok {
		refuse(c, apierr.Classified{Class: apierr.AuthRequired})
	}
	var req directUploadRequest
	if err := decodeBody(c, &req); err != nil || strings.TrimSpace(req.Path) == "" {
		refuse(c, apierr.Classified{Class: apierr.Malformed})
	}
	size, err := directUint(req.Size)
	if err != nil {
		refuse(c, apierr.Classified{Class: apierr.Malformed})
	}
	checksum, err := directChecksum(req.Checksum)
	if err != nil {
		refuse(c, apierr.Classified{Class: apierr.Malformed})
	}

	r, err := e.resolve(owner, req.Path, acl.Write|acl.Create)
	if err != nil {
		fail(c, err)
	}
	if enc, eerr := e.Core.ShareEncrypted(c.Request.Context(), r.Share()); eerr != nil {
		fail(c, eerr)
	} else if enc {
		refuse(c, apierr.Classified{Class: apierr.Unprocessable})
	}
	provider, ok := directProvider(r.Root())
	if !ok || !provider.DirectTransfer() {
		refuse(c, apierr.Classified{Class: apierr.NotImplemented, Key: "direct_transfer.unsupported"})
	}
	if lerr := e.guardDavLock(c.Request.Context(), uint32(r.Share()), r.Path().String(), int64(owner)); lerr != nil {
		refuse(c, apierr.Classified{Class: apierr.Locked, Key: "dav.locked"})
	}

	var priorETag string
	var priorSize uint64
	if st, serr := r.Root().Stat(r.Path()); serr == nil {
		if st.Kind.IsDir() {
			refuse(c, apierr.Classified{Class: apierr.Unprocessable})
		}
		priorETag, _ = coreETag(st)
		priorSize = st.Size
		if req.IfMatch != "" && stripETag(req.IfMatch) != priorETag {
			refuse(c, apierr.Classified{Class: apierr.Precondition, Key: "fs.precondition_failed"})
		}
	} else if req.IfMatch != "" {
		refuse(c, apierr.Classified{Class: apierr.Precondition, Key: "fs.precondition_failed"})
	}
	key := provider.ObjectKey(r.Path())
	id, err := directID()
	if err != nil {
		fail(c, err)
	}
	signedSize, nerr := num.Narrow[int64](size)
	if nerr != nil {
		fail(c, nerr)
	}
	uploadID, err := provider.BeginMultipart(c.Request.Context(), key, signedSize, checksum)
	if err != nil {
		fail(c, err)
	}
	quota := state.NewQuota(e.State)
	reserved, qerr := quota.Reserve(c.Request.Context(), int64(owner), size)
	if qerr != nil || !reserved {
		if aerr := provider.AbortMultipart(c.Request.Context(), key, uploadID); aerr != nil {
			e.logger.Warn("aborting multipart upload failed", "error", aerr)
		}
		if qerr != nil {
			fail(c, qerr)
		}
		fail(c, coreErrQuotaExceeded())
	}
	now := e.now()
	row := state.DirectTransferReservation{
		ID: id, Owner: int64(owner), Share: int64(r.Share()), Path: r.Path().String(), ObjectKey: key, UploadID: uploadID,
		ExpectedSize: size, ExpectedChecksum: checksum, IfMatch: stripETag(req.IfMatch), PriorETag: priorETag, PriorSize: priorSize,
		QuotaReservation: size, CreatedNs: now, UpdatedNs: now, ExpiresNs: now + int64(directTransferLifetime), State: state.DirectTransferPending,
	}
	if err := e.State.CreateDirectTransfer(c.Request.Context(), row); err != nil {
		if aerr := provider.AbortMultipart(c.Request.Context(), key, uploadID); aerr != nil {
			e.logger.Warn("aborting multipart upload failed", "error", aerr)
		}
		if relErr := quota.Release(c.Request.Context(), int64(owner), signedSize); relErr != nil {
			e.logger.Warn("releasing quota failed", "error", relErr)
		}
		fail(c, err)
	}
	writeJSON(c, http.StatusCreated, directUploadViewOf(row, nil))
}

func (e *Engine) directUploadStatus(c *gin.Context) {
	owner, ok := ownerOf(c)
	if !ok {
		refuse(c, apierr.Classified{Class: apierr.AuthRequired})
	}
	id := strings.TrimSpace(c.Param("id"))
	if !validDirectID(id) {
		notFound(c)
		return
	}
	row, err := e.State.GetDirectTransferOf(c.Request.Context(), int64(owner), id)
	if err != nil {
		notFound(c)
		return
	}
	parts, err := e.State.ListDirectTransferParts(c.Request.Context(), id)
	if err != nil {
		fail(c, err)
	}
	writeJSON(c, http.StatusOK, directUploadViewOf(row, parts))
}

func (e *Engine) directUploadPart(c *gin.Context) {
	owner, ok := ownerOf(c)
	if !ok {
		refuse(c, apierr.Classified{Class: apierr.AuthRequired})
	}
	id := strings.TrimSpace(c.Param("id"))
	if !validDirectID(id) {
		notFound(c)
		return
	}
	var req directPartRequest
	if err := decodeBody(c, &req); err != nil {
		refuse(c, apierr.Classified{Class: apierr.Malformed})
	}
	partNumber, err := directUint(req.PartNumber)
	if err != nil || partNumber == 0 || partNumber > directTransferMaxParts {
		refuse(c, apierr.Classified{Class: apierr.Unprocessable})
	}
	row, err := e.State.GetDirectTransferOf(c.Request.Context(), int64(owner), id)
	if errors.Is(err, state.ErrNoSuchDirectTransfer) {
		notFound(c)
		return
	}
	if err != nil {
		fail(c, err)
	}
	if row.State != state.DirectTransferPending || e.now() >= row.ExpiresNs {
		refuse(c, apierr.Classified{Class: apierr.Gone, Key: "direct_transfer.expired"})
	}
	provider, ok, perr := e.directProviderForRow(c.Request.Context(), row)
	if perr != nil {
		fail(c, perr)
	}
	if !ok || !provider.DirectTransfer() {
		refuse(c, apierr.Classified{Class: apierr.NotImplemented, Key: "direct_transfer.unsupported"})
	}
	expected := directPartSize(row.ExpectedSize, uint64(partNumber))
	if expected == 0 {
		refuse(c, apierr.Classified{Class: apierr.Unprocessable})
	}
	checksum, err := directChecksum(req.Checksum)
	if err != nil {
		refuse(c, apierr.Classified{Class: apierr.Malformed})
	}
	if len(req.Size) > 0 {
		if supplied, serr := directUint(req.Size); serr != nil || supplied != expected {
			refuse(c, apierr.Classified{Class: apierr.Unprocessable})
		}
	}
	expSize, nerr := num.Narrow[int64](expected)
	if nerr != nil {
		refuse(c, apierr.Classified{Class: apierr.Unprocessable})
	}
	partNumberInt, nerr := num.Narrow[int](partNumber)
	if nerr != nil {
		refuse(c, apierr.Classified{Class: apierr.Unprocessable})
	}
	url, headers, err := provider.PresignUploadPart(c.Request.Context(), row.ObjectKey, row.UploadID, partNumberInt, expSize, checksum, 10*time.Minute)
	if err != nil {
		fail(c, err)
	}
	writeJSON(c, http.StatusOK, directPartURLView{PartNumber: strconv.FormatUint(partNumber, 10), URL: url, Headers: directHeaders(headers)})
}

func (e *Engine) directUploadComplete(c *gin.Context) {
	owner, ok := ownerOf(c)
	if !ok {
		refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	id := strings.TrimSpace(c.Param("id"))
	if !validDirectID(id) {
		notFound(c)
		return
	}
	var req directCompleteRequest
	if err := decodeBody(c, &req); err != nil || len(req.Parts) == 0 {
		refuse(c, apierr.Classified{Class: apierr.Malformed})
		return
	}
	row, err := e.State.GetDirectTransferOf(c.Request.Context(), int64(owner), id)
	if errors.Is(err, state.ErrNoSuchDirectTransfer) {
		notFound(c)
		return
	}
	if err != nil {
		fail(c, err)
		return
	}
	if row.State == state.DirectTransferComplete {
		parts, perr := e.State.ListDirectTransferParts(c.Request.Context(), id)
		if perr != nil {
			e.logger.Warn("listing direct transfer parts failed", "error", perr)
		}
		writeJSON(c, http.StatusOK, directUploadViewOf(row, parts))
		return
	}
	provider, ok, perr := e.directProviderForRow(c.Request.Context(), row)
	if perr != nil {
		fail(c, perr)
		return
	}
	if !ok || !provider.DirectTransfer() {
		refuse(c, apierr.Classified{Class: apierr.NotImplemented, Key: "direct_transfer.unsupported"})
		return
	}
	if row.State != state.DirectTransferPending && row.State != state.DirectTransferCompleting {
		refuse(c, apierr.Classified{Class: apierr.Gone, Key: "direct_transfer.expired"})
		return
	}
	parts, err := e.State.ListDirectTransferParts(c.Request.Context(), id)
	if err != nil {
		fail(c, err)
		return
	}
	clientParts, err := directPartsFromRequest(id, req.Parts, row.ExpectedSize)
	if err != nil {
		refuse(c, apierr.Classified{Class: apierr.Unprocessable})
		return
	}
	if row.State == state.DirectTransferCompleting {
		// The provider commit may already have succeeded. Reconcile metadata;
		// never issue CompleteMultipart a second time for the same upload.
		size, etag, checksum, found, merr := provider.ObjectMetadata(c.Request.Context(), row.ObjectKey)
		if merr != nil {
			fail(c, merr)
			return
		}
		if !found || size != row.ExpectedSize || (row.ExpectedChecksum != "" && (checksum == "" || !strings.EqualFold(checksum, row.ExpectedChecksum))) {
			fail(c, fmt.Errorf("direct transfer publication is unresolved"))
			return
		}
		now := e.now()
		if err := e.State.PublishDirectTransfer(c.Request.Context(), id, int64(owner), size, etag, checksum, now); err != nil {
			fail(c, err)
			return
		}
		if quota, qerr := e.State.ReleaseDirectTransferQuota(c.Request.Context(), id); qerr != nil {
			fail(c, qerr)
			return
		} else if quota > row.PriorSize {
			relAmount, nerr := num.Narrow[int64](quota - row.PriorSize)
			if nerr != nil {
				fail(c, nerr)
				return
			}
			if qerr := state.NewQuota(e.State).Release(c.Request.Context(), int64(owner), relAmount); qerr != nil {
				fail(c, qerr)
				return
			}
		}
		row.State, row.CompletedNs = state.DirectTransferComplete, &now
		writeJSON(c, http.StatusOK, directUploadViewOf(row, parts))
		return
	}
	listed, lerr := provider.ListParts(c.Request.Context(), row.ObjectKey, row.UploadID)
	if lerr != nil {
		fail(c, lerr)
		return
	}
	if verr := validateDirectParts(row, clientParts, parts, listed); verr != nil {
		refuse(c, apierr.Classified{Class: apierr.Unprocessable, Key: "direct_transfer.parts_mismatch"})
		return
	}
	for _, p := range clientParts {
		if perr := e.State.PutDirectTransferPartOf(c.Request.Context(), int64(owner), p); perr != nil {
			fail(c, perr)
			return
		}
	}
	if rerr := e.revalidateDirectDestination(c.Request.Context(), row); rerr != nil {
		fail(c, rerr)
		return
	}
	if ierr := e.State.BeginDirectTransferCompletion(c.Request.Context(), id, int64(owner), e.now()); ierr != nil {
		fail(c, ierr)
		return
	}
	row.State = state.DirectTransferCompleting
	receipt, cerr := provider.CompleteMultipart(c.Request.Context(), row.ObjectKey, row.UploadID, directProviderParts(clientParts))
	if cerr != nil {
		// A timeout is ambiguous: inspect the object before reporting failure.
		size, etag, checksum, found, merr := provider.ObjectMetadata(c.Request.Context(), row.ObjectKey)
		if merr != nil || !found || size != row.ExpectedSize || (row.ExpectedChecksum != "" && (checksum == "" || !strings.EqualFold(checksum, row.ExpectedChecksum))) {
			fail(c, cerr)
			return
		}
		receipt = objstore.TransferReceipt{Size: size, ETag: etag, Checksum: checksum}
	}
	size, etag, checksum, found, merr := provider.ObjectMetadata(c.Request.Context(), row.ObjectKey)
	if merr != nil {
		fail(c, merr)
		return
	}
	if !found || size != row.ExpectedSize || (row.ExpectedChecksum != "" && (checksum == "" || !strings.EqualFold(checksum, row.ExpectedChecksum))) {
		fail(c, fmt.Errorf("direct transfer completed with unexpected object metadata"))
		return
	}
	if receipt.Size == 0 {
		receipt = objstore.TransferReceipt{Size: size, ETag: etag, Checksum: checksum}
	}
	now := e.now()
	if err := e.State.PublishDirectTransfer(c.Request.Context(), id, int64(owner), receipt.Size, receipt.ETag, receipt.Checksum, now); err != nil {
		fail(c, err)
		return
	}
	if quota, qerr := e.State.ReleaseDirectTransferQuota(c.Request.Context(), id); qerr != nil {
		fail(c, qerr)
		return
	} else if quota > row.PriorSize {
		relAmount, nerr := num.Narrow[int64](quota - row.PriorSize)
		if nerr != nil {
			fail(c, nerr)
			return
		}
		if qerr := state.NewQuota(e.State).Release(c.Request.Context(), int64(owner), relAmount); qerr != nil {
			fail(c, qerr)
			return
		}
	}
	row.State = state.DirectTransferComplete
	row.CompletedNs = &now
	writeJSON(c, http.StatusOK, directUploadViewOf(row, clientParts))
}

func (e *Engine) revalidateDirectDestination(ctx context.Context, row state.DirectTransferReservation) error {
	sharePath, err := vfs.ParseSharePath(row.Path)
	if err != nil {
		return core.ErrNotFound
	}
	shareID, serr := num.Narrow[uint32](row.Share)
	if serr != nil {
		return core.ErrNotFound
	}
	vpath, err := e.Core.VpathFor(coreUser(row.Owner), core.ShareID(shareID), sharePath)
	if err != nil {
		return core.ErrNotFound
	}
	r, err := e.resolve(coreUser(row.Owner), vpath.String(), acl.Write|acl.Create)
	if err != nil {
		return err
	}
	if gerr := e.guardDavLock(ctx, uint32(r.Share()), r.Path().String(), row.Owner); gerr != nil {
		return gerr
	}
	st, err := r.Root().Stat(r.Path())
	if err == nil {
		if st.Kind.IsDir() {
			return core.ErrUnprocessable
		}
		current, _ := coreETag(st)
		if current != row.PriorETag {
			return core.ErrPrecondition
		}
		return nil
	}
	if row.PriorETag != "" {
		return core.ErrPrecondition
	}
	return nil
}

func (e *Engine) directUploadCancel(c *gin.Context) {
	owner, ok := ownerOf(c)
	if !ok {
		refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	id := strings.TrimSpace(c.Param("id"))
	if !validDirectID(id) {
		notFound(c)
		return
	}
	row, err := e.State.GetDirectTransferOf(c.Request.Context(), int64(owner), id)
	if err != nil {
		notFound(c)
		return
	}
	if row.State != state.DirectTransferPending {
		refuse(c, apierr.Classified{Class: apierr.Gone, Key: "direct_transfer.expired"})
		return
	}
	provider, ok, perr := e.directProviderForRow(c.Request.Context(), row)
	if perr != nil {
		fail(c, perr)
		return
	}
	if ok && provider.DirectTransfer() {
		if err := provider.AbortMultipart(c.Request.Context(), row.ObjectKey, row.UploadID); err != nil && !errors.Is(err, objstore.ErrDirectTransferUnsupported) {
			fail(c, err)
			return
		}
	}
	if err := e.State.CancelDirectTransfer(c.Request.Context(), id, int64(owner), e.now()); err != nil {
		fail(c, err)
		return
	}
	if quota, qerr := e.State.ReleaseDirectTransferQuota(c.Request.Context(), id); qerr == nil && quota > 0 {
		if relAmount, nerr := num.Narrow[int64](quota); nerr == nil {
			if relErr := state.NewQuota(e.State).Release(c.Request.Context(), int64(owner), relAmount); relErr != nil {
				e.logger.Warn("releasing direct transfer quota failed", "error", relErr)
			}
		}
	}
	c.Status(http.StatusNoContent)
}

func directProvider(root vfs.Root) (objstore.DirectTransferProvider, bool) {
	p, ok := root.(objstore.DirectTransferProvider)
	return p, ok
}

func (e *Engine) directProviderForRow(ctx context.Context, row state.DirectTransferReservation) (objstore.DirectTransferProvider, bool, error) {
	sharePath, err := vfs.ParseSharePath(row.Path)
	if err != nil {
		return nil, false, core.ErrNotFound
	}
	shareID, serr := num.Narrow[uint32](row.Share)
	if serr != nil {
		return nil, false, core.ErrNotFound
	}
	vpath, err := e.Core.VpathFor(coreUser(row.Owner), core.ShareID(shareID), sharePath)
	if err != nil {
		return nil, false, core.ErrNotFound
	}
	r, err := e.resolve(coreUser(row.Owner), vpath.String(), acl.Write|acl.Create)
	if err != nil {
		return nil, false, err
	}
	p, ok := directProvider(r.Root())
	return p, ok, nil
}
func directHeaders(headers http.Header) map[string]string {
	out := make(map[string]string, len(headers))
	for name, values := range headers {
		if len(values) == 1 {
			out[name] = values[0]
		}
	}
	return out
}

func coreUser(n int64) core.UserID { return core.UserID(n) }

func directUploadViewOf(row state.DirectTransferReservation, parts []state.DirectTransferPart) directUploadView {
	v := directUploadView{ID: row.ID, State: directStateName(row.State), PartSize: strconv.FormatUint(directTransferPartSize, 10), Size: strconv.FormatUint(row.ExpectedSize, 10), ExpiresAt: strconv.FormatInt(row.ExpiresNs, 10), Capability: true}
	if parts != nil {
		v.Parts = make([]directUploadPartView, 0, len(parts))
		for _, p := range parts {
			v.Parts = append(v.Parts, directUploadPartView{PartNumber: strconv.FormatInt(p.PartNumber, 10), ETag: p.ETag, Checksum: p.Checksum, Size: strconv.FormatUint(p.Size, 10), State: directPartStateName(p.State)})
		}
	}
	return v
}

func directStateName(s state.DirectTransferState) string {
	switch s {
	case state.DirectTransferPending:
		return "pending"
	case state.DirectTransferCompleting:
		return "completing"
	case state.DirectTransferComplete:
		return "complete"
	case state.DirectTransferCancelled:
		return "cancelled"
	case state.DirectTransferExpired:
		return "expired"
	default:
		return "unknown"
	}
}

func directPartStateName(s state.DirectTransferPartState) string {
	switch s {
	case state.DirectTransferPartPending:
		return "pending"
	case state.DirectTransferPartUploaded:
		return "uploaded"
	case state.DirectTransferPartComplete:
		return "complete"
	default:
		return "unknown"
	}
}

func directID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("direct transfer id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

func validDirectID(s string) bool {
	if len(s) != 32 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

func directUint(raw json.RawMessage) (uint64, error) {
	if len(raw) == 0 {
		return 0, errors.New("missing integer")
	}
	var s string
	if raw[0] == '"' {
		if err := json.Unmarshal(raw, &s); err != nil {
			return 0, err
		}
	} else {
		s = string(raw)
	}
	n, err := strconv.ParseUint(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0, err
	}
	return n, nil
}

func directChecksum(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return "", nil
	}
	if len(s) != sha256.Size*2 {
		return "", errors.New("checksum is not sha256")
	}
	if _, err := hex.DecodeString(s); err != nil {
		return "", err
	}
	return s, nil
}

func stripETag(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "W/")
	return strings.Trim(s, "\"")
}

func coreETag(st vfs.Stat) (string, bool) {
	return core.FileETag(st)
}

func directPartSize(total, part uint64) uint64 {
	if part == 0 {
		return 0
	}
	start := (part - 1) * directTransferPartSize
	if start >= total {
		return 0
	}
	remain := total - start
	if remain > directTransferPartSize {
		return directTransferPartSize
	}
	return remain
}

func directPartsFromRequest(id string, in []directCompletePart, total uint64) ([]state.DirectTransferPart, error) {
	out := make([]state.DirectTransferPart, 0, len(in))
	seen := make(map[uint64]struct{}, len(in))
	for _, p := range in {
		n, err := directUint(p.PartNumber)
		if err != nil || n == 0 || n > directTransferMaxParts {
			return nil, errors.New("bad part number")
		}
		sz, err := directUint(p.Size)
		if err != nil || sz != directPartSize(total, n) || p.ETag == "" {
			return nil, errors.New("bad part")
		}
		if _, ok := seen[n]; ok {
			return nil, errors.New("duplicate part")
		}
		seen[n] = struct{}{}
		checksum, err := directChecksum(p.Checksum)
		if err != nil {
			return nil, err
		}
		out = append(out, state.DirectTransferPart{TransferID: id, PartNumber: int64(n), ETag: stripETag(p.ETag), Size: sz, Checksum: checksum, State: state.DirectTransferPartUploaded})
	}
	return out, nil
}

func validateDirectParts(row state.DirectTransferReservation, client, persisted []state.DirectTransferPart, listed []objstore.MultipartPart) error {
	if len(client) != len(listed) || len(client) == 0 {
		return errors.New("part count mismatch")
	}
	byNum := make(map[int64]state.DirectTransferPart, len(client))
	for _, p := range client {
		if row.ExpectedChecksum != "" && p.Checksum == "" {
			return errors.New("part checksum is required")
		}
		byNum[p.PartNumber] = p
	}
	for _, p := range persisted {
		if cp, ok := byNum[p.PartNumber]; ok && p.ETag != "" && stripETag(p.ETag) != stripETag(cp.ETag) {
			return errors.New("persisted part mismatch")
		}
	}
	var total uint64
	for _, p := range listed {
		if p.Size < 0 {
			return errors.New("provider part has negative size")
		}
		pSize, nerr := num.Narrow[uint64](p.Size)
		if nerr != nil {
			return errors.New("provider part size does not fit")
		}
		cp, ok := byNum[int64(p.PartNumber)]
		if !ok || stripETag(p.ETag) != stripETag(cp.ETag) || pSize != cp.Size {
			return errors.New("provider part mismatch")
		}
		if cp.Checksum != "" && !strings.EqualFold(p.Checksum, cp.Checksum) {
			return errors.New("provider part checksum mismatch")
		}
		total += pSize
	}
	if total != row.ExpectedSize {
		return errors.New("total size mismatch")
	}
	return nil
}

func directProviderParts(parts []state.DirectTransferPart) []objstore.MultipartPart {
	out := make([]objstore.MultipartPart, 0, len(parts))
	for _, p := range parts {
		pSize, err := num.Narrow[int64](p.Size)
		if err != nil {
			pSize = 0
		}
		out = append(out, objstore.MultipartPart{PartNumber: int(p.PartNumber), ETag: stripETag(p.ETag), Size: pSize, Checksum: p.Checksum})
	}
	return out
}

func coreErrQuotaExceeded() error { return core.ErrQuotaExceeded }
