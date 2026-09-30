//go:build linux

// Package encryption serves the share encryption HTTP surface.
package encryption

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/heavycaffeiner/stowcloud/backend/internal/files"
	num "github.com/heavycaffeiner/stowcloud/backend/internal/platform/number"
	"github.com/heavycaffeiner/stowcloud/backend/internal/server/apierr"
)

const saltLen = 22
const verifierMagic = "RCLONE\x00\x00"
const verifierLen = 67

type Handler struct {
	Core *files.Core
}

type listView struct {
	Shares []files.ShareEncryptionView `json:"shares"`
}

type enableRequest struct {
	Scheme   string `json:"scheme"`
	Salt     string `json:"salt"`
	Verifier string `json:"verifier"`
}

type listInput struct{}

type listOutput struct {
	Body listView
}

type shareInput struct {
	ID string `path:"id"`
}

type enableInput struct {
	ID   string `path:"id"`
	Body enableRequest
}

type noContentOutput struct {
	Status int `status:"204"`
}

// List answers the encryption state of the caller's shares.
func (h *Handler) List(ctx context.Context, _ *listInput) (*listOutput, error) {
	owner, err := files.OwnerFrom(ctx)
	if err != nil {
		return nil, err
	}
	roots := h.Core.Roots(owner)
	order := make([]int64, 0, len(roots))
	labels := make(map[int64][]string, len(roots))
	for _, root := range roots {
		if _, seen := labels[root.Share]; !seen {
			order = append(order, root.Share)
		}
		labels[root.Share] = append(labels[root.Share], root.Label)
	}
	out := make([]files.ShareEncryptionView, 0, len(order))
	for _, share := range order {
		id, err := num.Narrow[uint32](share)
		if err != nil {
			continue
		}
		enc, found, err := h.Core.EncryptionOf(ctx, files.ShareID(id))
		if err != nil {
			return nil, err
		}
		if found {
			out = append(out, files.ShareEncryptionOf(files.ShareID(id), labels[share], enc))
		}
	}
	return &listOutput{Body: listView{Shares: out}}, nil
}

func parseShareID(raw string) (files.ShareID, bool) {
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n <= 0 || uint64(n) > uint64(^uint32(0)) {
		return 0, false
	}
	return files.ShareID(n), true
}

func validSalt(s string) bool {
	if len(s) != saltLen {
		return false
	}
	for _, c := range []byte(s) {
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}

func validVerifierShape(decoded []byte) bool {
	return len(decoded) == verifierLen && strings.HasPrefix(string(decoded), verifierMagic)
}

// Enable turns on encryption for an empty share.
func (h *Handler) Enable(ctx context.Context, in *enableInput) (*noContentOutput, error) {
	id, ok := parseShareID(in.ID)
	if !ok {
		return nil, files.ErrNotFound
	}
	if in.Body.Scheme != files.SchemeRcloneCrypt {
		return nil, &apierr.ClassifiedError{Classified: apierr.Classified{Class: apierr.Unprocessable, Key: "encryption.invalid_scheme"}}
	}
	if !validSalt(in.Body.Salt) {
		return nil, &apierr.ClassifiedError{Classified: apierr.Classified{Class: apierr.Unprocessable, Key: "encryption.invalid_salt"}}
	}
	verifier, err := base64.StdEncoding.DecodeString(in.Body.Verifier)
	if err != nil || !validVerifierShape(verifier) {
		return nil, &apierr.ClassifiedError{Classified: apierr.Classified{Class: apierr.Unprocessable, Key: "encryption.invalid_verifier"}}
	}
	if err := h.Core.EnableEncryption(ctx, id, files.Encryption{Scheme: in.Body.Scheme, Salt: in.Body.Salt, Verifier: verifier}); err != nil {
		if errors.Is(err, files.ErrUnprocessable) {
			return nil, &apierr.ClassifiedError{Classified: apierr.Classified{Class: apierr.Unprocessable, Key: "encryption.share_not_empty"}}
		}
		return nil, err
	}
	return &noContentOutput{Status: http.StatusNoContent}, nil
}

// Disable turns off encryption for an empty share.
func (h *Handler) Disable(ctx context.Context, in *shareInput) (*noContentOutput, error) {
	id, ok := parseShareID(in.ID)
	if !ok {
		return nil, files.ErrNotFound
	}
	if err := h.Core.DisableEncryption(ctx, id); err != nil {
		if errors.Is(err, files.ErrUnprocessable) {
			return nil, &apierr.ClassifiedError{Classified: apierr.Classified{Class: apierr.Unprocessable, Key: "encryption.share_not_empty"}}
		}
		return nil, err
	}
	return &noContentOutput{Status: http.StatusNoContent}, nil
}
