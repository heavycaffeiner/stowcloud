//go:build linux && compat_nc

package lifecycle

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/middleware"
	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
)

type trashEntryItem struct {
	shareID int64
	entry   core.TrashEntry
}

func (e *Engine) serveDavTrash(w http.ResponseWriter, r *http.Request, p middleware.Principal, rest string) {
	rest = strings.TrimPrefix(rest, "/")
	switch {
	case rest == "trash" || rest == "trash/":
		if r.Method == "PROPFIND" {
			if !p.Mask.IsEmpty() && !p.Mask.Has(acl.Read) {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			e.serveDavTrashList(w, r, p)
			return
		}
		if r.Method == http.MethodDelete {
			if !p.Mask.IsEmpty() && !p.Mask.Has(acl.Delete) {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			e.serveDavTrashEmpty(w, r, p)
			return
		}
		w.Header().Set("Allow", "PROPFIND, DELETE")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return

	case strings.HasPrefix(rest, "trash/"):
		rawID := strings.TrimPrefix(rest, "trash/")
		if r.Method == http.MethodDelete {
			if !p.Mask.IsEmpty() && !p.Mask.Has(acl.Delete) {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			e.serveDavTrashPurge(w, r, p, rawID)
			return
		}
		if r.Method == "MOVE" {
			if !p.Mask.IsEmpty() && !p.Mask.Has(acl.Create) {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			e.serveDavTrashRestore(w, r, p, rawID)
			return
		}
		w.Header().Set("Allow", "DELETE, MOVE")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return

	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (e *Engine) serveDavTrashList(w http.ResponseWriter, r *http.Request, p middleware.Principal) {
	user := core.UserID(p.UserID)
	roots := e.Core.Roots(user)
	var items []trashEntryItem
	for _, rt := range roots {
		if len(p.Shares) > 0 && !slices.Contains(p.Shares, rt.Label) {
			continue
		}
		vp, verr := vfs.ParseVpath("/" + rt.Label)
		if verr != nil {
			continue
		}
		resolved, rerr := e.Core.Resolve(user, vp, acl.Read)
		if rerr != nil {
			continue
		}
		list, lerr := e.Core.TrashList(r.Context(), resolved)
		if lerr != nil {
			continue
		}
		for _, te := range list {
			items = append(items, trashEntryItem{shareID: rt.Share, entry: te})
		}
	}

	w.Header().Set("Content-Type", `application/xml; charset="utf-8"`)
	w.WriteHeader(http.StatusMultiStatus)
	var buf []byte
	buf = append(buf, "<?xml version=\"1.0\" encoding=\"utf-8\"?>\n"...)
	buf = append(buf, "<d:multistatus xmlns:d=\"DAV:\" xmlns:nc=\"http://nextcloud.org/ns\">\n"...)

	buf = append(buf, "  <d:response>\n"...)
	buf = append(buf, "    <d:href>"+xmlEscapeString(r.URL.Path)+"</d:href>\n"...)
	buf = append(buf, "    <d:propstat>\n"...)
	buf = append(buf, "      <d:prop><d:resourcetype><d:collection/></d:resourcetype></d:prop>\n"...)
	buf = append(buf, "      <d:status>HTTP/1.1 200 OK</d:status>\n"...)
	buf = append(buf, "    </d:propstat>\n"...)
	buf = append(buf, "  </d:response>\n"...)

	for _, it := range items {
		shareQualID := fmt.Sprintf("%d:%s", it.shareID, it.entry.ID)
		delSec := it.entry.DeletedAtNs / 1e9
		title := it.entry.Name
		if title == "" {
			title = leafOfTrash(it.entry.OrigPath)
		}
		resType := ""
		if it.entry.IsDir {
			resType = "<d:collection/>"
		}

		buf = append(buf, "  <d:response>\n"...)
		buf = append(buf, "    <d:href>/remote.php/dav/trashbin/trash/"+xmlEscapeString(shareQualID)+"</d:href>\n"...)
		buf = append(buf, "    <d:propstat>\n"...)
		buf = append(buf, "      <d:prop>\n"...)
		buf = append(buf, "        <d:resourcetype>"+resType+"</d:resourcetype>\n"...)
		buf = append(buf, fmt.Sprintf("        <d:getcontentlength>%d</d:getcontentlength>\n", it.entry.Size)...)
		buf = append(buf, "        <nc:trashbin-filename>"+xmlEscapeString(shareQualID)+"</nc:trashbin-filename>\n"...)
		buf = append(buf, "        <nc:trashbin-original-location>"+xmlEscapeString(it.entry.OrigPath)+"</nc:trashbin-original-location>\n"...)
		buf = append(buf, fmt.Sprintf("        <nc:trashbin-deletion-time>%d</nc:trashbin-deletion-time>\n", delSec)...)
		buf = append(buf, "        <nc:trashbin-title>"+xmlEscapeString(title)+"</nc:trashbin-title>\n"...)
		buf = append(buf, "      </d:prop>\n"...)
		buf = append(buf, "      <d:status>HTTP/1.1 200 OK</d:status>\n"...)
		buf = append(buf, "    </d:propstat>\n"...)
		buf = append(buf, "  </d:response>\n"...)
	}
	buf = append(buf, "</d:multistatus>\n"...)
	if _, err := w.Write(buf); err != nil { //nolint:gosec // G705: XML payload is built with escaped fields and write failure is logged
		e.logger.Warn("writing trash response", "error", err)
	}
}

func (e *Engine) serveDavTrashEmpty(w http.ResponseWriter, r *http.Request, p middleware.Principal) {
	user := core.UserID(p.UserID)
	for _, rt := range e.Core.Roots(user) {
		if len(p.Shares) > 0 && !slices.Contains(p.Shares, rt.Label) {
			continue
		}
		vp, verr := vfs.ParseVpath("/" + rt.Label)
		if verr != nil {
			continue
		}
		if resolved, err := e.Core.Resolve(user, vp, acl.Delete); err == nil {
			if perr := e.Core.TrashPurge(r.Context(), resolved, nil); perr != nil {
				e.logger.Warn("emptying trash failed", "share", rt.Share, "error", perr)
			}
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (e *Engine) serveDavTrashPurge(w http.ResponseWriter, r *http.Request, p middleware.Principal, rawID string) {
	user := core.UserID(p.UserID)
	res, id, err := e.resolveTrashID(user, rawID, acl.Delete, p.Shares...)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if perr := e.Core.TrashPurge(r.Context(), res, &id); perr != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (e *Engine) serveDavTrashRestore(w http.ResponseWriter, r *http.Request, p middleware.Principal, rawID string) {
	user := core.UserID(p.UserID)
	res, id, err := e.resolveTrashID(user, rawID, acl.Create, p.Shares...)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if _, rerr := e.Core.TrashRestore(r.Context(), res, id); rerr != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func leafOfTrash(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

func xmlEscapeString(s string) string {
	var buf strings.Builder
	if err := xml.EscapeText(&buf, []byte(s)); err != nil {
		return s
	}
	return buf.String()
}
