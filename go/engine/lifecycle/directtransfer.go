//go:build linux

package lifecycle

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

	"github.com/gofiber/fiber/v2"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/apierr"
	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/objstore"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/state"
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

func (e *Engine) directUploadCreate(c *fiber.Ctx) error {
	owner, ok := ownerOf(c)
	if !ok {
		return refuse(c, apierr.Classified{Class: apierr.AuthRequired})
	}
	var req directUploadRequest
	if err := decodeBody(c, &req); err != nil || strings.TrimSpace(req.Path) == "" {
		return refuse(c, apierr.Classified{Class: apierr.Malformed})
	}
	size, err := directUint(req.Size)
	if err != nil {
		return refuse(c, apierr.Classified{Class: apierr.Malformed})
	}
	checksum, err := directChecksum(req.Checksum)
	if err != nil {
		return refuse(c, apierr.Classified{Class: apierr.Malformed})
	}

	r, err := e.resolve(owner, req.Path, acl.Write|acl.Create)
	if err != nil {
		return fail(c, err)
	}
	if enc, eerr := e.Core.ShareEncrypted(c.UserContext(), r.Share()); eerr != nil {
		return fail(c, eerr)
	} else if enc {
		return refuse(c, apierr.Classified{Class: apierr.Unprocessable})
	}
	provider, ok := directProvider(r.Root())
	if !ok || !provider.DirectTransfer() {
		return refuse(c, apierr.Classified{Class: apierr.NotImplemented, Key: "direct_transfer.unsupported"})
	}
	if lerr := e.guardDavLock(c.UserContext(), uint32(r.Share()), r.Path().String(), int64(owner)); lerr != nil {
		return refuse(c, apierr.Classified{Class: apierr.Locked, Key: "dav.locked"})
	}

	var priorETag string
	var priorSize uint64
	if st, serr := r.Root().Stat(r.Path()); serr == nil {
		if st.Kind.IsDir() {
			return refuse(c, apierr.Classified{Class: apierr.Unprocessable})
		}
		priorETag, _ = coreETag(st)
		priorSize = st.Size
		if req.IfMatch != "" && stripETag(req.IfMatch) != priorETag {
			return refuse(c, apierr.Classified{Class: apierr.Precondition, Key: "fs.precondition_failed"})
		}
	} else if req.IfMatch != "" {
		return refuse(c, apierr.Classified{Class: apierr.Precondition, Key: "fs.precondition_failed"})
	}
	key := provider.ObjectKey(r.Path())
	id, err := directID()
	if err != nil {
		return fail(c, err)
	}
	uploadID, err := provider.BeginMultipart(c.UserContext(), key, int64(size), checksum)
	if err != nil {
		return fail(c, err)
	}
	quota := state.NewQuota(e.State)
	reserved, qerr := quota.Reserve(c.UserContext(), int64(owner), size)
	if qerr != nil || !reserved {
		_ = provider.AbortMultipart(c.UserContext(), key, uploadID)
		if qerr != nil {
			return fail(c, qerr)
		}
		return fail(c, coreErrQuotaExceeded())
	}
	now := e.now()
	row := state.DirectTransferReservation{
		ID: id, Owner: int64(owner), Share: int64(r.Share()), Path: r.Path().String(), ObjectKey: key, UploadID: uploadID,
		ExpectedSize: size, ExpectedChecksum: checksum, IfMatch: stripETag(req.IfMatch), PriorETag: priorETag, PriorSize: priorSize,
		QuotaReservation: size, CreatedNs: now, UpdatedNs: now, ExpiresNs: now + int64(directTransferLifetime), State: state.DirectTransferPending,
	}
	if err := e.State.CreateDirectTransfer(c.UserContext(), row); err != nil {
		_ = provider.AbortMultipart(c.UserContext(), key, uploadID)
		_ = quota.Release(c.UserContext(), int64(owner), int64(size))
		return fail(c, err)
	}
	return writeJSON(c, fiber.StatusCreated, directUploadViewOf(row, nil))
}

func (e *Engine) directUploadStatus(c *fiber.Ctx) error {
	owner, ok := ownerOf(c)
	if !ok {
		return refuse(c, apierr.Classified{Class: apierr.AuthRequired})
	}
	id := strings.TrimSpace(c.Params("id"))
	if !validDirectID(id) {
		return notFound(c)
	}
	row, err := e.State.GetDirectTransferOf(c.UserContext(), int64(owner), id)
	if err != nil {
		return notFound(c)
	}
	parts, err := e.State.ListDirectTransferParts(c.UserContext(), id)
	if err != nil {
		return fail(c, err)
	}
	return writeJSON(c, fiber.StatusOK, directUploadViewOf(row, parts))
}

func (e *Engine) directUploadPart(c *fiber.Ctx) error {
	owner, ok := ownerOf(c)
	if !ok {
		return refuse(c, apierr.Classified{Class: apierr.AuthRequired})
	}
	id := strings.TrimSpace(c.Params("id"))
	if !validDirectID(id) {
		return notFound(c)
	}
	var req directPartRequest
	if err := decodeBody(c, &req); err != nil {
		return refuse(c, apierr.Classified{Class: apierr.Malformed})
	}
	partNumber, err := directUint(req.PartNumber)
	if err != nil || partNumber == 0 || partNumber > directTransferMaxParts {
		return refuse(c, apierr.Classified{Class: apierr.Unprocessable})
	}
	row, err := e.State.GetDirectTransferOf(c.UserContext(), int64(owner), id)
	if errors.Is(err, state.ErrNoSuchDirectTransfer) {
		return notFound(c)
	}
	if err != nil {
		return fail(c, err)
	}
	if row.State != state.DirectTransferPending || e.now() >= row.ExpiresNs {
		return refuse(c, apierr.Classified{Class: apierr.Gone, Key: "direct_transfer.expired"})
	}
	provider, ok, perr := e.directProviderForRow(c.UserContext(), row)
	if perr != nil {
		return fail(c, perr)
	}
	if !ok || !provider.DirectTransfer() {
		return refuse(c, apierr.Classified{Class: apierr.NotImplemented, Key: "direct_transfer.unsupported"})
	}
	expected := directPartSize(row.ExpectedSize, uint64(partNumber))
	if expected == 0 {
		return refuse(c, apierr.Classified{Class: apierr.Unprocessable})
	}
	checksum, err := directChecksum(req.Checksum)
	if err != nil {
		return refuse(c, apierr.Classified{Class: apierr.Malformed})
	}
	if len(req.Size) > 0 {
		if supplied, serr := directUint(req.Size); serr != nil || supplied != expected {
			return refuse(c, apierr.Classified{Class: apierr.Unprocessable})
		}
	}
	url, headers, err := provider.PresignUploadPart(c.UserContext(), row.ObjectKey, row.UploadID, int(partNumber), int64(expected), checksum, 10*time.Minute)
	if err != nil {
		return fail(c, err)
	}
	return writeJSON(c, fiber.StatusOK, directPartURLView{PartNumber: strconv.FormatUint(partNumber, 10), URL: url, Headers: directHeaders(headers)})
}

func (e *Engine) directUploadComplete(c *fiber.Ctx) error {
	owner, ok := ownerOf(c)
	if !ok {
		return refuse(c, apierr.Classified{Class: apierr.AuthRequired})
	}
	id := strings.TrimSpace(c.Params("id"))
	if !validDirectID(id) {
		return notFound(c)
	}
	var req directCompleteRequest
	if err := decodeBody(c, &req); err != nil || len(req.Parts) == 0 {
		return refuse(c, apierr.Classified{Class: apierr.Malformed})
	}
	row, err := e.State.GetDirectTransferOf(c.UserContext(), int64(owner), id)
	if errors.Is(err, state.ErrNoSuchDirectTransfer) {
		return notFound(c)
	}
	if err != nil {
		return fail(c, err)
	}
	if row.State == state.DirectTransferComplete {
		parts, _ := e.State.ListDirectTransferParts(c.UserContext(), id)
		return writeJSON(c, fiber.StatusOK, directUploadViewOf(row, parts))
	}
	provider, ok, perr := e.directProviderForRow(c.UserContext(), row)
	if perr != nil {
		return fail(c, perr)
	}
	if !ok || !provider.DirectTransfer() {
		return refuse(c, apierr.Classified{Class: apierr.NotImplemented, Key: "direct_transfer.unsupported"})
	}
	if row.State != state.DirectTransferPending || e.now() >= row.ExpiresNs {
		return refuse(c, apierr.Classified{Class: apierr.Gone, Key: "direct_transfer.expired"})
	}
	parts, err := e.State.ListDirectTransferParts(c.UserContext(), id)
	if err != nil {
		return fail(c, err)
	}
	listed, err := provider.ListParts(c.UserContext(), row.ObjectKey, row.UploadID)
	if err != nil {
		return fail(c, err)
	}
	clientParts, err := directPartsFromRequest(id, req.Parts, row.ExpectedSize)
	if err != nil {
		return refuse(c, apierr.Classified{Class: apierr.Unprocessable})
	}
	if err := validateDirectParts(row, clientParts, parts, listed); err != nil {
		return refuse(c, apierr.Classified{Class: apierr.Unprocessable, Key: "direct_transfer.parts_mismatch"})
	}
	for _, p := range clientParts {
		if err := e.State.PutDirectTransferPartOf(c.UserContext(), int64(owner), p); err != nil {
			return fail(c, err)
		}
	}
	if err := e.revalidateDirectDestination(c.UserContext(), row); err != nil {
		return fail(c, err)
	}
	if err := provider.CompleteMultipart(c.UserContext(), row.ObjectKey, row.UploadID, directProviderParts(clientParts)); err != nil {
		return fail(c, err)
	}
	size, _, checksum, found, err := provider.ObjectMetadata(c.UserContext(), row.ObjectKey)
	if err != nil {
		return fail(c, err)
	}
	if !found || size != row.ExpectedSize || (row.ExpectedChecksum != "" && checksum != "" && !strings.EqualFold(checksum, row.ExpectedChecksum)) {
		return fail(c, fmt.Errorf("direct transfer completed with unexpected object metadata"))
	}
	now := e.now()
	if err := e.State.CompleteDirectTransfer(c.UserContext(), id, int64(owner), state.DirectTransferComplete, "", "", now); err != nil {
		return fail(c, err)
	}
	if quota, qerr := e.State.ReleaseDirectTransferQuota(c.UserContext(), id); qerr != nil {
		return fail(c, qerr)
	} else if quota > row.PriorSize {
		if qerr := state.NewQuota(e.State).Release(c.UserContext(), int64(owner), int64(quota-row.PriorSize)); qerr != nil {
			return fail(c, qerr)
		}
	}
	row.State = state.DirectTransferComplete
	row.CompletedNs = &now
	return writeJSON(c, fiber.StatusOK, directUploadViewOf(row, clientParts))
}

func (e *Engine) revalidateDirectDestination(ctx context.Context, row state.DirectTransferReservation) error {
	sharePath, err := vfs.ParseSharePath(row.Path)
	if err != nil {
		return core.ErrNotFound
	}
	vpath, err := e.Core.VpathFor(coreUser(row.Owner), core.ShareID(row.Share), sharePath)
	if err != nil {
		return core.ErrNotFound
	}
	r, err := e.resolve(coreUser(row.Owner), vpath.String(), acl.Write|acl.Create)
	if err != nil {
		return err
	}
	if err := e.guardDavLock(ctx, uint32(r.Share()), r.Path().String(), row.Owner); err != nil {
		return err
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

func (e *Engine) directUploadCancel(c *fiber.Ctx) error {
	owner, ok := ownerOf(c)
	if !ok {
		return refuse(c, apierr.Classified{Class: apierr.AuthRequired})
	}
	id := strings.TrimSpace(c.Params("id"))
	if !validDirectID(id) {
		return notFound(c)
	}
	row, err := e.State.GetDirectTransferOf(c.UserContext(), int64(owner), id)
	if err != nil {
		return notFound(c)
	}
	provider, ok, perr := e.directProviderForRow(c.UserContext(), row)
	if perr != nil {
		return fail(c, perr)
	}
	if ok && provider.DirectTransfer() {
		if err := provider.AbortMultipart(c.UserContext(), row.ObjectKey, row.UploadID); err != nil && !errors.Is(err, objstore.ErrDirectTransferUnsupported) {
			return fail(c, err)
		}
	}
	if err := e.State.CancelDirectTransfer(c.UserContext(), id, int64(owner), e.now()); err != nil {
		return fail(c, err)
	}
	if quota, qerr := e.State.ReleaseDirectTransferQuota(c.UserContext(), id); qerr == nil && quota > 0 {
		_ = state.NewQuota(e.State).Release(c.UserContext(), int64(owner), int64(quota))
	}
	return c.SendStatus(fiber.StatusNoContent)
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
	vpath, err := e.Core.VpathFor(coreUser(row.Owner), core.ShareID(row.Share), sharePath)
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
		cp, ok := byNum[int64(p.PartNumber)]
		if !ok || stripETag(p.ETag) != stripETag(cp.ETag) || uint64(p.Size) != cp.Size {
			return errors.New("provider part mismatch")
		}
		if cp.Checksum != "" && !strings.EqualFold(p.Checksum, cp.Checksum) {
			return errors.New("provider part checksum mismatch")
		}
		total += uint64(p.Size)
	}
	if total != row.ExpectedSize {
		return errors.New("total size mismatch")
	}
	return nil
}

func directProviderParts(parts []state.DirectTransferPart) []objstore.MultipartPart {
	out := make([]objstore.MultipartPart, 0, len(parts))
	for _, p := range parts {
		out = append(out, objstore.MultipartPart{PartNumber: int(p.PartNumber), ETag: stripETag(p.ETag), Size: int64(p.Size), Checksum: p.Checksum})
	}
	return out
}

func coreErrQuotaExceeded() error { return core.ErrQuotaExceeded }
