//go:build linux

package links

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/danielgtaylor/huma/v2"
	"github.com/gin-gonic/gin"

	"github.com/heavycaffeiner/stowcloud/backend/internal/feature/auth"
	"github.com/heavycaffeiner/stowcloud/backend/internal/feature/shares/acl"
	"github.com/heavycaffeiner/stowcloud/backend/internal/files"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/api/handler"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/api/humabridge"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/apierr"
	num "github.com/heavycaffeiner/stowcloud/backend/internal/platform/number"
)

type NativeDeps struct {
	Core *files.Core
	Auth interface {
		ListUsers(context.Context) ([]auth.UserRow, error)
	}
	Owner   func(*gin.Context) (files.UserID, bool)
	Resolve func(files.UserID, string, acl.Perms) (files.Resolved, error)
	VpathOf func(files.Link) string
	Now     func() int64
}

type Native struct{ d NativeDeps }

func NewNative(d NativeDeps) *Native { return &Native{d: d} }

type listInput struct {
	Path string `query:"path"`
}

type listOutput struct {
	Body []handler.LinkView
}

type adminListInput struct{}

type adminListOutput struct {
	Body []handler.OwnedLinkView
}

type createInput struct {
	Body createLinkRequest
}

type createOutput struct {
	Body   handler.MintedLinkView
	Status int `status:"201"`
}

type linkIDInput struct {
	ID string `path:"id"`
}

type updateInput struct {
	ID   string `path:"id"`
	Body updateLinkRequest
}

type updateOutput struct {
	Body handler.LinkView
}

type deleteOutput struct {
	Status int `status:"204"`
}

// Register mounts the authenticated link operations below the API prefix.
func Register(api huma.API, d NativeDeps) {
	h := NewNative(d)
	huma.Register[listInput, listOutput](api, huma.Operation{
		OperationID: "links.list", Method: http.MethodGet, Path: "/links",
	}, h.listHuma)
	huma.Register[createInput, createOutput](api, huma.Operation{
		OperationID: "links.create", Method: http.MethodPost, Path: "/links",
	}, h.createHuma)
	huma.Register[updateInput, updateOutput](api, huma.Operation{
		OperationID: "links.update", Method: http.MethodPatch, Path: "/links/{id}",
	}, h.updateHuma)
	huma.Register[linkIDInput, deleteOutput](api, huma.Operation{
		OperationID: "links.delete", Method: http.MethodDelete, Path: "/links/{id}",
	}, h.deleteHuma)
	huma.Register[adminListInput, adminListOutput](api, huma.Operation{
		OperationID: "admin.links.list", Method: http.MethodGet, Path: "/admin/links",
	}, h.adminListHuma)
}

func (h *Native) humaOwner(ctx context.Context) (files.UserID, error) {
	owner, ok := h.d.Owner(humabridge.Gin(ctx))
	if !ok {
		return 0, humabridge.Refusal(apierr.Classified{Class: apierr.AuthRequired})
	}
	return owner, nil
}

func parseLinkID(raw string) (int64, bool) {
	n, err := strconv.ParseInt(raw, 10, 64)
	return n, err == nil && n > 0
}

func (h *Native) listHuma(ctx context.Context, in *listInput) (*listOutput, error) {
	owner, err := h.humaOwner(ctx)
	if err != nil {
		return nil, err
	}
	var at *files.Resolved
	if in.Path != "" {
		r, rerr := h.d.Resolve(owner, in.Path, acl.Read)
		if rerr != nil {
			return nil, humabridge.Failure(ctx, rerr)
		}
		at = &r
	}
	links, err := h.d.Core.ListLinks(ctx, owner, at)
	if err != nil {
		return nil, humabridge.Failure(ctx, err)
	}
	return &listOutput{Body: handler.LinksOf(links, h.d.VpathOf, h.d.Now())}, nil
}

func (h *Native) adminListHuma(ctx context.Context, _ *adminListInput) (*adminListOutput, error) {
	links, err := h.d.Core.ListAllLinks(ctx)
	if err != nil {
		return nil, humabridge.Failure(ctx, err)
	}
	names := map[int64]string{}
	if h.d.Auth != nil {
		if rows, uerr := h.d.Auth.ListUsers(ctx); uerr == nil {
			for _, row := range rows {
				display := row.Display
				if display == "" {
					display = row.Name
				}
				names[row.ID] = display
			}
		}
	}
	return &adminListOutput{Body: handler.OwnedLinksOf(links, names, h.d.VpathOf, h.d.Now())}, nil
}

func (h *Native) createHuma(ctx context.Context, in *createInput) (*createOutput, error) {
	owner, err := h.humaOwner(ctx)
	if err != nil {
		return nil, err
	}
	r, err := h.d.Resolve(owner, in.Body.Path, acl.Share)
	if err != nil {
		return nil, humabridge.Failure(ctx, err)
	}
	expires, ok := optionalNumber[int64](in.Body.Expires, 0)
	if !ok {
		return nil, humabridge.Refusal(apierr.Classified{Class: apierr.Malformed})
	}
	maxDown, ok := optionalNumber[int32](in.Body.MaxDown, unlimitedDownloads)
	if !ok {
		return nil, humabridge.Refusal(apierr.Classified{Class: apierr.Malformed})
	}
	perms, ok := linkPerms(in.Body.Perms)
	if !ok {
		return nil, humabridge.Refusal(apierr.Classified{Class: apierr.Unprocessable})
	}
	link, token, err := h.d.Core.CreateLink(ctx, r, files.LinkSpec{
		Perms: perms, Password: in.Body.Password, Expires: expires, MaxDown: maxDown,
		Label: in.Body.Label, Note: in.Body.Note,
	})
	if err != nil {
		return nil, humabridge.Failure(ctx, err)
	}
	view, ok := handler.MintedLinkOf(link, h.d.VpathOf(link), h.d.Now())
	if !ok {
		return nil, humabridge.Failure(ctx, files.ErrNotFound)
	}
	view.Token = string(token.Reveal())
	return &createOutput{Body: view, Status: http.StatusCreated}, nil
}

func (h *Native) updateHuma(ctx context.Context, in *updateInput) (*updateOutput, error) {
	owner, err := h.humaOwner(ctx)
	if err != nil {
		return nil, err
	}
	id, ok := parseLinkID(in.ID)
	if !ok {
		return nil, humabridge.Failure(ctx, files.ErrNotFound)
	}
	patch, ok := linkPatchOf(in.Body)
	if !ok {
		return nil, humabridge.Refusal(apierr.Classified{Class: apierr.Malformed})
	}
	link, err := h.d.Core.UpdateLink(ctx, owner, id, patch)
	if err != nil {
		return nil, humabridge.Failure(ctx, err)
	}
	return &updateOutput{Body: handler.LinkOf(link, h.d.VpathOf(link), h.d.Now())}, nil
}

func (h *Native) deleteHuma(ctx context.Context, in *linkIDInput) (*deleteOutput, error) {
	owner, err := h.humaOwner(ctx)
	if err != nil {
		return nil, err
	}
	id, ok := parseLinkID(in.ID)
	if !ok {
		return nil, humabridge.Failure(ctx, files.ErrNotFound)
	}
	if err := h.d.Core.DeleteLink(ctx, owner, id); err != nil {
		return nil, humabridge.Failure(ctx, err)
	}
	return &deleteOutput{Status: http.StatusNoContent}, nil
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

type updateLinkRequest struct {
	Password json.RawMessage `json:"password,omitempty"`
	Expires  json.RawMessage `json:"expires_ns,omitempty"`
	MaxDown  json.RawMessage `json:"max_downloads,omitempty"`
	Perms    []string        `json:"perms,omitempty"`
	Label    *string         `json:"label,omitempty"`
	Note     *string         `json:"note,omitempty"`
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

func linkPatchOf(req updateLinkRequest) (files.LinkPatch, bool) {
	patch := files.LinkPatch{Label: req.Label, Note: req.Note}
	if len(req.Perms) > 0 {
		p, ok := linkPerms(req.Perms)
		if !ok {
			return files.LinkPatch{}, false
		}
		patch.Perms = &p
	}
	var ok bool
	if patch.Password, ok = tristate[string](req.Password); !ok {
		return files.LinkPatch{}, false
	}
	if patch.Expires, ok = tristateNumber[int64](req.Expires); !ok {
		return files.LinkPatch{}, false
	}
	if patch.MaxDown, ok = tristateNumber[int32](req.MaxDown); !ok {
		return files.LinkPatch{}, false
	}
	return patch, true
}
