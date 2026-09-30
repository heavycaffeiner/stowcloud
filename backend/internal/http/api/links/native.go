//go:build linux

package links

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/heavycaffeiner/stowcloud/backend/internal/auth"
	"github.com/heavycaffeiner/stowcloud/backend/internal/files"
	num "github.com/heavycaffeiner/stowcloud/backend/internal/platform/number"
	"github.com/heavycaffeiner/stowcloud/backend/internal/server/apierr"
	"github.com/heavycaffeiner/stowcloud/backend/internal/shares"
	"github.com/heavycaffeiner/stowcloud/backend/internal/shares/acl"
)

// Handler serves the caller's share links, and every link to an
// administrator.
type Handler struct {
	Core *files.Core
	Auth interface {
		ListUsers(context.Context) ([]auth.UserRow, error)
	}
	Resolve func(files.UserID, string, acl.Perms) (files.Resolved, error)
	VpathOf func(files.Link) string
	Now     func() int64
}

type listInput struct {
	Path string `query:"path"`
}

type listOutput struct {
	Body []shares.LinkView
}

type adminListInput struct{}

type adminListOutput struct {
	Body []shares.OwnedLinkView
}

type createInput struct {
	Body createLinkRequest
}

type createOutput struct {
	Body   shares.MintedLinkView
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
	Body shares.LinkView
}

type deleteOutput struct {
	Status int `status:"204"`
}

func parseLinkID(raw string) (int64, bool) {
	n, err := strconv.ParseInt(raw, 10, 64)
	return n, err == nil && n > 0
}

// List answers the caller's links, optionally under one path.
func (h *Handler) List(ctx context.Context, in *listInput) (*listOutput, error) {
	owner, err := files.OwnerFrom(ctx)
	if err != nil {
		return nil, err
	}
	var at *files.Resolved
	if in.Path != "" {
		r, rerr := h.Resolve(owner, in.Path, acl.Read)
		if rerr != nil {
			return nil, rerr
		}
		at = &r
	}
	links, err := h.Core.ListLinks(ctx, owner, at)
	if err != nil {
		return nil, err
	}
	return &listOutput{Body: shares.LinksOf(links, h.VpathOf, h.Now())}, nil
}

// AdminList answers every link with its owner.
func (h *Handler) AdminList(ctx context.Context, _ *adminListInput) (*adminListOutput, error) {
	links, err := h.Core.ListAllLinks(ctx)
	if err != nil {
		return nil, err
	}
	names := map[int64]string{}
	if h.Auth != nil {
		if rows, uerr := h.Auth.ListUsers(ctx); uerr == nil {
			for _, row := range rows {
				display := row.Display
				if display == "" {
					display = row.Name
				}
				names[row.ID] = display
			}
		}
	}
	return &adminListOutput{Body: shares.OwnedLinksOf(links, names, h.VpathOf, h.Now())}, nil
}

// Create mints a link to a path the caller may share.
func (h *Handler) Create(ctx context.Context, in *createInput) (*createOutput, error) {
	owner, err := files.OwnerFrom(ctx)
	if err != nil {
		return nil, err
	}
	r, err := h.Resolve(owner, in.Body.Path, acl.Share)
	if err != nil {
		return nil, err
	}
	expires, ok := optionalNumber[int64](in.Body.Expires, 0)
	if !ok {
		return nil, &apierr.ClassifiedError{Classified: apierr.Classified{Class: apierr.Malformed}}
	}
	maxDown, ok := optionalNumber[int32](in.Body.MaxDown, unlimitedDownloads)
	if !ok {
		return nil, &apierr.ClassifiedError{Classified: apierr.Classified{Class: apierr.Malformed}}
	}
	perms, ok := linkPerms(in.Body.Perms)
	if !ok {
		return nil, &apierr.ClassifiedError{Classified: apierr.Classified{Class: apierr.Unprocessable}}
	}
	link, token, err := h.Core.CreateLink(ctx, r, files.LinkSpec{
		Perms: perms, Password: in.Body.Password, Expires: expires, MaxDown: maxDown,
		Label: in.Body.Label, Note: in.Body.Note,
	})
	if err != nil {
		return nil, err
	}
	view, ok := shares.MintedLinkOf(link, h.VpathOf(link), h.Now())
	if !ok {
		return nil, files.ErrNotFound
	}
	view.Token = string(token.Reveal())
	return &createOutput{Body: view, Status: http.StatusCreated}, nil
}

// Update changes a link's expiry, password or permissions.
func (h *Handler) Update(ctx context.Context, in *updateInput) (*updateOutput, error) {
	owner, err := files.OwnerFrom(ctx)
	if err != nil {
		return nil, err
	}
	id, ok := parseLinkID(in.ID)
	if !ok {
		return nil, files.ErrNotFound
	}
	patch, ok := linkPatchOf(in.Body)
	if !ok {
		return nil, &apierr.ClassifiedError{Classified: apierr.Classified{Class: apierr.Malformed}}
	}
	link, err := h.Core.UpdateLink(ctx, owner, id, patch)
	if err != nil {
		return nil, err
	}
	return &updateOutput{Body: shares.LinkOf(link, h.VpathOf(link), h.Now())}, nil
}

// Delete revokes a link.
func (h *Handler) Delete(ctx context.Context, in *linkIDInput) (*deleteOutput, error) {
	owner, err := files.OwnerFrom(ctx)
	if err != nil {
		return nil, err
	}
	id, ok := parseLinkID(in.ID)
	if !ok {
		return nil, files.ErrNotFound
	}
	if err := h.Core.DeleteLink(ctx, owner, id); err != nil {
		return nil, err
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
