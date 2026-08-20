//go:build compat_nc

package nc

import (
	"fmt"
	"strconv"

	"github.com/heavycaffeiner/stowcloud/go/internal/compat/ncport"
)

// The vendor property source.
//
// It registers with the WebDAV package through that package's source
// interface, so the emitter writes what this returns and knows nothing about
// the vocabulary. Everything vendor-specific about a property lives here.

// The namespaces, matching the prefixes the reference registers.
const (
	NSOwnCloud   = "http://owncloud.org/ns"
	NSNextcloudX = "http://nextcloud.org/ns"
)

// FileID is the stable identifier a client keys its entire sync journal on.
type FileID uint64

// DavID is the zero-padded file id concatenated with the instance id, with no
// separator between them.
//
// The reference pads to at least eight digits and does not truncate, so a
// larger id simply gets longer.
func DavID(id FileID, instanceID string) string {
	return fmt.Sprintf("%08d%s", uint64(id), instanceID)
}

// DavPermissions is the highest-risk function in this package.
//
// A wrong letter makes desktop clients refuse to sync without reporting an
// error, which is the hardest failure mode to debug in the whole surface.
//
// The order is not alphabetical and is not free to choose, because some
// clients string-compare the value. It is taken verbatim from the reference,
// which appends in this sequence:
//
//	S  shared
//	R  may share
//	M  mounted
//	G  may read
//	D  may delete
//	N  may rename
//	V  may move
//	then, exclusively:
//	  file:      W   if writable
//	  directory: CK  if creatable
//
// So the longest strings are SRMGDNVW for a file and SRMGDNVCK for a
// directory.
//
// Three deliberate differences from the reference, all of them recorded:
//
//   - M is never emitted. There is no external storage concept here, and
//     claiming a mount makes clients apply mount-specific move restrictions.
//   - The reference derives N from a composite rename check and V from its
//     update bit. There are distinct rename and move permissions here, so they
//     map directly. That is strictly more expressive, and the letter positions
//     are unchanged, which is what matters on the wire.
//   - The reference folds write into update, so a file with update rights gets
//     both V and W. They are separate here: move gives V, write gives W.
//
// What each letter costs when it is wrong:
//
//   - missing W on a file: the client treats it read-only and never uploads a
//     local edit.
//   - missing N or V: the client implements rename as delete and re-upload, so
//     renaming a large directory re-uploads all of it.
//   - missing C or K on a directory: nothing can be created inside it.
//   - an empty string: the client ignores the entry entirely, which is why a
//     read-only share root must still carry G.
func DavPermissions(p ncport.Perms, isDir, shared bool) string {
	// The longest output is nine bytes.
	out := make([]byte, 0, 9)
	if shared {
		out = append(out, 'S')
	}
	if p.Has(ncport.Share) {
		out = append(out, 'R')
	}
	// M is intentionally never emitted.
	if p.Has(ncport.Read) {
		out = append(out, 'G')
	}
	if p.Has(ncport.Delete) {
		out = append(out, 'D')
	}
	if p.Has(ncport.Rename) {
		out = append(out, 'N')
	}
	if p.Has(ncport.Move) {
		out = append(out, 'V')
	}
	if isDir {
		if p.Has(ncport.Create) {
			out = append(out, 'C', 'K')
		}
	} else if p.Has(ncport.Write) {
		out = append(out, 'W')
	}
	return string(out)
}

// The share permission bitmask the OCS surface uses.
const (
	SharePermRead   = 1
	SharePermUpdate = 2
	SharePermCreate = 4
	SharePermDelete = 8
	SharePermShare  = 16
	SharePermAll    = 31
)

// SharePermissions maps a permission set onto that bitmask.
func SharePermissions(p ncport.Perms) int64 {
	var bits int64
	if p.Has(ncport.Read) {
		bits |= SharePermRead
	}
	if p.Has(ncport.Write) || p.Has(ncport.Move) {
		bits |= SharePermUpdate
	}
	if p.Has(ncport.Create) {
		bits |= SharePermCreate
	}
	if p.Has(ncport.Delete) {
		bits |= SharePermDelete
	}
	if p.Has(ncport.Share) {
		bits |= SharePermShare
	}
	return bits
}

// PropSource emits the vendor properties for an entry.
//
// It is built here and handed to the WebDAV package, which knows only that
// something claims these namespaces.
type PropSource struct {
	instanceID string
	// aggregate reports a directory's recursive size, and reports false when
	// it does not have one.
	aggregate func(e ncport.Entry) (uint64, bool)
	// shared reports whether an entry is shared, falling back rather than
	// failing: see the note in Emit.
	shared func(e ncport.Entry) bool
	// fileID resolves an entry's stable id.
	fileID func(e ncport.Entry) (FileID, bool)
	// warn reports an entry that reached emission without an id.
	warn func(msg string, args ...any)
}

// PropSourceDeps is what the source needs.
type PropSourceDeps struct {
	InstanceID string
	Aggregate  func(e ncport.Entry) (uint64, bool)
	Shared     func(e ncport.Entry) bool
	FileID     func(e ncport.Entry) (FileID, bool)
	Warn       func(msg string, args ...any)
}

// NewPropSource builds the source.
func NewPropSource(d PropSourceDeps) *PropSource {
	if d.Warn == nil {
		d.Warn = func(string, ...any) {}
	}
	if d.Shared == nil {
		d.Shared = func(ncport.Entry) bool { return false }
	}
	return &PropSource{
		instanceID: d.InstanceID,
		aggregate:  d.Aggregate,
		shared:     d.Shared,
		fileID:     d.FileID,
		warn:       d.Warn,
	}
}

// Namespaces are the URIs this source answers for.
func (s *PropSource) Namespaces() []string {
	return []string{NSOwnCloud, NSNextcloudX}
}

// EmittedProp is one property, in the shape the WebDAV writer consumes.
type EmittedProp struct {
	Space string
	Local string
	Value string
	// Raw is pre-serialised child markup, for a property whose value is
	// structured rather than text.
	Raw string
}

// Emit produces the vendor properties for an entry.
//
// want is the set the client asked for; a property nobody asked about is not
// computed, because several of them cost a lookup.
func (s *PropSource) Emit(e ncport.Entry, want []PropName) []EmittedProp {
	asked := func(space, local string) bool {
		for _, n := range want {
			if n.Space == space && n.Local == local {
				return true
			}
		}
		return false
	}

	// The core allocates file ids lazily, so a plain listing never forces one
	// into existence. A client cannot cope with that: this id is the key of
	// its entire local sync journal and an entry without one is skipped
	// outright. So whoever assembled the response must have materialised an id
	// before this ran, and if it did not, the sentinel is emitted rather than
	// the whole property set dropped: a wrong id is visible and debuggable,
	// and a silently missing entry is not.
	id, ok := s.fileID(e)
	if !ok {
		s.warn("an entry reached property emission without a file id", "name", e.Name)
		id = 0
	}

	// The share lookup feeds both the share types and the leading letter of
	// the permissions, so it runs once. On a backend failure it falls back to
	// "not shared" rather than dropping the property set: a missing
	// permissions string is worse than a missing S.
	shared := false
	if asked(NSOwnCloud, "share-types") || asked(NSOwnCloud, "permissions") {
		shared = s.shared(e)
	}

	var out []EmittedProp
	add := func(space, local, value string) {
		out = append(out, EmittedProp{Space: space, Local: local, Value: value})
	}

	if asked(NSOwnCloud, "id") {
		add(NSOwnCloud, "id", DavID(id, s.instanceID))
	}
	if asked(NSOwnCloud, "fileid") {
		add(NSOwnCloud, "fileid", strconv.FormatUint(uint64(id), 10))
	}
	if asked(NSOwnCloud, "permissions") {
		add(NSOwnCloud, "permissions", DavPermissions(e.Perms, e.IsDir, shared))
	}
	if asked(NSOwnCloud, "size") {
		// For a directory this is the recursive rollup and for a file the
		// plain size.
		//
		// A directory whose rollup is unavailable gets no property at all. It
		// used to fall back to the inode's own size, which for a directory is
		// whatever stat reports, so a folder holding a terabyte announced four
		// kilobytes: a number plausible enough that nobody reads it as an
		// error. Omitting leaves the client at its initialised zero, and
		// emitting an empty element would be worse still, since its parser
		// casts the value unguarded and fails the whole folder listing.
		if e.IsDir {
			if s.aggregate != nil {
				if total, have := s.aggregate(e); have {
					add(NSOwnCloud, "size", strconv.FormatUint(total, 10))
				}
			}
		} else {
			add(NSOwnCloud, "size", strconv.FormatUint(e.Size, 10))
		}
	}
	if asked(NSOwnCloud, "share-types") {
		// An empty element rather than an omission: the client reads the
		// presence of the property, and a share type list it can iterate over
		// is what it expects even when the list is empty.
		out = append(out, EmittedProp{Space: NSOwnCloud, Local: "share-types"})
	}
	if asked(NSNextcloudX, "is-encrypted") {
		// Zero, which is what a reference server answers for an unencrypted
		// folder too. Answering anything else here is one of the three
		// server-side lies that would suppress a client-side tick, and all
		// three are lies about the data.
		add(NSNextcloudX, "is-encrypted", "0")
	}
	if asked(NSNextcloudX, "has-preview") {
		add(NSNextcloudX, "has-preview", boolStr(!e.IsDir))
	}
	return out
}

// PropName is a namespace-resolved property name.
//
// Declared here rather than taken from the WebDAV package, because the layer
// may not import it: the wiring package converts.
type PropName struct {
	Space string
	Local string
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
