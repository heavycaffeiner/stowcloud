//go:build linux

package uploads

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/heavycaffeiner/stowcloud/backend/internal/db/state"
	"github.com/heavycaffeiner/stowcloud/backend/internal/files"
	"github.com/heavycaffeiner/stowcloud/backend/internal/server/apierr"
	"github.com/heavycaffeiner/stowcloud/backend/internal/server/middleware"
)

type DirectHandler struct {
	d   DirectDependencies
	svc *DirectService
}

func NewDirectHandler(d DirectDependencies) *DirectHandler {
	return &DirectHandler{d: d, svc: NewDirectService(d)}
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

func (h *DirectHandler) Create(c *gin.Context) {
	owner, ok := files.Owner(c)
	if !ok {
		middleware.Refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	var req directUploadRequest
	if err := middleware.DecodeJSON(c.Request.Body, &req); err != nil || strings.TrimSpace(req.Path) == "" {
		middleware.Refuse(c, apierr.Classified{Class: apierr.Malformed})
		return
	}
	size, err := directUint(req.Size)
	if err != nil {
		middleware.Refuse(c, apierr.Classified{Class: apierr.Malformed})
		return
	}
	checksum, err := directChecksum(req.Checksum)
	if err != nil {
		middleware.Refuse(c, apierr.Classified{Class: apierr.Malformed})
		return
	}
	row, err := h.svc.Create(c.Request.Context(), owner, DirectCreateRequest{Path: req.Path, Size: size, Checksum: checksum, IfMatch: stripETag(req.IfMatch)})
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusCreated, directUploadViewOf(row, nil))
}

func (h *DirectHandler) Status(c *gin.Context) {
	owner, ok := files.Owner(c)
	if !ok {
		middleware.Refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	id := strings.TrimSpace(c.Param("id"))
	if !validDirectID(id) {
		middleware.Fail(c, files.ErrNotFound)
		return
	}
	row, parts, err := h.svc.Status(c.Request.Context(), owner, id)
	if err != nil {
		if row.ID == "" {
			middleware.Fail(c, files.ErrNotFound)
		} else {
			middleware.Fail(c, err)
		}
		return
	}
	c.JSON(http.StatusOK, directUploadViewOf(row, parts))
}

func (h *DirectHandler) Part(c *gin.Context) {
	owner, ok := files.Owner(c)
	if !ok {
		middleware.Refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	id := strings.TrimSpace(c.Param("id"))
	if !validDirectID(id) {
		middleware.Fail(c, files.ErrNotFound)
		return
	}
	var req directPartRequest
	if err := middleware.DecodeJSON(c.Request.Body, &req); err != nil {
		middleware.Refuse(c, apierr.Classified{Class: apierr.Malformed})
		return
	}
	part, err := directUint(req.PartNumber)
	if err != nil || part == 0 || part > MaxParts {
		middleware.Refuse(c, apierr.Classified{Class: apierr.Unprocessable})
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
				return "", files.ErrUnprocessable
			}
		}
		return checksum, nil
	})
	if errors.Is(err, state.ErrNoSuchDirectTransfer) {
		middleware.Fail(c, files.ErrNotFound)
		return
	}
	if err != nil {
		var parsed *apierr.ClassifiedError
		if errors.As(err, &parsed) {
			middleware.Refuse(c, parsed.Classified)
		} else {
			h.writeServiceError(c, err)
		}
		return
	}
	c.JSON(http.StatusOK, directPartURLView{PartNumber: strconv.FormatUint(result.Number, 10), URL: result.URL, Headers: result.Headers})
}

func (h *DirectHandler) Complete(c *gin.Context) {
	owner, ok := files.Owner(c)
	if !ok {
		middleware.Refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	id := strings.TrimSpace(c.Param("id"))
	if !validDirectID(id) {
		middleware.Fail(c, files.ErrNotFound)
		return
	}
	var req directCompleteRequest
	if err := middleware.DecodeJSON(c.Request.Body, &req); err != nil || len(req.Parts) == 0 {
		middleware.Refuse(c, apierr.Classified{Class: apierr.Malformed})
		return
	}
	row, persisted, err := h.svc.Complete(c.Request.Context(), owner, id, func() ([]DirectCompletedPart, error) {
		return completePartsOf(req.Parts)
	})
	if errors.Is(err, state.ErrNoSuchDirectTransfer) {
		middleware.Fail(c, files.ErrNotFound)
		return
	}
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, directUploadViewOf(row, persisted))
}

func (h *DirectHandler) Cancel(c *gin.Context) {
	owner, ok := files.Owner(c)
	if !ok {
		middleware.Refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	id := strings.TrimSpace(c.Param("id"))
	if !validDirectID(id) {
		middleware.Fail(c, files.ErrNotFound)
		return
	}
	err := h.svc.Cancel(c.Request.Context(), owner, id)
	if errors.Is(err, state.ErrNoSuchDirectTransfer) {
		middleware.Fail(c, files.ErrNotFound)
		return
	}
	if err != nil {
		h.writeServiceError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *DirectHandler) writeServiceError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, ErrUnsupportedSize):
		middleware.Refuse(c, apierr.Classified{Class: apierr.Unprocessable, Key: "transfer.unsupported_size"})
	case errors.Is(err, ErrUnsupported):
		middleware.Refuse(c, apierr.Classified{Class: apierr.NotImplemented, Key: "direct_transfer.unsupported"})
	case errors.Is(err, ErrLocked):
		middleware.Refuse(c, apierr.Classified{Class: apierr.Locked, Key: "dav.locked"})
	case errors.Is(err, ErrExpired):
		middleware.Refuse(c, apierr.Classified{Class: apierr.Gone, Key: "direct_transfer.expired"})
	case errors.Is(err, ErrPartsMismatch):
		middleware.Refuse(c, apierr.Classified{Class: apierr.Unprocessable, Key: "direct_transfer.parts_mismatch"})
	case errors.Is(err, files.ErrUnprocessable):
		middleware.Refuse(c, apierr.Classified{Class: apierr.Unprocessable})
	case errors.Is(err, files.ErrPrecondition):
		middleware.Refuse(c, apierr.Classified{Class: apierr.Precondition, Key: "fs.precondition_failed"})
	default:
		middleware.Fail(c, err)
	}
}

func completePartsOf(in []directCompletePart) ([]DirectCompletedPart, error) {
	out := make([]DirectCompletedPart, 0, len(in))
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
		out = append(out, DirectCompletedPart{Number: number, ETag: stripETag(part.ETag), Checksum: checksum, Size: size})
	}
	return out, nil
}
func directUploadViewOf(row state.DirectTransferReservation, parts []state.DirectTransferPart) directUploadView {
	view := directUploadView{ID: row.ID, State: row.State.String(), PartSize: strconv.FormatUint(PartSize, 10), Size: strconv.FormatUint(row.ExpectedSize, 10), ExpiresAt: strconv.FormatInt(row.ExpiresNs, 10), Capability: true}
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
