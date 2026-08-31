//go:build linux

package dav

import (
	"encoding/xml"
	"strconv"
	"strings"
	"time"
)

// The live properties, built from an entry.
//
// Live means the server computes it: a client cannot store one, and PROPPATCH
// on any of these is refused. Anything else a client stores is a dead property,
// which this file takes as given and never interprets.

// CollectionContentType is what a directory reports. Every WebDAV client
// recognises this exact string for one.
const CollectionContentType = "httpd/unix-directory"

// httpDate is RFC 1123 with a hard GMT, which is what RFC 9110 requires.
// Go's time.RFC1123 prints the zone name and would emit "UTC" here.
const httpDate = "Mon, 02 Jan 2006 15:04:05 GMT"

// Resource is what the live properties are computed from.
//
// A struct of plain values rather than the domain's entry type, so this package
// stays a vocabulary: it describes what a resource looks like on the wire and
// borrows nothing from the tier that produced it.
type Resource struct {
	// Name is the last path segment, which displayname reports.
	Name string
	// IsDir decides resourcetype and suppresses getcontentlength.
	IsDir bool
	Size  uint64
	// MTimeNs is what getlastmodified reports.
	MTimeNs int64
	// BTimeNs is nil on a filesystem with no birth time. Zero is a real
	// timestamp, so it cannot stand for absent: creationdate is then omitted
	// rather than reported as 1970.
	BTimeNs *int64
	// ETag is the validator without its quotes or weak marker; getetag adds
	// them so the property matches what GET returns byte for byte.
	ETag     string
	ETagWeak bool
	// Locks are the live locks covering this resource, which lockdiscovery
	// renders.
	Locks []Lock
	// Quota is the pair of quota properties, when the caller has them. Nil
	// omits both rather than reporting zero, which a client would act on.
	Quota *Quota
}

// Lock is one live lock, as lockdiscovery renders it.
type Lock struct {
	Token     string
	Path      string
	Owner     string
	TimeoutS  int64
	Infinite  bool
	Exclusive bool
}

// Quota is the two quota properties.
type Quota struct {
	Used uint64
	// Available is nil where no limit applies, which omits the property. A
	// client reads zero as "full" and stops uploading.
	Available *uint64
}

// LiveNames is what propname reports, in a fixed order so two identical
// requests produce identical documents.
//
// Built per call rather than held in a package variable: a shared slice is
// mutable state every caller could append into.
// The two conditional names are conditional here as well. propname describes
// what this resource has, so a name it offers has to be one allprop produces:
// otherwise a client asks for exactly what it was just advertised and gets a
// 404 from the same server.
func LiveNames(r Resource) []xml.Name {
	names := []xml.Name{
		davName("resourcetype"),
		davName("displayname"),
		davName("getlastmodified"),
		davName("getetag"),
		davName("supportedlock"),
		davName("lockdiscovery"),
		davName("getcontenttype"),
	}
	if r.BTimeNs != nil {
		names = append(names, davName("creationdate"))
	}
	if !r.IsDir {
		names = append(names, davName("getcontentlength"))
	}
	return names
}

// LiveProps is the whole live set, for allprop.
func LiveProps(r Resource) []Prop {
	out := make([]Prop, 0, len(LiveNames(r)))
	for _, n := range LiveNames(r) {
		if p, ok := LiveProp(n, r); ok {
			out = append(out, p)
		}
	}
	return out
}

// LiveProp produces one live property, or reports that the name is not one.
//
// A name this server cannot answer is not invented. It comes back false and
// the caller reports it as a 404 inside the multistatus, which is what tells a
// client the difference between a property that is empty and one that is not
// there.
func LiveProp(n xml.Name, r Resource) (Prop, bool) {
	if n.Space != davNS {
		return Prop{}, false
	}

	switch n.Local {
	case "resourcetype":
		if r.IsDir {
			return Prop{Name: n, Children: []Element{{Name: davName("collection")}}}, true
		}
		// Present and empty: a file has a resourcetype, and it is nothing.
		return Prop{Name: n}, true

	case "getcontentlength":
		// A collection has no length. Reporting zero is a lie a sync client
		// acts on, so the property is absent instead.
		if r.IsDir {
			return Prop{}, false
		}
		return Prop{Name: n, Value: strconv.FormatUint(r.Size, 10)}, true

	case "getcontenttype":
		if r.IsDir {
			return Prop{Name: n, Value: CollectionContentType}, true
		}
		return Prop{Name: n, Value: ContentTypeOf(r.Name)}, true

	case "getlastmodified":
		return Prop{Name: n, Value: time.Unix(0, r.MTimeNs).UTC().Format(httpDate)}, true

	case "creationdate":
		if r.BTimeNs == nil {
			return Prop{}, false
		}
		return Prop{Name: n, Value: time.Unix(0, *r.BTimeNs).UTC().Format("2006-01-02T15:04:05Z")}, true

	case "getetag":
		return Prop{Name: n, Value: ETagHeader(r.ETag, r.ETagWeak)}, true

	case "displayname":
		return Prop{Name: n, Value: r.Name}, true

	case "supportedlock":
		return Prop{Name: n, Children: supportedLock()}, true

	case "lockdiscovery":
		return Prop{Name: n, Children: lockDiscovery(r.Locks)}, true

	case "quota-used-bytes":
		if r.Quota == nil {
			return Prop{}, false
		}
		return Prop{Name: n, Value: strconv.FormatUint(r.Quota.Used, 10)}, true

	case "quota-available-bytes":
		if r.Quota == nil || r.Quota.Available == nil {
			return Prop{}, false
		}
		return Prop{Name: n, Value: strconv.FormatUint(*r.Quota.Available, 10)}, true

	default:
		// getcontentlanguage lands here deliberately. Nothing in this server
		// knows a file's language, and a guess is worse than the 404.
		return Prop{}, false
	}
}

// supportedLock advertises both scopes, which is what this server grants.
func supportedLock() []Element {
	entry := func(scope string) Element {
		return Element{
			Name: davName("lockentry"),
			Children: []Element{
				{Name: davName("lockscope"), Children: []Element{{Name: davName(scope)}}},
				{Name: davName("locktype"), Children: []Element{{Name: davName("write")}}},
			},
		}
	}
	return []Element{entry("exclusive"), entry("shared")}
}

// lockDiscovery renders the locks covering a resource.
func lockDiscovery(locks []Lock) []Element {
	if len(locks) == 0 {
		return nil
	}
	out := make([]Element, 0, len(locks))
	for _, l := range locks {
		depth := "0"
		if l.Infinite {
			depth = "infinity"
		}
		// Report the scope that was actually granted rather than a constant.
		// Answering "exclusive" to a client that requested a shared lock leaves
		// it convinced it has sole possession.
		scope := "shared"
		if l.Exclusive {
			scope = "exclusive"
		}

		children := []Element{
			{Name: davName("locktype"), Children: []Element{{Name: davName("write")}}},
			{Name: davName("lockscope"), Children: []Element{{Name: davName(scope)}}},
			{Name: davName("depth"), Text: depth},
		}
		if l.Owner != "" {
			// Text, not markup. This is what the client sent when it took the
			// lock, and the writer escapes it on the way out.
			children = append(children, Element{Name: davName("owner"), Text: l.Owner})
		}
		children = append(children,
			Element{Name: davName("timeout"), Text: "Second-" + strconv.FormatInt(l.TimeoutS, 10)},
			Element{
				Name:     davName("locktoken"),
				Children: []Element{{Name: davName("href"), Text: l.Token}},
			},
			Element{
				// The lockroot carries the share-relative path the lock was
				// taken under, which is the form the lock table keys on and
				// the only one this rendering can name: the mount prefix
				// lives with the request this document is answering, and
				// threading it here would mean handing the resource's own
				// URL through every lock lookup. Clients read the token from
				// locktoken and match the lock on that; lockroot is the
				// display value.
				Name:     davName("lockroot"),
				Children: []Element{{Name: davName("href"), Text: l.Path}},
			},
		)
		out = append(out, Element{Name: davName("activelock"), Children: children})
	}
	return out
}

// ETagHeader renders a validator the way a header carries it, weak marker
// included. A client compares this against what GET returned, so the two have
// to agree exactly.
func ETagHeader(etag string, weak bool) string {
	if weak {
		return `W/"` + etag + `"`
	}
	return `"` + etag + `"`
}

// ContentTypeOf guesses a media type from a name.
//
// Deliberately not net/http's DetectContentType, which reads the first bytes
// of the file. A PROPFIND over a large collection must never open the files it
// lists, and a name-based guess is what every other WebDAV server does here.
func ContentTypeOf(name string) string {
	const fallback = "application/octet-stream"

	dot := strings.LastIndexByte(name, '.')
	if dot < 0 || dot == len(name)-1 {
		return fallback
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
	default:
		return fallback
	}
}

// davName builds a name in the DAV namespace.
func davName(local string) xml.Name { return xml.Name{Space: davNS, Local: local} }
