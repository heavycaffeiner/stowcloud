//go:build linux

package dav

import (
	"net/http"

	"github.com/heavycaffeiner/stowcloud/go/internal/acl"
	"github.com/heavycaffeiner/stowcloud/go/internal/core"
	"github.com/heavycaffeiner/stowcloud/go/internal/limits"
	"github.com/heavycaffeiner/stowcloud/go/internal/store/state"
)

// PROPPATCH. Dead properties live in state.db, keyed by the identity tuple, so
// they follow the file rather than a cache-minted id.

func (h *Handler) proppatch(w http.ResponseWriter, r *http.Request, res core.Resolved) {
	if err := res.Require(acl.Write); err != nil {
		h.fail(w, r, err)
		return
	}

	body, err := readBody(r, limits.RequestBodyXML)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	ops, err := ParsePropPatch(body, h.limits)
	if err != nil {
		h.fail(w, r, err)
		return
	}

	st, serr := res.Root().Stat(res.Path())
	if serr != nil {
		h.fail(w, r, core.ErrNotFound)
		return
	}
	e := h.core.EntryAt(res, st)
	href := hrefOf(r.URL.Path, st.Kind.IsDir())

	if err := h.guardWrite(r, res, string(e.Path.String())); err != nil {
		h.fail(w, r, err)
		return
	}

	// A live property is not storage. Refusing to set one is what stops a
	// client believing it changed a file's length by writing the property.
	var refused []Name
	var writes []PatchOp
	for _, op := range ops {
		if isLiveProperty(op.Name) {
			refused = append(refused, op.Name)
			continue
		}
		writes = append(writes, op)
	}

	status := http.StatusOK
	if len(refused) > 0 {
		// Nothing is applied when any instruction fails: RFC 4918 requires a
		// PROPPATCH to be atomic across its whole instruction set.
		status = http.StatusForbidden
	} else if len(writes) > 0 && h.state != nil {
		if err := h.state.SetDavProps(r.Context(), identOf(e), toStoreOps(writes)); err != nil {
			h.fail(w, r, err)
			return
		}
	}

	m := NewMultistatus(w, h.namespaces())
	resp := Response{Href: href}
	for _, op := range ops {
		code := status
		if len(refused) > 0 && !containsName(refused, op.Name) {
			// The others failed only because this one did.
			code = http.StatusFailedDependency
		}
		if code == http.StatusOK {
			resp.Found = append(resp.Found, Prop{Name: op.Name})
			continue
		}
		resp.NotFound = append(resp.NotFound, op.Name)
	}
	// A propstat pair carrying the real status, rather than the 200/404 split
	// the property reader uses.
	if len(refused) > 0 {
		resp.Found = nil
		resp.NotFound = nil
		resp.Status = http.StatusForbidden
		resp.Desc = "a live property cannot be set through PROPPATCH"
	}
	if werr := m.Write(resp); werr != nil {
		h.logger(r).Warn("the multistatus could not be written", "error", werr)
	}
	if cerr := m.Close(); cerr != nil {
		h.logger(r).Warn("the multistatus could not be closed", "error", cerr)
	}
}

func toStoreOps(ops []PatchOp) []state.DavPropOp {
	out := make([]state.DavPropOp, 0, len(ops))
	for _, op := range ops {
		out = append(out, state.DavPropOp{
			NS:     op.Name.Space,
			Name:   op.Name.Local,
			Value:  op.Value,
			Remove: op.Remove,
		})
	}
	return out
}

// isLiveProperty reports whether a name is one the server computes.
//
// Storing a dead property under a live name would produce two answers for one
// question, and the reader would have to pick. Refusing at the door means it
// never has to.
func isLiveProperty(n Name) bool {
	if n.Space != NSDav {
		return false
	}
	switch n.Local {
	case "resourcetype", "getcontentlength", "getcontenttype", "getlastmodified",
		"getetag", "creationdate", "supportedlock", "lockdiscovery",
		"quota-used-bytes", "quota-available-bytes":
		return true
	}
	// displayname is deliberately absent: RFC 4918 makes it settable, and
	// clients do set it.
	return false
}
