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

	"github.com/danielgtaylor/huma/v2"
	"github.com/gin-gonic/gin"

	core "github.com/heavycaffeiner/stowcloud/backend/internal/feature/files"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/api/handler"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/api/humabridge"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/apierr"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/middleware"
	num "github.com/heavycaffeiner/stowcloud/backend/internal/platform/number"
)

const saltLen = 22
const verifierMagic = "RCLONE\x00\x00"
const verifierLen = 67

type Deps struct {
	Core *core.Core
}

type handlers struct{ d Deps }

type listView struct {
	Shares []handler.ShareEncryptionView `json:"shares"`
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

// Register mounts typed encryption operations below the API prefix.
func Register(api huma.API, d Deps) {
	h := &handlers{d: d}
	huma.Register[listInput, listOutput](api, huma.Operation{
		OperationID: "encryption.list", Method: http.MethodGet, Path: "/encryption",
	}, h.listHuma)
	huma.Register[enableInput, noContentOutput](api, huma.Operation{
		OperationID: "admin.encryption.enable", Method: http.MethodPost, Path: "/encryption/{id}",
	}, h.enableHuma)
	huma.Register[shareInput, noContentOutput](api, huma.Operation{
		OperationID: "admin.encryption.disable", Method: http.MethodDelete, Path: "/encryption/{id}",
	}, h.disableHuma)
}

func (h *handlers) listHuma(ctx context.Context, _ *listInput) (*listOutput, error) {
	c := humabridge.Gin(ctx)
	owner, ok := ownerOf(c)
	if !ok {
		return nil, humabridge.Refusal(apierr.Classified{Class: apierr.AuthRequired})
	}
	roots := h.d.Core.Roots(owner)
	order := make([]int64, 0, len(roots))
	labels := make(map[int64][]string, len(roots))
	for _, root := range roots {
		if _, seen := labels[root.Share]; !seen {
			order = append(order, root.Share)
		}
		labels[root.Share] = append(labels[root.Share], root.Label)
	}
	out := make([]handler.ShareEncryptionView, 0, len(order))
	for _, share := range order {
		id, err := num.Narrow[uint32](share)
		if err != nil {
			continue
		}
		enc, found, err := h.d.Core.EncryptionOf(ctx, core.ShareID(id))
		if err != nil {
			return nil, humabridge.Failure(ctx, err)
		}
		if found {
			out = append(out, handler.ShareEncryptionOf(core.ShareID(id), labels[share], enc))
		}
	}
	return &listOutput{Body: listView{Shares: out}}, nil
}

func parseShareID(raw string) (core.ShareID, bool) {
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n <= 0 || uint64(n) > uint64(^uint32(0)) {
		return 0, false
	}
	return core.ShareID(n), true
}

func ownerOf(c *gin.Context) (core.UserID, bool) {
	v, ok := c.Get(string(middleware.KeyCredential))
	p, okp := v.(middleware.Principal)
	if !ok || !okp || p.UserID == 0 {
		return 0, false
	}
	return core.UserID(p.UserID), true
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

func (h *handlers) enableHuma(ctx context.Context, in *enableInput) (*noContentOutput, error) {
	id, ok := parseShareID(in.ID)
	if !ok {
		return nil, humabridge.Failure(ctx, core.ErrNotFound)
	}
	if in.Body.Scheme != core.SchemeRcloneCrypt {
		return nil, humabridge.Refusal(apierr.Classified{Class: apierr.Unprocessable, Key: "encryption.invalid_scheme"})
	}
	if !validSalt(in.Body.Salt) {
		return nil, humabridge.Refusal(apierr.Classified{Class: apierr.Unprocessable, Key: "encryption.invalid_salt"})
	}
	verifier, err := base64.StdEncoding.DecodeString(in.Body.Verifier)
	if err != nil || !validVerifierShape(verifier) {
		return nil, humabridge.Refusal(apierr.Classified{Class: apierr.Unprocessable, Key: "encryption.invalid_verifier"})
	}
	if err := h.d.Core.EnableEncryption(ctx, id, core.Encryption{Scheme: in.Body.Scheme, Salt: in.Body.Salt, Verifier: verifier}); err != nil {
		if errors.Is(err, core.ErrUnprocessable) {
			return nil, humabridge.Refusal(apierr.Classified{Class: apierr.Unprocessable, Key: "encryption.share_not_empty"})
		}
		return nil, humabridge.Failure(ctx, err)
	}
	return &noContentOutput{Status: http.StatusNoContent}, nil
}
func (h *handlers) disableHuma(ctx context.Context, in *shareInput) (*noContentOutput, error) {
	id, ok := parseShareID(in.ID)
	if !ok {
		return nil, humabridge.Failure(ctx, core.ErrNotFound)
	}
	if err := h.d.Core.DisableEncryption(ctx, id); err != nil {
		if errors.Is(err, core.ErrUnprocessable) {
			return nil, humabridge.Refusal(apierr.Classified{Class: apierr.Unprocessable, Key: "encryption.share_not_empty"})
		}
		return nil, humabridge.Failure(ctx, err)
	}
	return &noContentOutput{Status: http.StatusNoContent}, nil
}
