//go:build linux

package dav

import (
	"strconv"
	"strings"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/internal/core"
)

// collectionContentType is what a directory reports, and it is the string
// every WebDAV client recognises for one.
const collectionContentType = "httpd/unix-directory"

// liveNames is what propname reports and what allprop produces, in a fixed
// order so two identical requests produce identical documents.
//
// The list is built per call rather than held in a package variable: a shared
// slice is mutable state every caller could append into.
func liveNames(e core.Entry) []Name {
	names := []Name{
		DavName("resourcetype"),
		DavName("displayname"),
		DavName("getlastmodified"),
		DavName("creationdate"),
		DavName("getetag"),
		DavName("supportedlock"),
		DavName("lockdiscovery"),
		DavName("getcontenttype"),
	}
	if !e.IsDir {
		names = append(names, DavName("getcontentlength"))
	}
	return names
}

// PropCtx is what a source may know about the request beyond the entry. It is
// owned rather than borrowed, so a source can keep it without a lifetime.
type PropCtx struct {
	// Href is the request path this entry is being rendered under.
	Href string
	// User is the caller the response is being built for.
	User core.UserID
	// NamesOnly is set under propname: a source returns names with empty
	// values and must not do work to compute one.
	NamesOnly bool
}

// PropSource contributes properties for an entry.
//
// The compat layer registers one. This package knows nothing about what it
// emits: a source claims namespaces, and anything in them is handed over
// without being interpreted here.
type PropSource interface {
	// Namespaces are the URIs this source answers for. Registering them is
	// also what puts SEARCH and REPORT into the Allow header.
	Namespaces() []string
	// Props returns the properties this source can produce from want. A name
	// it cannot answer is simply omitted and becomes a 404 in the response.
	Props(ctx PropCtx, e core.Entry, want []Name) []Prop
}

// DeadProp is one stored property.
type DeadProp struct {
	Name  Name
	Value string
}

// buildProps assembles one response's properties.
//
// The order is fixed: live properties, then dead ones, then whatever the
// sources contribute. Anything explicitly asked for that nobody produced comes
// back as a not-found name.
func buildProps(
	req PropFind, e core.Entry, href string, user core.UserID,
	dead []DeadProp, sources []PropSource, locks []ActiveLock, quota *Quota,
) (found []Prop, notFound []Name) {
	namesOnly := req.Mode == PropFindPropName

	switch req.Mode {
	case PropFindPropName:
		for _, n := range liveNames(e) {
			found = append(found, Prop{Name: n})
		}
		for _, d := range dead {
			found = append(found, Prop{Name: d.Name})
		}
	case PropFindAllProp:
		found = append(found, liveProps(e, locks, quota)...)
		// allprop deliberately does not dump every dead property. RFC 4918
		// permits this, and a client that wants them names them.
	case PropFindNamed:
		want := req.Props
		for _, n := range want {
			if p, ok := liveProp(n, e, locks, quota); ok {
				found = append(found, p)
				continue
			}
			if d, ok := findDead(dead, n); ok {
				found = append(found, Prop{Name: d.Name, Value: d.Value})
				continue
			}
			notFound = append(notFound, n)
		}
	}

	// The sources get a turn at whatever is still missing, which is how a
	// vendor property reaches a response without this package naming it.
	if len(sources) > 0 {
		ctx := PropCtx{Href: href, User: user, NamesOnly: namesOnly}
		ask := notFound
		if req.Mode != PropFindNamed {
			ask = nil
		}
		for _, s := range sources {
			var give []Prop
			switch req.Mode {
			case PropFindNamed:
				give = s.Props(ctx, e, claimed(s, ask))
			default:
				give = s.Props(ctx, e, nil)
			}
			for _, p := range give {
				found = append(found, p)
				ask = removeName(ask, p.Name)
			}
		}
		if req.Mode == PropFindNamed {
			notFound = ask
		}
	}
	return found, notFound
}

// claimed narrows a want list to the namespaces a source answers for, so a
// source is never asked about a vocabulary it does not own.
func claimed(s PropSource, want []Name) []Name {
	spaces := s.Namespaces()
	out := make([]Name, 0, len(want))
	for _, n := range want {
		for _, ns := range spaces {
			if n.Space == ns {
				out = append(out, n)
				break
			}
		}
	}
	return out
}

func removeName(set []Name, n Name) []Name {
	for i, have := range set {
		if have == n {
			return append(set[:i:i], set[i+1:]...)
		}
	}
	return set
}

func findDead(dead []DeadProp, n Name) (DeadProp, bool) {
	for _, d := range dead {
		if d.Name == n {
			return d, true
		}
	}
	return DeadProp{}, false
}

// liveProps is the whole live set for allprop.
func liveProps(e core.Entry, locks []ActiveLock, quota *Quota) []Prop {
	out := make([]Prop, 0, 9)
	for _, n := range liveNames(e) {
		if p, ok := liveProp(n, e, locks, quota); ok {
			out = append(out, p)
		}
	}
	return out
}

// liveProp produces one live property, or reports that this is not one.
func liveProp(n Name, e core.Entry, locks []ActiveLock, quota *Quota) (Prop, bool) {
	if n.Space != NSDav {
		return Prop{}, false
	}
	switch n.Local {
	case "resourcetype":
		if e.IsDir {
			return Prop{Name: n, Raw: "<" + davPrefix + ":collection/>"}, true
		}
		return Prop{Name: n}, true

	case "getcontentlength":
		// A collection has no length. Emitting zero would be a lie a sync
		// client acts on.
		if e.IsDir {
			return Prop{}, false
		}
		return Prop{Name: n, Value: strconv.FormatUint(e.Size, 10)}, true

	case "getcontenttype":
		if e.IsDir {
			return Prop{Name: n, Value: collectionContentType}, true
		}
		return Prop{Name: n, Value: contentTypeOf(e.Name)}, true

	case "getlastmodified":
		return Prop{Name: n, Value: httpDate(e.MTimeNs)}, true

	case "creationdate":
		if e.BTimeNs == nil {
			return Prop{}, false
		}
		return Prop{Name: n, Value: iso8601(*e.BTimeNs)}, true

	case "getetag":
		return Prop{Name: n, Value: etagHeader(e)}, true

	case "displayname":
		return Prop{Name: n, Value: e.Name}, true

	case "supportedlock":
		// Exclusive write locks only, which is what this server offers.
		return Prop{Name: n, Raw: "<" + davPrefix + ":lockentry>" +
			"<" + davPrefix + ":lockscope><" + davPrefix + ":exclusive/></" + davPrefix + ":lockscope>" +
			"<" + davPrefix + ":locktype><" + davPrefix + ":write/></" + davPrefix + ":locktype>" +
			"</" + davPrefix + ":lockentry>"}, true

	case "lockdiscovery":
		return Prop{Name: n, Raw: lockDiscovery(locks)}, true

	case "quota-used-bytes":
		if quota == nil {
			return Prop{}, false
		}
		return Prop{Name: n, Value: strconv.FormatUint(quota.Used, 10)}, true

	case "quota-available-bytes":
		if quota == nil || quota.Available == nil {
			return Prop{}, false
		}
		return Prop{Name: n, Value: strconv.FormatUint(*quota.Available, 10)}, true

	case "getcontentlanguage":
		// Never guessed. A client asking gets a 404 rather than an invention.
		return Prop{}, false
	}
	return Prop{}, false
}

// Quota is the pair of quota properties, when the caller has them to give.
type Quota struct {
	Used      uint64
	Available *uint64
}

// etagHeader renders an entry's validator the way a header carries it, weak
// marker included. A client compares this against what GET returned, so the
// two must agree exactly.
func etagHeader(e core.Entry) string {
	if e.ETagWeak {
		return `W/"` + e.ETag + `"`
	}
	return `"` + e.ETag + `"`
}

func httpDate(ns int64) string {
	return time.Unix(0, ns).UTC().Format(http1123)
}

// http1123 is RFC 1123 with a hard GMT, which is what RFC 9110 requires. Go's
// time.RFC1123 prints the zone name and would emit "UTC".
const http1123 = "Mon, 02 Jan 2006 15:04:05 GMT"

func iso8601(ns int64) string {
	return time.Unix(0, ns).UTC().Format("2006-01-02T15:04:05Z")
}

// contentTypeOf is a small extension table.
//
// It is deliberately not net/http's DetectContentType: that reads the file's
// first bytes, and a PROPFIND over a large collection must never open the
// files it lists. A name-based guess costs nothing and is what every other
// WebDAV server does here.
func contentTypeOf(name string) string {
	dot := strings.LastIndexByte(name, '.')
	if dot < 0 || dot == len(name)-1 {
		return "application/octet-stream"
	}
	switch strings.ToLower(name[dot+1:]) {
	case "txt", "text", "log", "md":
		return "text/plain"
	case "html", "htm":
		return "text/html"
	case "css":
		return "text/css"
	case "js", "mjs":
		return "text/javascript"
	case "json":
		return "application/json"
	case "xml":
		return "application/xml"
	case "pdf":
		return "application/pdf"
	case "png":
		return "image/png"
	case "jpg", "jpeg":
		return "image/jpeg"
	case "gif":
		return "image/gif"
	case "webp":
		return "image/webp"
	case "svg":
		return "image/svg+xml"
	case "mp4":
		return "video/mp4"
	case "webm":
		return "video/webm"
	case "mp3":
		return "audio/mpeg"
	case "zip":
		return "application/zip"
	}
	return "application/octet-stream"
}
