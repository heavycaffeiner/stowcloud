//go:build linux

package dav

import (
	"context"
	"encoding/xml"
	"net/http"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/limits"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
)

// PROPFIND: the method a sync client spends most of its time in.
//
// The answer is a 207 document written as it is produced rather than assembled
// and then sent. A directory of any size would otherwise be held in memory
// before the first byte reaches the client.

// DefaultInfinityEntries bounds what Depth: infinity will attempt.
//
// A client asking for an unbounded walk of a large tree gets a refusal instead
// of a response that arrives minutes later, or never. The honest failure is
// worth more than the attempt.
const DefaultInfinityEntries = 10000

// Propfind answers PROPFIND.
func (h *Handler) Propfind(w http.ResponseWriter, r *http.Request, res core.Resolved) {
	req, err := ParsePropFind(http.MaxBytesReader(w, r.Body, h.limits.Bytes), h.limits)
	if err != nil {
		h.fail(w, r, err)
		return
	}

	// Infinity is the default because RFC 4918 says so. Clients that mean
	// depth one send it, and the bound below is what makes the default safe.
	//
	// All three are listed, because PROPFIND accepts all three. The allowed
	// set is not optional: an empty one permits nothing, so leaving it off
	// refuses every request including the ones carrying no header at all.
	depth, derr := ParseDepth(r.Header.Get("Depth"), DepthInfinity,
		DepthZero, DepthOne, DepthInfinity)
	if derr != nil {
		h.fail(w, r, derr)
		return
	}

	st, serr := res.Root().Stat(res.Path())
	if serr != nil {
		h.fail(w, r, core.ErrNotFound)
		return
	}

	if depth == DepthInfinity && st.Kind.IsDir() {
		if berr := h.refuseHugeInfinity(r.Context(), res); berr != nil {
			h.fail(w, r, berr)
			return
		}
	}

	// Everything above could still refuse. Past this line the status is 207
	// and the document is going out, so a later failure can only truncate it.
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusMultiStatus)

	// The namespaces a request named, so a property the client asked for can
	// be written back. The writer drops anything in a namespace it was not
	// told about, which for a vendor property means the answer silently omits
	// exactly what was requested.
	m := NewMultistatus(w, requestedNamespaces(req))
	self := h.core.EntryAt(res, st)
	base := EncodeHref(res.Path().Components(), st.Kind.IsDir())

	if werr := h.writeEntry(r.Context(), m, req, res, self, base); werr != nil {
		// No way to turn this into an error response: the status is sent and
		// part of the body is on the wire. Closing truncates the document,
		// which is what a client detects.
		h.log(r).Warn("the multistatus stopped early", "error", werr)
		h.closeMultistatus(r, m)
		return
	}

	if depth != DepthZero && st.Kind.IsDir() {
		if werr := h.walk(r.Context(), m, req, res, depth); werr != nil {
			h.log(r).Warn("the listing stopped early", "error", werr)
		}
	}

	h.closeMultistatus(r, m)
}

// walk writes the members of a collection, and their members under infinity.
func (h *Handler) walk(
	ctx context.Context, m *Multistatus, req PropFind, res core.Resolved, depth Depth,
) error {
	var cur core.Cursor
	for {
		// Once per page rather than once per entry: a client that hung up has
		// to stop the walk, and checking per file would cost a syscall each.
		if err := ctx.Err(); err != nil {
			return err
		}

		page, err := h.core.List(ctx, res, cur)
		if err != nil {
			return err
		}

		for _, e := range page.Entries {
			child, rerr := h.resolveChild(res, e)
			if rerr != nil {
				// A member the caller may not reach is left out rather than
				// reported: a 403 inside the listing would confirm it exists.
				continue
			}
			href := EncodeHref(child.Path().Components(), e.IsDir)
			if werr := h.writeEntry(ctx, m, req, child, e, href); werr != nil {
				return werr
			}
			if depth == DepthInfinity && e.IsDir {
				if werr := h.walk(ctx, m, req, child, depth); werr != nil {
					return werr
				}
			}
		}

		if page.Next == "" {
			return nil
		}
		cur = page.Next
	}
}

// writeEntry writes one resource's properties into the document.
func (h *Handler) writeEntry(
	ctx context.Context, m *Multistatus, req PropFind,
	res core.Resolved, e core.Entry, href string,
) error {
	resource := Resource{
		Name:     e.Name,
		IsDir:    e.IsDir,
		Size:     e.Size,
		MTimeNs:  e.MTimeNs,
		BTimeNs:  e.BTimeNs,
		ETag:     e.ETag,
		ETagWeak: e.ETagWeak,
	}

	// The lock table is read only when the answer could carry a lock. Under a
	// named request that did not ask for lockdiscovery it is a query whose
	// result nothing would use.
	if h.locksAt != nil && wantsLockDiscovery(req) {
		resource.Locks = h.locksAt(ctx, uint32(res.Share()), res.Path().String())
	}

	var dead []deadProp
	// Dead properties are read only when they could be returned. allprop does
	// not dump them, which RFC 4918 permits, so under allprop this would be a
	// query nobody reads.
	if req.Mode != ModeAllProp && h.store != nil && h.keyOf != nil {
		stored, err := h.store.Props(ctx, h.keyOf(e))
		if err != nil {
			return err
		}
		for _, s := range stored {
			dead = append(dead, deadProp{Name: xml.Name{Space: s.NS, Local: s.Name}, Value: s.Value})
		}
	}

	// Vendor properties are consulted for named requests only. The source
	// answers the names it owns and leaves the rest, which the assemble below
	// reports as missing: a vendor property that is asked for and absent is
	// the client's signal to skip the entry, and hiding that behind an empty
	// value would have it believe the property is blank.
	var vendor []Prop
	if h.vendorProps != nil && req.Mode == ModeNamed {
		vendor = h.vendorProps(ctx, res, e, req.Names)
	}

	found, missing := assemble(req, resource, dead, vendor)

	groups := []PropStat{{Status: http.StatusOK, Props: found}}
	if len(missing) > 0 {
		// A separate group, because the status applies to the properties in
		// it. Folding them together would report the found ones as missing.
		absent := make([]Prop, 0, len(missing))
		for _, n := range missing {
			absent = append(absent, Prop{Name: n, NamesOnly: true})
		}
		groups = append(groups, PropStat{Status: http.StatusNotFound, Props: absent})
	}

	m.Response(href, groups)
	return nil
}

// deadProp is one stored property, as this file passes it around.
type deadProp struct {
	Name  xml.Name
	Value string
}

// assemble produces the properties one response carries.
//
// vendor carries the properties a registered source contributed. They sit in
// the order between live and dead: a live property is this package's answer,
// a vendor property is a claimed namespace's, and a dead one is what was
// stored against the resource.
func assemble(req PropFind, r Resource, dead []deadProp, vendor []Prop) (found []Prop, missing []xml.Name) {
	switch req.Mode {
	case ModePropName:
		// Names with no values, live and dead alike.
		for _, n := range LiveNames(r) {
			found = append(found, Prop{Name: n, NamesOnly: true})
		}
		for _, d := range dead {
			found = append(found, Prop{Name: d.Name, NamesOnly: true})
		}

	case ModeAllProp:
		found = append(found, LiveProps(r)...)

	case ModeNamed:
		for _, n := range req.Names {
			if p, ok := LiveProp(n, r); ok {
				found = append(found, p)
				continue
			}
			if p, ok := findVendor(vendor, n); ok {
				found = append(found, p)
				continue
			}
			if d, ok := findDead(dead, n); ok {
				found = append(found, Prop{Name: d.Name, Value: d.Value})
				continue
			}
			// Named and unavailable, which is a 404 for that property rather
			// than an empty value: the two say different things to a client.
			missing = append(missing, n)
		}
	}
	return found, missing
}

// findVendor returns the contributed property a name asked for.
func findVendor(vendor []Prop, n xml.Name) (Prop, bool) {
	for _, p := range vendor {
		if p.Name == n {
			return p, true
		}
	}
	return Prop{}, false
}

func findDead(dead []deadProp, n xml.Name) (deadProp, bool) {
	for _, d := range dead {
		if d.Name == n {
			return d, true
		}
	}
	return deadProp{}, false
}

// requestedNamespaces are the vocabularies a request named.
//
// Only what the client asked for. An allprop response carries live properties
// alone, which are all in the DAV namespace the writer declares anyway, and a
// stored property reaches a response only by being named.
func requestedNamespaces(req PropFind) []string {
	seen := map[string]bool{}
	var out []string
	for _, n := range req.Names {
		if n.Space == "" || n.Space == davNS || seen[n.Space] {
			continue
		}
		seen[n.Space] = true
		out = append(out, n.Space)
	}
	return out
}

// wantsLockDiscovery reports whether the answer could carry a lock.
func wantsLockDiscovery(req PropFind) bool {
	switch req.Mode {
	case ModeAllProp, ModePropName:
		return true
	case ModeNamed:
		for _, n := range req.Names {
			if n.Space == davNS && n.Local == "lockdiscovery" {
				return true
			}
		}
		return false
	default:
		return false
	}
}

// refuseHugeInfinity refuses an unbounded walk of a large collection.
//
// The count is the immediate directory rather than the whole subtree, which is
// what makes the check cheap: a tree big enough to matter has a big directory
// somewhere in it, and reading the total costs one listing.
func (h *Handler) refuseHugeInfinity(ctx context.Context, res core.Resolved) error {
	page, err := h.core.List(ctx, res, "")
	if err != nil {
		return err
	}
	if page.Total > h.infinityEntries {
		return limits.Exceed("dav depth-infinity entries",
			int64(h.infinityEntries), int64(page.Total))
	}
	return nil
}

// resolveChild resolves one member of an already-resolved collection.
func (h *Handler) resolveChild(parent core.Resolved, e core.Entry) (core.Resolved, error) {
	p, err := parent.Path().JoinExisting(e.Name)
	if err != nil {
		return core.Resolved{}, err
	}
	return h.core.ResolveUnder(parent, p, 0)
}

// closeMultistatus ends the document, reporting a failure it cannot answer.
func (h *Handler) closeMultistatus(r *http.Request, m *Multistatus) {
	if err := m.Close(); err != nil {
		h.log(r).Warn("the multistatus did not close", "error", err)
	}
}
