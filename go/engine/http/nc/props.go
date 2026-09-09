//go:build linux && compat_nc

package nc

import (
	"strconv"
	"strings"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
)

// The property vocabulary, and the small conversions every surface here
// shares: the permission string, the identity string, the date format and the
// two timestamp headers a client sets on an upload.

// The properties this surface answers. A name absent from this list is
// reported as missing rather than guessed at, which is what a client already
// handles: it skips the property and keeps the entry.
//
// One table of immutable names, written once and read everywhere. Nothing
// assigns to any of them; they are variables only because a struct value
// cannot be a constant in this language.
func PropDisplayName() PropName    { return dav("displayname") }
func PropLastModified() PropName   { return dav("getlastmodified") }
func PropETag() PropName           { return dav("getetag") }
func PropContentType() PropName    { return dav("getcontenttype") }
func PropContentLength() PropName  { return dav("getcontentlength") }
func PropResourceType() PropName   { return dav("resourcetype") }
func PropCreationDate() PropName   { return dav("creationdate") }
func PropQuotaAvailable() PropName { return dav("quota-available-bytes") }
func PropQuotaUsed() PropName      { return dav("quota-used-bytes") }

func PropID() PropName           { return oc("id") }
func PropFileID() PropName       { return oc("fileid") }
func PropPermissions() PropName  { return oc("permissions") }
func PropSize() PropName         { return oc("size") }
func PropFavorite() PropName     { return oc("favorite") }
func PropShareTypes() PropName   { return oc("share-types") }
func PropOwnerID() PropName      { return oc("owner-id") }
func PropOwnerName() PropName    { return oc("owner-display-name") }
func PropCommentsHref() PropName { return oc("comments-href") }
func PropCommentsCnt() PropName  { return oc("comments-count") }
func PropCommentsRead() PropName { return oc("comments-unread") }
func PropChecksums() PropName    { return oc("checksums") }
func PropDownloadURL() PropName  { return oc("downloadURL") }
func PropFingerprint() PropName  { return oc("data-fingerprint") }
func PropTags() PropName         { return oc("tags") }

func PropHasPreview() PropName      { return ncp("has-preview") }
func PropIsEncrypted() PropName     { return ncp("is-encrypted") }
func PropMountType() PropName       { return ncp("mount-type") }
func PropIsMountRoot() PropName     { return ncp("is-mount-root") }
func PropNote() PropName            { return ncp("note") }
func PropSharees() PropName         { return ncp("sharees") }
func PropRichWorkspace() PropName   { return ncp("rich-workspace") }
func PropCreationTime() PropName    { return ncp("creation_time") }
func PropUploadTime() PropName      { return ncp("upload_time") }
func PropLock() PropName            { return ncp("lock") }
func PropLockOwner() PropName       { return ncp("lock-owner") }
func PropLockOwnerName() PropName   { return ncp("lock-owner-displayname") }
func PropLockOwnerType() PropName   { return ncp("lock-owner-type") }
func PropLockOwnerEditor() PropName { return ncp("lock-owner-editor") }
func PropLockTime() PropName        { return ncp("lock-time") }
func PropLockTimeout() PropName     { return ncp("lock-timeout") }
func PropLockToken() PropName       { return ncp("lock-token") }
func PropSystemTags() PropName      { return ncp("system-tags") }
func PropHidden() PropName          { return ncp("hidden") }
func PropShareAttrs() PropName      { return ncp("share-attributes") }
func PropLivePhoto() PropName       { return ncp("metadata-files-live-photo") }

func PropTrashFilename() PropName { return ncp("trashbin-filename") }
func PropTrashOrigin() PropName   { return ncp("trashbin-original-location") }
func PropTrashDeleted() PropName  { return ncp("trashbin-deletion-time") }

// PropSharePermissions is the same question in two foreign namespaces.
// One client reads the integer form and another the comma-separated one,
// and each looks for its own prefix.
func PropSharePermsInt() PropName  { return PropName{NS: nsOCS, Local: "share-permissions"} }
func PropSharePermsList() PropName { return PropName{NS: nsOCM, Local: "share-permissions"} }

// PropQuery is what a PROPFIND asked for.
type PropQuery struct {
	// All is a request for every property this surface has, which is what
	// allprop and an absent body both mean.
	All bool
	// NamesOnly is a request for the names without the values.
	NamesOnly bool
	// Names are the properties an explicit request listed, in order.
	Names []PropName
}

// Asked reports whether a query wants one property.
func (q PropQuery) Asked(n PropName) bool {
	if q.All {
		return true
	}
	for _, name := range q.Names {
		if name.Equal(n) {
			return true
		}
	}
	return false
}

// AllProps is the set an allprop request answers, in the order a response
// writes them. Fixed order so a golden response is a byte comparison.
func AllProps() []PropName {
	return []PropName{
		PropDisplayName(), PropLastModified(), PropETag(), PropContentType(),
		PropContentLength(), PropResourceType(), PropCreationDate(),
		PropID(), PropFileID(), PropPermissions(), PropSize(), PropFavorite(),
		PropShareTypes(), PropOwnerID(), PropOwnerName(), PropCommentsRead(),
		PropHasPreview(), PropIsEncrypted(), PropMountType(), PropNote(),
		PropRichWorkspace(), PropCreationTime(), PropUploadTime(), PropLock(),
		PropSystemTags(), PropHidden(),
		PropSharePermsInt(), PropSharePermsList(),
	}
}

// The letters a permission string is spelled with, and what each one grants.
//
// The order matters to nothing on the wire, but a client asserts that the read
// letter is present on every entry it accepts, and refuses to sync an entry
// whose permission string is empty. So a listable entry always carries at
// least the read letter.
const (
	letterRead      = "G"
	letterWrite     = "W"
	letterDelete    = "D"
	letterRename    = "N"
	letterMove      = "V"
	letterAddFile   = "C"
	letterAddDir    = "K"
	letterReshare   = "R"
	letterShared    = "S"
	letterMountRoot = "M"
)

// PermString renders the permission letters for one entry.
//
// The create bit maps onto two letters, one for files and one for
// directories, because the vocabulary separates them and a client checks the
// matching one before it uploads. Both are answered from the same bit: this
// engine does not distinguish which kind of name a grant may mint.
//
// A file carries neither, since the letters describe what may be created
// inside a collection and a client reads them on a file as noise.
func PermString(p acl.Perms, isDir, shared, mountRoot bool) string {
	out := ""
	if shared {
		out += letterShared
	}
	if mountRoot {
		out += letterMountRoot
	}
	if p.Intersects(acl.Read | acl.Download) {
		out += letterRead
	}
	if p.Has(acl.Write) {
		out += letterWrite
	}
	if p.Has(acl.Delete) {
		out += letterDelete
	}
	if p.Has(acl.Rename) {
		out += letterRename
	}
	if p.Has(acl.Move) {
		out += letterMove
	}
	if p.Has(acl.Share) {
		out += letterReshare
	}
	if isDir && p.Has(acl.Create) {
		out += letterAddFile + letterAddDir
	}
	return out
}

// SharePermissionMask renders the same permissions as the integer bitmask the
// share API speaks.
func SharePermissionMask(p acl.Perms) int64 {
	var mask int64
	if p.Intersects(acl.Read | acl.Download) {
		mask |= SharePermRead
	}
	if p.Has(acl.Write) {
		mask |= SharePermUpdate
	}
	if p.Has(acl.Create) {
		mask |= SharePermCreate
	}
	if p.Has(acl.Delete) {
		mask |= SharePermDelete
	}
	if p.Has(acl.Share) {
		mask |= SharePermShare
	}
	return mask
}

// The share permission bits, as the wire numbers them.
const (
	SharePermRead   = 1
	SharePermUpdate = 2
	SharePermCreate = 4
	SharePermDelete = 8
	SharePermShare  = 16
	SharePermAll    = 31
)

// The share types, as the wire numbers them.
const (
	ShareTypeUser       = 0
	ShareTypeGroup      = 1
	ShareTypePublicLink = 3
	ShareTypeEmail      = 4
)

// DavID renders the identity a client keys its sync journal on.
//
// The vocabulary's own form is a numeric id followed by a fixed-width instance
// tag, and one client compares the whole string for equality across restarts
// while another parses the leading digits. Both are satisfied by rendering the
// id zero-padded and appending the instance, and neither tolerates the value
// changing for a file that did not move.
func DavID(fileID uint64, instance string) string {
	return zeroPad(strconv.FormatUint(fileID, 10), 8) + instance
}

// zeroPad left-pads to a width, leaving anything already wider alone.
func zeroPad(s string, width int) string {
	if len(s) >= width {
		return s
	}
	return strings.Repeat("0", width-len(s)) + s
}

// ETagValue renders an entry's validator as a client reads it.
//
// Quoted and unprefixed. Every validator this engine mints is derived from
// metadata rather than from a change counter, which is what the standard
// calls weak, but a weak marker on the wire is not what these clients parse:
// one of them compares the quoted string against what it stored and treats
// anything with a prefix as a different file, so the whole tree would
// re-download on every sync.
func ETagValue(token string) string {
	if token == "" {
		return ""
	}
	return `"` + token + `"`
}

// ParseETag reads a validator a client sent back, tolerating the shapes
// proxies add: a weak marker, the compression suffix a gateway appends, and
// surrounding quotes.
func ParseETag(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "W/")
	s = strings.Trim(s, `"`)
	s = strings.TrimSuffix(s, "-gzip")
	return s
}

// httpDateFormat is the one format every client here parses for a modification
// time. Two of them try several and one accepts only this.
const httpDateFormat = "Mon, 02 Jan 2006 15:04:05 GMT"

// HTTPDate renders a nanosecond timestamp as the date a listing carries.
func HTTPDate(ns int64) string {
	return time.Unix(0, ns).UTC().Format(httpDateFormat)
}

// ISODate renders a timestamp as the creation date format the vocabulary uses
// for the one property that is not an HTTP date.
func ISODate(ns int64) string {
	return time.Unix(0, ns).UTC().Format("2006-01-02T15:04:05Z")
}

// UnixSeconds renders a nanosecond timestamp as the epoch seconds a property
// carries.
func UnixSeconds(ns int64) string { return strconv.FormatInt(ns/int64(time.Second), 10) }

// ParseUnixHeader reads one of the timestamp headers a client sets on an
// upload.
//
// Seconds since the epoch, and a client only ever sends a positive value: it
// refuses to upload a file whose modification time is not positive. So a value
// at or below zero is treated as absent rather than applied, and a malformed
// one likewise: the alternative is stamping a file with a time no listing can
// render.
func ParseUnixHeader(v string) (int64, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	// A fractional value is truncated rather than refused. One client sends
	// the value as a floating-point number when its source is a filesystem
	// with sub-second stamps.
	if dot := strings.IndexByte(v, '.'); dot >= 0 {
		v = v[:dot]
	}
	secs, err := strconv.ParseInt(v, 10, 64)
	if err != nil || secs <= 0 {
		return 0, false
	}
	return secs * int64(time.Second), true
}

// ContentTypeOf is the type a listing reports for an entry.
//
// A collection carries the vocabulary's own directory type, which one client
// keys its folder detection on in addition to the resource type element.
func ContentTypeOf(isDir bool, name string) string {
	if isDir {
		return "httpd/unix-directory"
	}
	return mimeOfName(name)
}

// mimeOfName maps a file name to a media type.
//
// A short table rather than the system's: this runs inside a listing, once per
// entry, and the answer only has to be good enough for a client to pick an
// icon and decide whether to offer a preview. Anything unrecognised is the
// generic byte stream, which every client handles.
func mimeOfName(name string) string {
	dot := strings.LastIndexByte(name, '.')
	if dot < 0 || dot == len(name)-1 {
		return "application/octet-stream"
	}
	switch strings.ToLower(name[dot+1:]) {
	case "txt", "log", "md":
		return "text/plain"
	case "html", "htm":
		return "text/html"
	case "css":
		return "text/css"
	case "csv":
		return "text/csv"
	case "json":
		return "application/json"
	case "xml":
		return "application/xml"
	case "pdf":
		return "application/pdf"
	case "zip":
		return "application/zip"
	case "gz", "tgz":
		return "application/gzip"
	case "tar":
		return "application/x-tar"
	case "7z":
		return "application/x-7z-compressed"
	case "jpg", "jpeg":
		return "image/jpeg"
	case "png":
		return "image/png"
	case "gif":
		return "image/gif"
	case "webp":
		return "image/webp"
	case "bmp":
		return "image/bmp"
	case "tif", "tiff":
		return "image/tiff"
	case "svg":
		return "image/svg+xml"
	case "heic", "heif":
		return "image/heic"
	case "mp3":
		return "audio/mpeg"
	case "flac":
		return "audio/flac"
	case "wav":
		return "audio/wav"
	case "ogg", "oga":
		return "audio/ogg"
	case "m4a":
		return "audio/mp4"
	case "mp4", "m4v":
		return "video/mp4"
	case "mov":
		return "video/quicktime"
	case "mkv":
		return "video/x-matroska"
	case "webm":
		return "video/webm"
	case "avi":
		return "video/x-msvideo"
	case "doc":
		return "application/msword"
	case "docx":
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	case "xls":
		return "application/vnd.ms-excel"
	case "xlsx":
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	case "ppt":
		return "application/vnd.ms-powerpoint"
	case "pptx":
		return "application/vnd.openxmlformats-officedocument.presentationml.presentation"
	case "odt":
		return "application/vnd.oasis.opendocument.text"
	case "ods":
		return "application/vnd.oasis.opendocument.spreadsheet"
	case "odp":
		return "application/vnd.oasis.opendocument.presentation"
	default:
		return "application/octet-stream"
	}
}

// PreviewableName reports whether a name is one a thumbnail could be made
// from, which is what the has-preview property answers.
//
// Decided from the name rather than by opening the file: the property is
// rendered once per entry in a listing, and reading every file to classify it
// is not a listing. A client that asks for a preview and gets none falls back
// to its own icon.
func PreviewableName(name string) bool {
	return strings.HasPrefix(mimeOfName(name), "image/")
}
