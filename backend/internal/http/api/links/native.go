//go:build linux

package links

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/heavycaffeiner/stowcloud/backend/internal/feature/auth"
	core "github.com/heavycaffeiner/stowcloud/backend/internal/feature/files"
	"github.com/heavycaffeiner/stowcloud/backend/internal/feature/shares/acl"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/api/handler"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/apierr"
	num "github.com/heavycaffeiner/stowcloud/backend/internal/platform/number"
)

type NativeDeps struct {
	Core *core.Core
	Auth interface {
		ListUsers(context.Context) ([]auth.UserRow, error)
	}
	Owner     func(*gin.Context) (core.UserID, bool)
	Admin     func(*gin.Context) (int64, bool)
	Resolve   func(core.UserID, string, acl.Perms) (core.Resolved, error)
	VpathOf   func(core.Link) string
	Now       func() int64
	Decode    func(*gin.Context, any) error
	Fail      func(*gin.Context, error)
	Refuse    func(*gin.Context, apierr.Classified)
	NotFound  func(*gin.Context)
	WriteJSON func(*gin.Context, int, any)
}

type Native struct{ d NativeDeps }

func NewNative(d NativeDeps) *Native { return &Native{d: d} }

func (h *Native) List(c *gin.Context) {
	owner, ok := h.d.Owner(c)
	if !ok {
		h.d.Refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	var at *core.Resolved
	if raw := c.Query("path"); raw != "" {
		r, err := h.d.Resolve(owner, raw, acl.Read)
		if err != nil {
			h.d.Fail(c, err)
			return
		}
		at = &r
	}
	links, err := h.d.Core.ListLinks(c.Request.Context(), owner, at)
	if err != nil {
		h.d.Fail(c, err)
		return
	}
	h.d.WriteJSON(c, http.StatusOK, handler.LinksOf(links, h.d.VpathOf, h.d.Now()))
}

func (h *Native) AdminList(c *gin.Context) {
	if _, ok := h.d.Admin(c); !ok {
		return
	}
	links, err := h.d.Core.ListAllLinks(c.Request.Context())
	if err != nil {
		h.d.Fail(c, err)
		return
	}
	names := map[int64]string{}
	if h.d.Auth != nil {
		if rows, uerr := h.d.Auth.ListUsers(c.Request.Context()); uerr == nil {
			for _, row := range rows {
				display := row.Display
				if display == "" {
					display = row.Name
				}
				names[row.ID] = display
			}
		}
	}
	h.d.WriteJSON(c, http.StatusOK, handler.OwnedLinksOf(links, names, h.d.VpathOf, h.d.Now()))
}

type createLinkRequest struct {
	Path     string          `json:"path"`
	Password *string         `json:"password,omitempty"`
	Label    string          `json:"label,omitempty"`
	Note     string          `json:"note,omitempty"`
	Perms    []string        `json:"perms,omitempty"`
	Expires  json.RawMessage `json:"expires_ns,omitempty"`
	MaxDown  json.RawMessage `json:"max_downloads,omitempty"`
}

const unlimitedDownloads = -1

func (h *Native) Create(c *gin.Context) {
	owner, ok := h.d.Owner(c)
	if !ok {
		h.d.Refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	var req createLinkRequest
	if err := h.d.Decode(c, &req); err != nil {
		h.d.Refuse(c, apierr.Classified{Class: apierr.Malformed})
		return
	}
	r, err := h.d.Resolve(owner, req.Path, acl.Share)
	if err != nil {
		h.d.Fail(c, err)
		return
	}
	expires, ok := optionalNumber[int64](req.Expires, 0)
	if !ok {
		h.d.Refuse(c, apierr.Classified{Class: apierr.Malformed})
		return
	}
	maxDown, ok := optionalNumber[int32](req.MaxDown, unlimitedDownloads)
	if !ok {
		h.d.Refuse(c, apierr.Classified{Class: apierr.Malformed})
		return
	}
	perms, ok := linkPerms(req.Perms)
	if !ok {
		h.d.Refuse(c, apierr.Classified{Class: apierr.Unprocessable})
		return
	}
	link, token, err := h.d.Core.CreateLink(c.Request.Context(), r, core.LinkSpec{Perms: perms, Password: req.Password, Expires: expires, MaxDown: maxDown, Label: req.Label, Note: req.Note})
	if err != nil {
		h.d.Fail(c, err)
		return
	}
	view, ok := handler.MintedLinkOf(link, h.d.VpathOf(link), h.d.Now())
	if !ok {
		h.d.Fail(c, core.ErrNotFound)
		return
	}
	view.Token = string(token.Reveal())
	h.d.WriteJSON(c, http.StatusCreated, view)
}

func (h *Native) Delete(c *gin.Context) {
	owner, ok := h.d.Owner(c)
	if !ok {
		h.d.Refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	id, ok := pathID(c)
	if !ok {
		h.d.NotFound(c)
		return
	}
	if err := h.d.Core.DeleteLink(c.Request.Context(), owner, id); err != nil {
		h.d.Fail(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

type updateLinkRequest struct {
	Password json.RawMessage `json:"password,omitempty"`
	Expires  json.RawMessage `json:"expires_ns,omitempty"`
	MaxDown  json.RawMessage `json:"max_downloads,omitempty"`
	Perms    []string        `json:"perms,omitempty"`
	Label    *string         `json:"label,omitempty"`
	Note     *string         `json:"note,omitempty"`
}

func (h *Native) Update(c *gin.Context) {
	owner, ok := h.d.Owner(c)
	if !ok {
		h.d.Refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	id, ok := pathID(c)
	if !ok {
		h.d.NotFound(c)
		return
	}
	var req updateLinkRequest
	if err := h.d.Decode(c, &req); err != nil {
		h.d.Refuse(c, apierr.Classified{Class: apierr.Malformed})
		return
	}
	patch, ok := linkPatchOf(req)
	if !ok {
		h.d.Refuse(c, apierr.Classified{Class: apierr.Malformed})
		return
	}
	link, err := h.d.Core.UpdateLink(c.Request.Context(), owner, id, patch)
	if err != nil {
		h.d.Fail(c, err)
		return
	}
	h.d.WriteJSON(c, http.StatusOK, handler.LinkOf(link, h.d.VpathOf(link), h.d.Now()))
}

func pathID(c *gin.Context) (int64, bool) {
	n, err := strconv.ParseInt(c.Param("id"), 10, 64)
	return n, err == nil && n > 0
}
func linkPerms(names []string) (acl.Perms, bool) {
	if len(names) == 0 {
		return acl.Read | acl.Download, true
	}
	var p acl.Perms
	for _, name := range names {
		bit, ok := acl.PermByName(name)
		if !ok {
			return 0, false
		}
		p |= bit
	}
	p &= acl.Read | acl.Download | acl.Create
	return p, p != 0
}
func tristate[T any](raw json.RawMessage) (**T, bool) {
	if len(raw) == 0 {
		return nil, true
	}
	if string(raw) == "null" {
		var cleared *T
		return &cleared, true
	}
	var value T
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, false
	}
	set := &value
	return &set, true
}
func optionalNumber[T int64 | int32](raw json.RawMessage, absent T) (T, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return absent, true
	}
	got, ok := tristateNumber[T](raw)
	if !ok || got == nil || *got == nil {
		return absent, false
	}
	return **got, true
}
func tristateNumber[T int64 | int32](raw json.RawMessage) (**T, bool) {
	if len(raw) == 0 {
		return nil, true
	}
	if string(raw) == "null" {
		var cleared *T
		return &cleared, true
	}
	var quoted string
	if err := json.Unmarshal(raw, &quoted); err == nil {
		n, err := strconv.ParseInt(quoted, 10, 64)
		if err != nil {
			return nil, false
		}
		narrowed, err := num.Narrow[T](n)
		if err != nil {
			return nil, false
		}
		set := &narrowed
		return &set, true
	}
	return tristate[T](raw)
}
func linkPatchOf(req updateLinkRequest) (core.LinkPatch, bool) {
	patch := core.LinkPatch{Label: req.Label, Note: req.Note}
	if len(req.Perms) > 0 {
		p, ok := linkPerms(req.Perms)
		if !ok {
			return core.LinkPatch{}, false
		}
		patch.Perms = &p
	}
	var ok bool
	if patch.Password, ok = tristate[string](req.Password); !ok {
		return core.LinkPatch{}, false
	}
	if patch.Expires, ok = tristateNumber[int64](req.Expires); !ok {
		return core.LinkPatch{}, false
	}
	if patch.MaxDown, ok = tristateNumber[int32](req.MaxDown); !ok {
		return core.LinkPatch{}, false
	}
	return patch, true
}
