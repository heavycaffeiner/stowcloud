//go:build linux

// Package encryption serves the share encryption HTTP surface.
package encryption

import (
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/heavycaffeiner/stowcloud/go/internal/feature/auth"
	core "github.com/heavycaffeiner/stowcloud/go/internal/feature/files"
	num "github.com/heavycaffeiner/stowcloud/go/internal/platform/number"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/apierr"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/handler"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/middleware"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/route"
)

const saltLen = 22
const verifierMagic = "RCLONE\x00\x00"
const verifierLen = 67

type Deps struct {
	Core *core.Core
	Auth *auth.Service
}

func NewHandlers(d Deps) map[string]gin.HandlerFunc {
	h := &handlers{d: d}
	return map[string]gin.HandlerFunc{
		"encryption.list":          h.list,
		"admin.encryption.enable":  h.enable,
		"admin.encryption.disable": h.disable,
	}
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

func (h *handlers) list(c *gin.Context) {
	owner, ok := ownerOf(c)
	if !ok {
		refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
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
		enc, found, err := h.d.Core.EncryptionOf(c.Request.Context(), core.ShareID(id))
		if err != nil {
			fail(c, err)
			return
		}
		if found {
			out = append(out, handler.ShareEncryptionOf(core.ShareID(id), labels[share], enc))
		}
	}
	json(c, http.StatusOK, listView{Shares: out})
}

func (h *handlers) enable(c *gin.Context) {
	if _, ok := h.admin(c); !ok {
		return
	}
	id, ok := shareIDOf(c)
	if !ok {
		notFound(c)
		return
	}
	var req enableRequest
	if !decode(c, &req) {
		return
	}
	if req.Scheme != core.SchemeRcloneCrypt {
		refuse(c, apierr.Classified{Class: apierr.Unprocessable, Key: "encryption.invalid_scheme"})
		return
	}
	if !validSalt(req.Salt) {
		refuse(c, apierr.Classified{Class: apierr.Unprocessable, Key: "encryption.invalid_salt"})
		return
	}
	verifier, err := base64.StdEncoding.DecodeString(req.Verifier)
	if err != nil || !validVerifierShape(verifier) {
		refuse(c, apierr.Classified{Class: apierr.Unprocessable, Key: "encryption.invalid_verifier"})
		return
	}
	if err := h.d.Core.EnableEncryption(c.Request.Context(), id, core.Encryption{Scheme: req.Scheme, Salt: req.Salt, Verifier: verifier}); err != nil {
		if errors.Is(err, core.ErrUnprocessable) {
			refuse(c, apierr.Classified{Class: apierr.Unprocessable, Key: "encryption.share_not_empty"})
			return
		}
		fail(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *handlers) disable(c *gin.Context) {
	if _, ok := h.admin(c); !ok {
		return
	}
	id, ok := shareIDOf(c)
	if !ok {
		notFound(c)
		return
	}
	if err := h.d.Core.DisableEncryption(c.Request.Context(), id); err != nil {
		if errors.Is(err, core.ErrUnprocessable) {
			refuse(c, apierr.Classified{Class: apierr.Unprocessable, Key: "encryption.share_not_empty"})
			return
		}
		fail(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *handlers) admin(c *gin.Context) (int64, bool) {
	owner, ok := ownerOf(c)
	if !ok {
		refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return 0, false
	}
	isAdmin, err := h.d.Auth.IsAdmin(c.Request.Context(), int64(owner))
	if err != nil {
		fail(c, err)
		return 0, false
	}
	if !isAdmin {
		refuse(c, apierr.Classified{Class: apierr.Denied})
		return 0, false
	}
	return int64(owner), true
}

func ownerOf(c *gin.Context) (core.UserID, bool) {
	v, ok := c.Get(string(middleware.KeyCredential))
	p, okp := v.(middleware.Principal)
	if !ok || !okp || p.UserID == 0 {
		return 0, false
	}
	return core.UserID(p.UserID), true
}

func shareIDOf(c *gin.Context) (core.ShareID, bool) {
	n, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || n <= 0 || uint64(n) > uint64(^uint32(0)) {
		return 0, false
	}
	return core.ShareID(n), true
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

func decode(c *gin.Context, v any) bool {
	if err := middleware.DecodeJSON(middleware.LimitBody(c.Request.Body, route.BodyJSON), v); err != nil {
		refuse(c, apierr.Classified{Class: apierr.Malformed})
		return false
	}
	return true
}

func json(c *gin.Context, status int, v any) { c.JSON(status, v) }
func notFound(c *gin.Context)                { fail(c, core.ErrNotFound) }
func refuse(c *gin.Context, class apierr.Classified) {
	status, body := apierr.REST(class)
	json(c, status, body)
}
func fail(c *gin.Context, err error) {
	middleware.SetCause(c, err)
	refuse(c, apierr.Classify(err, apierr.VisibilityKnown))
}
