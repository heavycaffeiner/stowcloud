//go:build linux

package dav

import (
	"encoding/xml"
	"net/http"

	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
)

// PROPPATCH: writing the properties the server does not maintain itself.
//
// A live property is computed from the resource, so setting one would be a
// client believing it changed a file's length by writing the number. Those are
// refused, and RFC 4918 makes the refusal cost the whole request: one bad
// instruction means nothing is written.

// Proppatch answers PROPPATCH.
func (h *Handler) Proppatch(w http.ResponseWriter, r *http.Request, res core.Resolved) {
	if err := res.Require(acl.Write); err != nil {
		h.fail(w, r, err)
		return
	}

	patch, perr := ParsePropPatch(http.MaxBytesReader(w, r.Body, h.limits.Bytes), h.limits)
	if perr != nil {
		h.fail(w, r, perr)
		return
	}

	st, serr := res.Root().Stat(res.Path())
	if serr != nil {
		h.fail(w, r, core.ErrNotFound)
		return
	}
	if gerr := h.guard(r, res); gerr != nil {
		h.fail(w, r, gerr)
		return
	}

	// A name that cannot be written back as a tag is refused here rather than
	// stored. Stored, it would break every later PROPFIND on the resource, and
	// the client that caused it would never see the failure.
	for _, in := range patch.Instructions {
		if !ValidPropertyName(in.Name) {
			h.fail(w, r, ErrBadPropertyName)
			return
		}
	}

	plan := PlanPropPatch(patch, isLiveProperty)

	if plan.Commit {
		if h.store == nil || h.keyOf == nil {
			// Nowhere to put them. Reporting success would tell a client its
			// properties are stored when the next PROPFIND will not find them.
			h.fail(w, r, ErrNoPropertyStore)
			return
		}
		entry := h.core.EntryAt(res, st)
		if werr := h.store.SetProps(r.Context(), h.keyOf(entry), writesOf(patch)); werr != nil {
			h.fail(w, r, werr)
			return
		}
	}

	// The response href is the path the client addressed, so the result it
	// reads back is for the resource it can request.
	segs, serr := SplitPath(r.URL.EscapedPath())
	if serr != nil {
		h.fail(w, r, serr)
		return
	}
	h.writePlan(r, w, EncodeHref(segs, st.Kind.IsDir()), plan)
}

// writePlan writes the multistatus reporting what each instruction got.
//
// One group per distinct status rather than one per property, which is what
// RFC 4918 asks for and what keeps a hundred-property request from producing a
// hundred propstat elements.
func (h *Handler) writePlan(r *http.Request, w http.ResponseWriter, href string, plan Plan) {
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusMultiStatus)

	m := NewMultistatus(w, namespacesOf(plan))

	// Grouped in first-seen order, so two identical requests produce identical
	// documents rather than differing by map iteration.
	var order []int
	byStatus := map[int][]Prop{}
	for _, out := range plan.Outcomes {
		if _, seen := byStatus[out.Status]; !seen {
			order = append(order, out.Status)
		}
		byStatus[out.Status] = append(byStatus[out.Status], Prop{Name: out.Name, NamesOnly: true})
	}

	groups := make([]PropStat, 0, len(order))
	for _, status := range order {
		groups = append(groups, PropStat{Status: status, Props: byStatus[status]})
	}

	m.Response(href, groups)
	h.closeMultistatus(r, m)
}

// namespacesOf collects the namespaces a plan's names live in.
//
// The writer declares only what it was told about, and a property whose
// namespace was not declared is dropped: a request's own vocabulary has to be
// declared or its outcome would not be reported back.
func namespacesOf(plan Plan) []string {
	seen := map[string]bool{}
	var out []string
	for _, o := range plan.Outcomes {
		if o.Name.Space == "" || seen[o.Name.Space] {
			continue
		}
		seen[o.Name.Space] = true
		out = append(out, o.Name.Space)
	}
	return out
}

// writesOf turns the instructions into store operations.
func writesOf(patch PropPatch) []PropWrite {
	out := make([]PropWrite, 0, len(patch.Instructions))
	for _, in := range patch.Instructions {
		out = append(out, PropWrite{
			NS:     in.Name.Space,
			Name:   in.Name.Local,
			Value:  in.Value,
			Remove: in.Op == OpRemove,
		})
	}
	return out
}

// isLiveProperty reports whether the server maintains this itself.
//
// Everything in the DAV namespace this build produces is live. A DAV: name it
// does not produce is still refused: the namespace belongs to the protocol, so
// letting a client store into it invites a stored property to shadow one a
// later version computes.
func isLiveProperty(name xml.Name) bool { return name.Space == davNS }
