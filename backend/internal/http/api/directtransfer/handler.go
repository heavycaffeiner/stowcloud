//go:build linux

package directtransfer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	feature "github.com/heavycaffeiner/stowcloud/backend/internal/feature/directtransfer"
	core "github.com/heavycaffeiner/stowcloud/backend/internal/feature/files"
	"github.com/heavycaffeiner/stowcloud/backend/internal/feature/shares/acl"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/apierr"
	"github.com/heavycaffeiner/stowcloud/backend/internal/storage/objstore"
	"github.com/heavycaffeiner/stowcloud/backend/internal/store/state"
)

type Handler struct {
	d   Deps
	svc *feature.Service
}

type Deps struct {
	State                 *state.DB
	Owner                 func(*gin.Context) (core.UserID, bool)
	Resolve               func(core.UserID, string, acl.Perms) (core.Resolved, error)
	ShareEncrypted        func(context.Context, core.ShareID) (bool, error)
	GuardLock             func(context.Context, uint32, string, int64) error
	ProviderForRow        func(context.Context, state.DirectTransferReservation) (objstore.DirectTransferProvider, bool, error)
	RevalidateDestination func(context.Context, state.DirectTransferReservation) error
	Now                   func() int64
	Decode                func(*gin.Context, any) error
	Fail                  func(*gin.Context, error)
	Refuse                func(*gin.Context, apierr.Classified)
	NotFound              func(*gin.Context)
	Logger                *slog.Logger
}

func NewHandler(d Deps) *Handler {
	return &Handler{d: d, svc: feature.NewService(feature.Dependencies{
		State: d.State, Resolve: d.Resolve, ShareEncrypted: d.ShareEncrypted,
		GuardLock: d.GuardLock, ProviderForRow: d.ProviderForRow,
		RevalidateDestination: d.RevalidateDestination, Now: d.Now, Logger: d.Logger,
	})}
}

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

func (h *Handler) Create(c *gin.Context) {
	owner, ok := h.d.Owner(c)
	if !ok {
		h.d.Refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	var req directUploadRequest
	if err := h.d.Decode(c, &req); err != nil || strings.TrimSpace(req.Path) == "" {
		h.d.Refuse(c, apierr.Classified{Class: apierr.Malformed})
		return
	}
	size, err := directUint(req.Size)
	if err != nil {
		h.d.Refuse(c, apierr.Classified{Class: apierr.Malformed})
		return
	}
	checksum, err := directChecksum(req.Checksum)
	if err != nil {
		h.d.Refuse(c, apierr.Classified{Class: apierr.Malformed})
		return
	}
	row, err := h.svc.Create(c.Request.Context(), owner, feature.CreateRequest{Path: req.Path, Size: size, Checksum: checksum, IfMatch: stripETag(req.IfMatch)})
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusCreated, directUploadViewOf(row, nil))
}

func (h *Handler) Status(c *gin.Context) {
	owner, ok := h.d.Owner(c)
	if !ok {
		h.d.Refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	id := strings.TrimSpace(c.Param("id"))
	if !validDirectID(id) {
		h.d.NotFound(c)
		return
	}
	row, parts, err := h.svc.Status(c.Request.Context(), owner, id)
	if err != nil {
		if row.ID == "" {
			h.d.NotFound(c)
		} else {
			h.d.Fail(c, err)
		}
		return
	}
	c.JSON(http.StatusOK, directUploadViewOf(row, parts))
}

func (h *Handler) Part(c *gin.Context) {
	owner, ok := h.d.Owner(c)
	if !ok {
		h.d.Refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	id := strings.TrimSpace(c.Param("id"))
	if !validDirectID(id) {
		h.d.NotFound(c)
		return
	}
	var req directPartRequest
	if err := h.d.Decode(c, &req); err != nil {
		h.d.Refuse(c, apierr.Classified{Class: apierr.Malformed})
		return
	}
	part, err := directUint(req.PartNumber)
	if err != nil || part == 0 || part > feature.MaxParts {
		h.d.Refuse(c, apierr.Classified{Class: apierr.Unprocessable})
		return
	}
	result, err := h.svc.PresignPart(c.Request.Context(), owner, id, part, func(expected uint64) (string, error) {
		checksum, checksumErr := directChecksum(req.Checksum)
		if checksumErr != nil {
			return "", apierr.AsClassified(apierr.Malformed, "")
		}
		if len(req.Size) > 0 {
			size, sizeErr := directUint(req.Size)
			if sizeErr != nil || size != expected {
				return "", core.ErrUnprocessable
			}
		}
		return checksum, nil
	})
	if errors.Is(err, state.ErrNoSuchDirectTransfer) {
		h.d.NotFound(c)
		return
	}
	if err != nil {
		var parsed *apierr.ClassifiedError
		if errors.As(err, &parsed) {
			h.d.Refuse(c, parsed.Classified)
		} else {
			h.writeServiceError(c, err)
		}
		return
	}
	c.JSON(http.StatusOK, directPartURLView{PartNumber: strconv.FormatUint(result.Number, 10), URL: result.URL, Headers: result.Headers})
}

func (h *Handler) Complete(c *gin.Context) {
	owner, ok := h.d.Owner(c)
	if !ok {
		h.d.Refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	id := strings.TrimSpace(c.Param("id"))
	if !validDirectID(id) {
		h.d.NotFound(c)
		return
	}
	var req directCompleteRequest
	if err := h.d.Decode(c, &req); err != nil || len(req.Parts) == 0 {
		h.d.Refuse(c, apierr.Classified{Class: apierr.Malformed})
		return
	}
	row, persisted, err := h.svc.Complete(c.Request.Context(), owner, id, func() ([]feature.CompletePart, error) {
		return completePartsOf(req.Parts)
	})
	if errors.Is(err, state.ErrNoSuchDirectTransfer) {
		h.d.NotFound(c)
		return
	}
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, directUploadViewOf(row, persisted))
}

func (h *Handler) Cancel(c *gin.Context) {
	owner, ok := h.d.Owner(c)
	if !ok {
		h.d.Refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	id := strings.TrimSpace(c.Param("id"))
	if !validDirectID(id) {
		h.d.NotFound(c)
		return
	}
	err := h.svc.Cancel(c.Request.Context(), owner, id)
	if errors.Is(err, state.ErrNoSuchDirectTransfer) {
		h.d.NotFound(c)
		return
	}
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) writeServiceError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, feature.ErrUnsupportedSize):
		h.d.Refuse(c, apierr.Classified{Class: apierr.Unprocessable, Key: "transfer.unsupported_size"})
	case errors.Is(err, feature.ErrUnsupported):
		h.d.Refuse(c, apierr.Classified{Class: apierr.NotImplemented, Key: "direct_transfer.unsupported"})
	case errors.Is(err, feature.ErrLocked):
		h.d.Refuse(c, apierr.Classified{Class: apierr.Locked, Key: "dav.locked"})
	case errors.Is(err, feature.ErrExpired):
		h.d.Refuse(c, apierr.Classified{Class: apierr.Gone, Key: "direct_transfer.expired"})
	case errors.Is(err, feature.ErrPartsMismatch):
		h.d.Refuse(c, apierr.Classified{Class: apierr.Unprocessable, Key: "direct_transfer.parts_mismatch"})
	case errors.Is(err, core.ErrUnprocessable):
		h.d.Refuse(c, apierr.Classified{Class: apierr.Unprocessable})
	case errors.Is(err, core.ErrPrecondition):
		h.d.Refuse(c, apierr.Classified{Class: apierr.Precondition, Key: "fs.precondition_failed"})
	default:
		h.d.Fail(c, err)
	}
}

func completePartsOf(in []directCompletePart) ([]feature.CompletePart, error) {
	out := make([]feature.CompletePart, 0, len(in))
	for _, part := range in {
		number, err := directUint(part.PartNumber)
		if err != nil {
			return nil, err
		}
		size, err := directUint(part.Size)
		if err != nil {
			return nil, err
		}
		checksum, err := directChecksum(part.Checksum)
		if err != nil {
			return nil, err
		}
		out = append(out, feature.CompletePart{Number: number, ETag: stripETag(part.ETag), Checksum: checksum, Size: size})
	}
	return out, nil
}
func directUploadViewOf(row state.DirectTransferReservation, parts []state.DirectTransferPart) directUploadView {
	view := directUploadView{ID: row.ID, State: row.State.String(), PartSize: strconv.FormatUint(feature.PartSize, 10), Size: strconv.FormatUint(row.ExpectedSize, 10), ExpiresAt: strconv.FormatInt(row.ExpiresNs, 10), Capability: true}
	if parts != nil {
		view.Parts = make([]directUploadPartView, 0, len(parts))
		for _, part := range parts {
			view.Parts = append(view.Parts, directUploadPartView{PartNumber: strconv.FormatInt(part.PartNumber, 10), ETag: part.ETag, Checksum: part.Checksum, Size: strconv.FormatUint(part.Size, 10), State: partStateName(part.State)})
		}
	}
	return view
}
func partStateName(value state.DirectTransferPartState) string {
	switch value {
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
func directUint(raw json.RawMessage) (uint64, error) {
	if len(raw) == 0 {
		return 0, errors.New("missing integer")
	}
	var value string
	if raw[0] == '"' {
		if err := json.Unmarshal(raw, &value); err != nil {
			return 0, err
		}
	} else {
		value = string(raw)
	}
	return strconv.ParseUint(strings.TrimSpace(value), 10, 64)
}
func directChecksum(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "", nil
	}
	if len(value) != sha256.Size*2 {
		return "", errors.New("checksum is not sha256")
	}
	if _, err := hex.DecodeString(value); err != nil {
		return "", err
	}
	return value, nil
}
func validDirectID(value string) bool {
	if len(value) != 32 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
func stripETag(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "W/")
	return strings.Trim(value, "\"")
}
func (h *Handler) CreateHandler(c *gin.Context)   { h.Create(c) }
func (h *Handler) StatusHandler(c *gin.Context)   { h.Status(c) }
func (h *Handler) PartHandler(c *gin.Context)     { h.Part(c) }
func (h *Handler) CompleteHandler(c *gin.Context) { h.Complete(c) }
func (h *Handler) CancelHandler(c *gin.Context)   { h.Cancel(c) }
