//go:build linux

package uploads

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/heavycaffeiner/stowcloud/backend/internal/db/state"
	"github.com/heavycaffeiner/stowcloud/backend/internal/files"
	"github.com/heavycaffeiner/stowcloud/backend/internal/server/apierr"
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

type directCreateInput struct{ Body directUploadRequest }
type directIDInput struct {
	ID string `path:"id"`
}
type directPartInput struct {
	ID   string `path:"id"`
	Body directPartRequest
}
type directCompleteInput struct {
	ID   string `path:"id"`
	Body directCompleteRequest
}
type directUploadOutput struct {
	Status int
	Body   directUploadView
}
type directPartURLOutput struct{ Body directPartURLView }
type directNoContentOutput struct {
	Status int
}

func (h *DirectHandler) Create(ctx context.Context, in *directCreateInput) (*directUploadOutput, error) {
	owner, err := files.OwnerFrom(ctx)
	if err != nil {
		return nil, err
	}
	req := in.Body
	if strings.TrimSpace(req.Path) == "" {
		return nil, apierr.AsClassified(apierr.Malformed, "")
	}
	size, err := directUint(req.Size)
	if err != nil {
		return nil, apierr.AsClassified(apierr.Malformed, "")
	}
	checksum, err := directChecksum(req.Checksum)
	if err != nil {
		return nil, apierr.AsClassified(apierr.Malformed, "")
	}
	row, err := h.svc.Create(ctx, owner, DirectCreateRequest{Path: req.Path, Size: size, Checksum: checksum, IfMatch: stripETag(req.IfMatch)})
	if err != nil {
		return nil, directErr(err)
	}
	return &directUploadOutput{Status: http.StatusCreated, Body: directUploadViewOf(row, nil)}, nil
}

func (h *DirectHandler) Status(ctx context.Context, in *directIDInput) (*directUploadOutput, error) {
	owner, err := files.OwnerFrom(ctx)
	if err != nil {
		return nil, err
	}
	id := strings.TrimSpace(in.ID)
	if !validDirectID(id) {
		return nil, files.ErrNotFound
	}
	row, parts, err := h.svc.Status(ctx, owner, id)
	if err != nil {
		if row.ID == "" {
			return nil, files.ErrNotFound
		}
		return nil, err
	}
	return &directUploadOutput{Status: http.StatusOK, Body: directUploadViewOf(row, parts)}, nil
}

func (h *DirectHandler) Part(ctx context.Context, in *directPartInput) (*directPartURLOutput, error) {
	owner, err := files.OwnerFrom(ctx)
	if err != nil {
		return nil, err
	}
	id := strings.TrimSpace(in.ID)
	if !validDirectID(id) {
		return nil, files.ErrNotFound
	}
	req := in.Body
	part, err := directUint(req.PartNumber)
	if err != nil || part == 0 || part > MaxParts {
		return nil, apierr.AsClassified(apierr.Unprocessable, "")
	}
	result, err := h.svc.PresignPart(ctx, owner, id, part, func(expected uint64) (string, error) {
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
	if err != nil {
		return nil, directErr(err)
	}
	return &directPartURLOutput{Body: directPartURLView{PartNumber: strconv.FormatUint(result.Number, 10), URL: result.URL, Headers: result.Headers}}, nil
}

func (h *DirectHandler) Complete(ctx context.Context, in *directCompleteInput) (*directUploadOutput, error) {
	owner, err := files.OwnerFrom(ctx)
	if err != nil {
		return nil, err
	}
	id := strings.TrimSpace(in.ID)
	if !validDirectID(id) {
		return nil, files.ErrNotFound
	}
	if len(in.Body.Parts) == 0 {
		return nil, apierr.AsClassified(apierr.Malformed, "")
	}
	row, persisted, err := h.svc.Complete(ctx, owner, id, func() ([]DirectCompletedPart, error) {
		return completePartsOf(in.Body.Parts)
	})
	if err != nil {
		return nil, directErr(err)
	}
	return &directUploadOutput{Status: http.StatusOK, Body: directUploadViewOf(row, persisted)}, nil
}

func (h *DirectHandler) Cancel(ctx context.Context, in *directIDInput) (*directNoContentOutput, error) {
	owner, err := files.OwnerFrom(ctx)
	if err != nil {
		return nil, err
	}
	id := strings.TrimSpace(in.ID)
	if !validDirectID(id) {
		return nil, files.ErrNotFound
	}
	if err = h.svc.Cancel(ctx, owner, id); err != nil {
		return nil, directErr(err)
	}
	return &directNoContentOutput{Status: http.StatusNoContent}, nil
}

// directErr classifies a direct transfer service failure.
func directErr(err error) error {
	switch {
	case errors.Is(err, state.ErrNoSuchDirectTransfer):
		return files.ErrNotFound
	case errors.Is(err, ErrUnsupportedSize):
		return apierr.AsClassified(apierr.Unprocessable, "transfer.unsupported_size")
	case errors.Is(err, ErrUnsupported):
		return apierr.AsClassified(apierr.NotImplemented, "direct_transfer.unsupported")
	case errors.Is(err, ErrLocked):
		return apierr.AsClassified(apierr.Locked, "dav.locked")
	case errors.Is(err, ErrExpired):
		return apierr.AsClassified(apierr.Gone, "direct_transfer.expired")
	case errors.Is(err, ErrPartsMismatch):
		return apierr.AsClassified(apierr.Unprocessable, "direct_transfer.parts_mismatch")
	case errors.Is(err, files.ErrUnprocessable):
		return apierr.AsClassified(apierr.Unprocessable, "")
	case errors.Is(err, files.ErrPrecondition):
		return apierr.AsClassified(apierr.Precondition, "fs.precondition_failed")
	default:
		return err
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
