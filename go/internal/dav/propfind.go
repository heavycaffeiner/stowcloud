//go:build linux

package dav

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/heavycaffeiner/stowcloud/go/internal/acl"
	"github.com/heavycaffeiner/stowcloud/go/internal/core"
	"github.com/heavycaffeiner/stowcloud/go/internal/limits"
	"github.com/heavycaffeiner/stowcloud/go/internal/store/state"
	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
)

// PROPFIND, streamed.
//
// The listing is pulled a page at a time and each entry is written as it is
// produced, so a directory of a million entries is a bounded-memory response.
// Nothing accumulates: the document is not built and then sent.

// propfind answers a PROPFIND.
func (h *Handler) propfind(w http.ResponseWriter, r *http.Request, res core.Resolved) {
	body, err := readBody(r, limits.RequestBodyXML)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	req, err := ParsePropFind(body, h.limits)
	if err != nil {
		h.fail(w, r, err)
		return
	}

	depth, err := ParseDepth(r.Header.Get("Depth"), DepthInfinity)
	if err != nil {
		h.fail(w, r, err)
		return
	}

	st, err := res.Root().Stat(res.Path())
	if err != nil {
		h.fail(w, r, core.ErrNotFound)
		return
	}

	// Depth: infinity over a large collection is refused rather than attempted.
	// The honest failure beats the one that arrives after ten minutes.
	if depth == DepthInfinity && st.Kind.IsDir() {
		if err := h.refuseHugeInfinity(r.Context(), res); err != nil {
			h.fail(w, r, err)
			return
		}
	}

	m := NewMultistatus(w, h.namespaces())
	self := h.entryOf(res, st)
	base := hrefOf(r.URL.Path, st.Kind.IsDir())

	if werr := h.writeEntry(r.Context(), m, req, res, self, base); werr != nil {
		// The status is already 207 and some of the document is on the wire,
		// so there is no way to turn this into an error response. Closing the
		// document truncates it, which is what a client detects.
		h.logger(r).Warn("the multistatus could not be completed", "error", werr)
		_ = m.Close() //nolint:errcheck // the write error above is the answer.
		return
	}

	if depth != DepthZero && st.Kind.IsDir() {
		if err := h.walk(r.Context(), m, req, res, base, depth); err != nil {
			h.logger(r).Warn("the listing could not be completed", "error", err)
		}
	}

	if err := m.Close(); err != nil {
		h.logger(r).Warn("the multistatus could not be closed", "error", err)
	}
}

// walk streams one level, and recurses for depth infinity.
func (h *Handler) walk(
	ctx context.Context, m *Multistatus, req PropFind,
	res core.Resolved, base string, depth int,
) error {
	var cur core.Cursor
	for {
		// Cancellation is checked once per page rather than once per entry. A
		// client that hung up must stop the walk without the check costing a
		// syscall per file.
		if err := ctx.Err(); err != nil {
			return err
		}

		page, err := h.core.List(ctx, res, cur)
		if err != nil {
			return err
		}
		for _, e := range page.Entries {
			href := base + escapeSegment(e.Name)
			if e.IsDir {
				href += "/"
			}
			if werr := h.writeEntry(ctx, m, req, res, e, href); werr != nil {
				return werr
			}
			if depth == DepthInfinity && e.IsDir {
				child, rerr := h.resolveChild(res, e.Name)
				if rerr != nil {
					// A child that cannot be resolved is one the caller may
					// not see or one that vanished. Neither fails the listing.
					continue
				}
				if err := h.walk(ctx, m, req, child, href, depth); err != nil {
					return err
				}
			}
		}
		if page.Next == "" {
			return nil
		}
		cur = page.Next
	}
}

// writeEntry renders one entry into the document.
func (h *Handler) writeEntry(
	ctx context.Context, m *Multistatus, req PropFind,
	res core.Resolved, e core.Entry, href string,
) error {
	var dead []DeadProp
	// Dead properties are only read when they could be returned. allprop does
	// not dump them, so under allprop this is a query nobody would use.
	if req.Mode != PropFindAllProp && h.state != nil {
		stored, err := h.state.DavProps(ctx, identOf(e))
		if err != nil {
			return err
		}
		for _, s := range stored {
			dead = append(dead, DeadProp{Name: Name{Space: s.NS, Local: s.Name}, Value: s.Value})
		}
	}

	var locks []ActiveLock
	if h.locks != nil && wantsLockDiscovery(req) {
		got, err := h.locks.At(ctx, int64(res.Share()), string(e.Path.String()))
		if err != nil {
			return err
		}
		locks = got
	}

	found, notFound := buildProps(req, e, href, res.User(), dead, h.sources, locks, nil)
	return m.Write(Response{Href: href, Found: found, NotFound: notFound})
}

// wantsLockDiscovery reports whether this request could return the property,
// so the lock table is not read for a request that would discard it.
func wantsLockDiscovery(req PropFind) bool {
	if req.Mode != PropFindNamed {
		return true
	}
	for _, n := range req.Props {
		if n.IsDav("lockdiscovery") {
			return true
		}
	}
	return false
}

// refuseHugeInfinity turns a depth-infinity request over a big collection into
// a 507 before any of it is walked.
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

func (h *Handler) entryOf(res core.Resolved, st vfs.Stat) core.Entry {
	return h.core.EntryAt(res, st)
}

func (h *Handler) resolveChild(res core.Resolved, name string) (core.Resolved, error) {
	p, err := res.Path().JoinExisting(name)
	if err != nil {
		return core.Resolved{}, err
	}
	return h.core.ResolveUnder(res, p, acl.Read)
}

func identOf(e core.Entry) state.Ident {
	id := state.Ident{
		Share: int64(e.Ident.Share),
		Dev:   e.Ident.Dev,
		Ino:   e.Ident.Ino,
	}
	if e.Ident.Btime != nil {
		b := *e.Ident.Btime
		id.Btime = &b
	}
	return id
}

// hrefOf normalises the request path into the href a response carries. A
// collection always ends in a slash, which several clients depend on.
func hrefOf(p string, isDir bool) string {
	if p == "" {
		p = "/"
	}
	if isDir && !strings.HasSuffix(p, "/") {
		return p + "/"
	}
	if !isDir {
		return strings.TrimSuffix(p, "/")
	}
	return p
}

// escapeSegment is the raw name; the writer percent-encodes and escapes it.
func escapeSegment(name string) string { return name }

// readBody reads a request body under a cap.
//
// http.MaxBytesReader is what enforces the ceiling, so an oversized body is
// refused while it is being read rather than after it is in memory.
func readBody(r *http.Request, max int64) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	limited := http.MaxBytesReader(nil, r.Body, max)
	buf, err := io.ReadAll(limited)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return nil, limits.Exceed("dav request body", max, max+1)
		}
		return nil, err
	}
	return buf, nil
}
