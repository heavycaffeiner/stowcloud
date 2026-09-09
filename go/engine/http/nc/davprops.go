//go:build linux && compat_nc

package nc

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
)

// Rendering one files-tree entry into the property vocabulary.
//
// entryProps is the one place that decides, per property, whether this
// deployment has an answer. A property nobody stores a value for goes in the
// missing list rather than being guessed at: a fabricated share attribute or
// a made-up photo timestamp is worse than a client that quietly skips one
// property it asked for, which is what every client here already does.

// EntryProps is what one entry needs rendered.
type EntryProps struct {
	Query     PropQuery
	Entry     core.Entry
	Perms     acl.Perms
	ShareRoot bool
	Favorite  bool
	// Quota is non-nil on a collection that should carry the two quota
	// properties.
	Quota *QuotaProps
	// Trash is non-nil for a trash entry.
	Trash              *TrashProps
	OwnerID, OwnerName string
	ShareTypes         []int64
}

// QuotaProps is the pair of quota properties. Available is negative for
// unlimited, matching how the store already represents it.
type QuotaProps struct {
	Used      uint64
	Available int64
}

// TrashProps is the trio a trash entry carries.
type TrashProps struct {
	FileName         string
	OriginalLocation string
	DeletedAtNs      int64
}

// entryProps answers a query for one entry, in the order asked for, and
// names what could not be answered.
func (s *Server) entryProps(ctx context.Context, in EntryProps) ([]Prop, []PropName) {
	e := in.Entry
	shared := len(in.ShareTypes) > 0

	// The identity is minted at most once per call: PropID() and PropFileID()
	// both need it, and the store call it costs logs a warning on failure,
	// which a second call would repeat for the same entry.
	var (
		fileID     uint64
		fileIDRead bool
	)
	idOf := func() uint64 {
		if !fileIDRead {
			fileID = s.fileID(ctx, e)
			fileIDRead = true
		}
		return fileID
	}

	resolve := func(n PropName) (Prop, bool) {
		switch n {
		case PropETag():
			v := ETagValue(e.ETag)
			if v == "" {
				return Prop{}, false
			}
			return TextProp(n, v), true

		case PropID():
			id := idOf()
			if id == 0 {
				return Prop{}, false
			}
			return TextProp(n, DavID(id, s.instanceTag())), true

		case PropFileID():
			id := idOf()
			if id == 0 {
				return Prop{}, false
			}
			return TextProp(n, strconv.FormatUint(id, 10)), true

		case PropPermissions():
			return TextProp(n, PermString(in.Perms, e.IsDir, shared, in.ShareRoot)), true

		case PropResourceType():
			if e.IsDir {
				return Prop{Name: n, Children: []Node{{Name: dav("collection")}}}, true
			}
			// Present and empty: a file has a resource type, and it is
			// nothing.
			return Prop{Name: n}, true

		case PropLastModified():
			return TextProp(n, HTTPDate(e.MTimeNs)), true

		case PropContentType():
			if e.IsDir {
				return Prop{}, false
			}
			return TextProp(n, ContentTypeOf(false, e.Name)), true

		case PropContentLength():
			if e.IsDir {
				return Prop{}, false
			}
			return TextProp(n, strconv.FormatUint(e.Size, 10)), true

		case PropSize():
			return TextProp(n, strconv.FormatUint(s.entrySize(ctx, e), 10)), true

		case PropDisplayName():
			return TextProp(n, e.Name), true

		case PropFavorite():
			return BoolProp(n, in.Favorite), true

		case PropHasPreview():
			return BoolProp(n, PreviewableName(e.Name) && s.deps.Features().Thumbnails), true

		case PropIsEncrypted():
			return BoolProp(n, false), true

		case PropMountType():
			if in.ShareRoot {
				return TextProp(n, "external"), true
			}
			return TextProp(n, ""), true

		case PropOwnerID():
			return TextProp(n, in.OwnerID), true

		case PropOwnerName():
			return TextProp(n, in.OwnerName), true

		case PropCommentsRead():
			return IntProp(n, 0), true

		case PropHidden():
			return BoolProp(n, false), true

		case PropCreationTime(), PropUploadTime():
			// Approximated from the filesystem's birth time: this store
			// keeps no separate upload log, and a file's birth time is the
			// nearest honest answer to when it arrived. Zero when the
			// filesystem reports none, rather than a mtime standing in for
			// a question it was not asked.
			return IntProp(n, birthSeconds(e)), true

		case PropShareTypes():
			if len(in.ShareTypes) == 0 {
				return Prop{Name: n}, true
			}
			children := make([]Node, len(in.ShareTypes))
			for i, t := range in.ShareTypes {
				children[i] = Node{Name: oc("share-type"), Text: strconv.FormatInt(t, 10)}
			}
			return Prop{Name: n, Children: children}, true

		case PropSharePermsInt():
			return IntProp(n, SharePermissionMask(in.Perms)), true

		case PropSharePermsList():
			return TextProp(n, sharePermList(SharePermissionMask(in.Perms))), true

		case PropLock():
			return BoolProp(n, false), true

		case PropSystemTags():
			return Prop{Name: n}, true

		case PropCreationDate():
			if e.BTimeNs == nil {
				return Prop{}, false
			}
			return TextProp(n, ISODate(*e.BTimeNs)), true

		case PropQuotaUsed():
			if in.Quota == nil {
				return Prop{}, false
			}
			return TextProp(n, strconv.FormatUint(in.Quota.Used, 10)), true

		case PropQuotaAvailable():
			if in.Quota == nil {
				return Prop{}, false
			}
			avail := in.Quota.Available
			if avail < 0 {
				// -3 is the sentinel a client reads as unlimited; a raw
				// negative is not a value the wire format defines.
				avail = -3
			}
			return IntProp(n, avail), true

		case PropTrashFilename():
			if in.Trash == nil {
				return Prop{}, false
			}
			return TextProp(n, in.Trash.FileName), true

		case PropTrashOrigin():
			if in.Trash == nil {
				return Prop{}, false
			}
			return TextProp(n, in.Trash.OriginalLocation), true

		case PropTrashDeleted():
			if in.Trash == nil {
				return Prop{}, false
			}
			return IntProp(n, in.Trash.DeletedAtNs/int64(time.Second)), true

		default:
			// A system tag relation, a comments href, a rich workspace, a
			// live photo pairing, a share attribute, a download limit, or a
			// photo metadata field: this deployment stores none of them,
			// and the client already treats an absent property as one to
			// skip.
			return Prop{}, false
		}
	}

	switch {
	case in.Query.NamesOnly:
		var found []Prop
		for _, n := range entryPropNames(in) {
			if _, ok := resolve(n); ok {
				found = append(found, Prop{Name: n})
			}
		}
		return found, nil

	case in.Query.All:
		var found []Prop
		for _, n := range entryPropNames(in) {
			if p, ok := resolve(n); ok {
				found = append(found, p)
			}
		}
		return found, nil

	default:
		var found []Prop
		var missing []PropName
		for _, n := range in.Query.Names {
			if p, ok := resolve(n); ok {
				found = append(found, p)
			} else {
				missing = append(missing, n)
			}
		}
		return found, missing
	}
}

// entryPropNames is the canonical order an allprop or propname request
// answers in: the fixed set plus whichever conditional groups this entry
// actually carries.
func entryPropNames(in EntryProps) []PropName {
	names := append([]PropName(nil), AllProps()...)
	if in.Quota != nil {
		names = append(names, PropQuotaUsed(), PropQuotaAvailable())
	}
	if in.Trash != nil {
		names = append(names, PropTrashFilename(), PropTrashOrigin(), PropTrashDeleted())
	}
	return names
}

// entrySize is oc:size: a file's own byte count, and a directory's recursive
// total from the core's rollup. Unknown, on any failure, answers zero rather
// than a stale or guessed figure.
func (s *Server) entrySize(ctx context.Context, e core.Entry) uint64 {
	if !e.IsDir {
		return e.Size
	}
	safe, err := e.Path.Safe()
	if err != nil {
		return 0
	}
	agg, err := s.deps.Core.Aggregate(ctx, e.Ident.Share, safe)
	if err != nil {
		return 0
	}
	return agg.RSize
}

// birthSeconds is a filesystem's birth time in epoch seconds, zero when it
// reports none.
func birthSeconds(e core.Entry) int64 {
	if e.BTimeNs == nil {
		return 0
	}
	return *e.BTimeNs / int64(time.Second)
}

// sharePermList renders the share permission mask as the comma-separated
// word list the second foreign namespace carries.
func sharePermList(mask int64) string {
	var parts []string
	if mask&SharePermRead != 0 {
		parts = append(parts, "read")
	}
	if mask&SharePermUpdate != 0 {
		parts = append(parts, "update")
	}
	if mask&SharePermCreate != 0 {
		parts = append(parts, "create")
	}
	if mask&SharePermDelete != 0 {
		parts = append(parts, "delete")
	}
	if mask&SharePermShare != 0 {
		parts = append(parts, "share")
	}
	return strings.Join(parts, ",")
}
