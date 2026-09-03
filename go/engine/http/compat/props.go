//go:build linux && compat_nc

package compat

import (
	"encoding/xml"
	"fmt"
	"strconv"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/dav"
)

// The vendor properties a sync client reads on every PROPFIND, and the two
// functions that render them.
//
// The vocabulary belongs to the client, not to this server: the namespaces,
// the property names and the value formats are reproduced exactly, because a
// client parses them. The permissions string in particular is the highest
// risk value in this whole surface, and its own comment says why.

// The namespaces this source claims.
const (
	NSOwnCloud   = "http://owncloud.org/ns"
	NSNextcloudX = "http://nextcloud.org/ns"
)

// DavID renders the identity a client keys its sync journal on: the file id,
// zero-padded to at least eight digits, concatenated with the instance id
// with no separator. The reference pads and never truncates, so a larger id
// simply gets longer.
func DavID(id uint64, instanceID string) string {
	return fmt.Sprintf("%08d%s", id, instanceID)
}

// DavPermissions renders the permission letters.
//
// This is the one value here whose failure is silent: a letter that is wrong
// in either direction makes a desktop client change its own behaviour, sync
// stops, and nothing anywhere logs a reason. The sequence is the reference's
// verbatim, order included, because clients compare the string rather than
// reading it as a set:
//
//	S  shared
//	R  may share
//	M  mounted, never emitted here: there is no external storage, and
//	   claiming a mount makes clients apply mount-specific move rules
//	G  may read
//	D  may delete
//	N  may rename
//	V  may move
//	then, exclusively:
//	  file:      W   if writable
//	  directory: CK  if creatable
//
// What each letter costs when it is wrong: missing W and the client treats
// the file as read-only and never uploads an edit; missing N or V and it
// implements rename as delete and re-upload, so renaming a large directory
// re-uploads all of it; missing C or K and nothing can be created inside; an
// empty string and the client ignores the entry entirely, which is why a
// read-only resource still carries G.
func DavPermissions(p PermBits, isDir, shared bool) string {
	out := make([]byte, 0, 9)
	if shared {
		out = append(out, 'S')
	}
	if p.Has(PermShare) {
		out = append(out, 'R')
	}
	if p.Has(PermRead) {
		out = append(out, 'G')
	}
	if p.Has(PermDelete) {
		out = append(out, 'D')
	}
	if p.Has(PermRename) {
		out = append(out, 'N')
	}
	if p.Has(PermMove) {
		out = append(out, 'V')
	}
	if isDir {
		if p.Has(PermCreate) {
			out = append(out, 'C', 'K')
		}
	} else if p.Has(PermWrite) {
		out = append(out, 'W')
	}
	return string(out)
}

// The permission bits the letters read.
const (
	PermShare PermBits = 1 << iota
	PermRead
	PermDelete
	PermRename
	PermMove
	PermCreate
	PermWrite
)

// PermBits is the bitset DavPermissions renders.
type PermBits uint8

// Has answers whether the letters this renderer emits all sit in the set.
func (p PermBits) Has(want PermBits) bool { return p&want == want }

// PropEntry is what one hit carries into the renderer: the fields the
// vocabulary touches and nothing else.
type PropEntry struct {
	IsDir      bool
	Size       uint64
	Perms      PermBits
	FileID     uint64
	HasPreview bool
	Favorite   bool
}

// PropSource renders the vendor properties for one entry.
//
// It renders; it does not decide. The file id and the shared flag arrive
// resolved, because both belong to the assembly that owns the storage, and a
// source that reached for them itself would need an import the layer gate
// forbids.
type PropSource struct {
	instanceID func() string
	shared     func(PropEntry) bool
	favorite   func(PropEntry) bool
	warn       func(msg string, args ...any)
}

// PropSourceDeps carries the source's reach, as functions, so the assembly
// decides where each answer comes from.
type PropSourceDeps struct {
	InstanceID func() string
	Shared     func(PropEntry) bool
	Favorite   func(PropEntry) bool
	Warn       func(msg string, args ...any)
}

// NewPropSource fills the defaults a caller should not have to think about:
// silence for warnings, and not-shared where nobody can answer.
func NewPropSource(d PropSourceDeps) *PropSource {
	if d.Warn == nil {
		d.Warn = func(string, ...any) {}
	}
	if d.Shared == nil {
		d.Shared = func(PropEntry) bool { return false }
	}
	return &PropSource{
		instanceID: d.InstanceID,
		shared:     d.Shared,
		favorite:   d.Favorite,
		warn:       d.Warn,
	}
}

// Namespaces claims the two vocabularies the properties are written in.
func (s *PropSource) Namespaces() []string {
	return []string{NSOwnCloud, NSNextcloudX}
}

// Props produces the vendor properties for one entry, under a named request.
//
// Only the names in want are computed. Several of the properties cost a
// lookup or a decision, so producing the whole set for a request that named
// one of them would be work nothing reads.
func (s *PropSource) Props(e PropEntry, want []xml.Name) []dav.Prop {
	asked := func(space, local string) bool {
		for _, n := range want {
			if n.Space == space && n.Local == local {
				return true
			}
		}
		return false
	}

	// An entry without its journal key vanishes from the client's view
	// without a word. So when the lookup could not answer, the zero id goes
	// out anyway: a wrong id is visible and debuggable, a missing one is
	// neither.
	id := e.FileID
	if id == 0 {
		s.warn("an entry reached property emission without a file id")
	}

	// The shared lookup feeds both the share types and the leading letter of
	// the permissions, so it runs once. On failure it falls back to not
	// shared: a missing permissions string is worse than a missing S.
	shared := false
	if asked(NSOwnCloud, "share-types") || asked(NSOwnCloud, "permissions") {
		shared = s.shared(e)
	}

	var out []dav.Prop
	add := func(space, local, value string) {
		out = append(out, dav.Prop{Name: xml.Name{Space: space, Local: local}, Value: value})
	}

	if asked(NSOwnCloud, "id") {
		add(NSOwnCloud, "id", DavID(id, s.instanceID()))
	}
	if asked(NSOwnCloud, "fileid") {
		add(NSOwnCloud, "fileid", strconv.FormatUint(id, 10))
	}
	if asked(NSOwnCloud, "permissions") {
		add(NSOwnCloud, "permissions", DavPermissions(e.Perms, e.IsDir, shared))
	}
	if asked(NSOwnCloud, "size") {
		// For a file this is the plain size. For a directory it is the
		// recursive rollup, which this server does not compute, so the
		// property is omitted rather than invented: falling back to the
		// directory's own stat size once announced a folder holding a
		// terabyte as four kilobytes, a number plausible enough that nobody
		// reads it as an error. An empty element would be worse still, since
		// a client casts the value unguarded and fails the whole listing.
		if !e.IsDir {
			add(NSOwnCloud, "size", strconv.FormatUint(e.Size, 10))
		}
	}
	if asked(NSOwnCloud, "share-types") {
		// Present and empty. The client's check is for the property itself,
		// not for anything inside it, and what it wants back is a list it
		// can walk: this server grants no share types, so the list is the
		// empty one rather than a missing property.
		out = append(out, dav.Prop{Name: xml.Name{Space: NSOwnCloud, Local: "share-types"}})
	}
	if asked(NSOwnCloud, "favorite") {
		fav := "0"
		if e.Favorite {
			fav = "1"
		}
		add(NSOwnCloud, "favorite", fav)
	}
	if asked(NSNextcloudX, "is-favorite") {
		fav := "0"
		if e.Favorite {
			fav = "1"
		}
		add(NSNextcloudX, "is-favorite", fav)
	}
	if asked(NSNextcloudX, "is-encrypted") {
		// This deployment holds no encrypted folders, and the client shows a
		// lock badge from exactly this field. A value that read as encrypted
		// would be one of the three answers that suppresses the tick without
		// anything being wrong the user could see.
		add(NSNextcloudX, "is-encrypted", "0")
	}
	if asked(NSNextcloudX, "has-preview") || asked(NSOwnCloud, "has-preview") {
		add(NSNextcloudX, "has-preview", strconv.FormatBool(e.HasPreview))
	}
	return out
}
