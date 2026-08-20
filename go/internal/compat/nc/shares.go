//go:build compat_nc

package nc

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/internal/compat/ncport"
)

// The share surface.
//
// Reproduced exactly, including the parts that are wrong. The field types are
// not uniform in the reference and clients depend on the inconsistency, so
// making them uniform here is a change every client would notice.

// GranteeKind is who a share is with.
type GranteeKind int

const (
	GranteeUser GranteeKind = iota
	GranteeGroup
	GranteeLink
)

// The wire numbers for a share type.
const (
	ShareTypeUser       = 0
	ShareTypeGroup      = 1
	ShareTypePublicLink = 3
)

// ShareTypeOf is the wire number for a grantee kind.
func ShareTypeOf(k GranteeKind) int64 {
	switch k {
	case GranteeGroup:
		return ShareTypeGroup
	case GranteeLink:
		return ShareTypePublicLink
	}
	return ShareTypeUser
}

// KindOfShareType is the inverse, refusing a type this server does not offer.
func KindOfShareType(t int64) (GranteeKind, *OCSError) {
	switch t {
	case ShareTypeUser:
		return GranteeUser, nil
	case ShareTypeGroup:
		return GranteeGroup, nil
	case ShareTypePublicLink:
		return GranteeLink, nil
	}
	return 0, BadRequest("Unknown share type")
}

// Share is one share, as this layer renders it.
type Share struct {
	ID           int64
	Kind         GranteeKind
	Owner        string
	OwnerDisplay string
	Perms        ncport.Perms
	CreatedS     int64
	ExpiresS     *int64
	Token        string
	Note         string
	Label        string
	Path         string
	IsDir        bool
	FileID       FileID
	ParentFileID FileID
	// Grantee and GranteeDisplay name the account or group a share is with,
	// and are empty for a link.
	Grantee        string
	GranteeDisplay string
	HasPassword    bool
	// URL is the link a public share is fetched through.
	URL string
}

// FormatShare renders one share.
//
// The field types are the reference's and are deliberately not made uniform.
// The id is a string. Two of the flags are integers where three neighbouring
// ones are booleans. The password is never the real password: it is absent or
// a fixed placeholder. The parent is always absent.
func FormatShare(s Share) Val {
	perms := SharePermissions(s.Perms)
	itemType := "file"
	mime := "application/octet-stream"
	if s.IsDir {
		itemType = "folder"
		mime = "httpd/unix-directory"
	}

	fields := []Field{
		// A string, not a number. The reference's own accessor returns one and
		// clients read it as one.
		F("id", VStr(strconv.FormatInt(s.ID, 10))),
		F("share_type", VInt(ShareTypeOf(s.Kind))),
		F("uid_owner", VStr(s.Owner)),
		F("displayname_owner", VStr(s.OwnerDisplay)),
		F("permissions", VInt(perms)),
		// Real booleans, unlike the two integer flags at the end.
		F("can_edit", VBool(true)),
		F("can_delete", VBool(true)),
		F("stime", VInt(s.CreatedS)),
		// Hardcoded absent in the reference and never overwritten.
		F("parent", VNull()),
		F("expiration", expirationVal(s.ExpiresS)),
		F("token", optionalStr(emptyToNil(s.Token))),
		F("uid_file_owner", VStr(s.Owner)),
		F("note", VStr(s.Note)),
		F("label", VStr(s.Label)),
		F("displayname_file_owner", VStr(s.OwnerDisplay)),
		F("path", VStr(s.Path)),
		F("item_type", VStr(itemType)),
		F("item_permissions", VInt(perms)),
		F("is-mount-root", VBool(false)),
		F("mount-type", VStr("")),
		// The type is never sniffed or guessed: a server asserting one risks
		// it being trusted for a serving decision. Clients fall back to
		// extension-based detection.
		F("mimetype", VStr(mime)),
		F("has_preview", VBool(false)),
		F("storage_id", VStr("home")),
		F("storage", VInt(1)),
		F("item_source", fileIDVal(s.FileID)),
		F("file_source", fileIDVal(s.FileID)),
		F("file_parent", fileIDVal(s.ParentFileID)),
		F("file_target", VStr(s.Path)),
	}

	switch s.Kind {
	case GranteeUser:
		display := s.GranteeDisplay
		if display == "" {
			display = s.Grantee
		}
		fields = append(fields,
			F("share_with", VStr(s.Grantee)),
			F("share_with_displayname", VStr(display)),
			F("share_with_displayname_unique", VStr(s.Grantee)),
		)
	case GranteeGroup:
		display := s.GranteeDisplay
		if display == "" {
			display = s.Grantee
		}
		fields = append(fields,
			F("share_with", VStr(s.Grantee)),
			F("share_with_displayname", VStr(display)),
		)
	case GranteeLink:
		// Never the real password. A placeholder says one is set without
		// saying what it is, and absent says there is none.
		password := VNull()
		if s.HasPassword {
			password = VStr(redactedPassword)
		}
		fields = append(fields,
			F("share_with", password),
			F("share_with_displayname", VStr("(Shared link)")),
			F("password", password),
			F("send_password_by_talk", VBool(false)),
			F("url", optionalStr(emptyToNil(s.URL))),
		)
	}

	// Always last, in this order, and both are integers rather than booleans.
	hidden := int64(0)
	if s.Perms.Has(ncport.Read) && !s.Perms.Has(ncport.Download) {
		hidden = 1
	}
	fields = append(fields,
		F("mail_send", VInt(0)),
		F("hide_download", VInt(hidden)),
		F("attributes", VNull()),
	)
	return VMap(fields...)
}

// fileIDVal renders a file id as the number a client reads.
//
// Clamped rather than reinterpreted: an id above the signed range would render
// negative, and a client stores it as the key of its local sync journal. No id
// this server mints reaches that, which is why an unclamped conversion would
// go unnoticed.
func fileIDVal(id FileID) Val { return bytesVal(uint64(id)) }

// redactedPassword is what a link with a password reports instead of one.
const redactedPassword = "redacted"

// expirationVal renders an expiry.
//
// Deliberately not a full timestamp format: clients parse this with a fixed
// pattern, and a date-time separator or a zone suffix breaks them.
func expirationVal(unixS *int64) Val {
	if unixS == nil {
		return VNull()
	}
	t := time.Unix(*unixS, 0).UTC()
	return VStr(fmt.Sprintf("%04d-%02d-%02d %02d:%02d:%02d",
		t.Year(), int(t.Month()), t.Day(), t.Hour(), t.Minute(), t.Second()))
}

// ShareRequest is a create or update, read from a form.
type ShareRequest struct {
	Path        string
	ShareType   *int64
	ShareWith   string
	Permissions *int64
	Password    string
	Expiration  string
	Note        string
	Label       string
}

// ShareRequestFromForm reads the parameters a client sends.
//
// Absent and empty are kept apart for the two that mean something different:
// an absent permission set means "leave it alone" on an update, where an empty
// one would mean "remove everything".
func ShareRequestFromForm(get func(string) string, has func(string) bool) ShareRequest {
	r := ShareRequest{
		Path:      get("path"),
		ShareWith: get("shareWith"),
		Password:  get("password"),
		Note:      get("note"),
		Label:     get("label"),
	}
	if has("shareType") {
		if n, err := strconv.ParseInt(get("shareType"), 10, 64); err == nil {
			r.ShareType = &n
		}
	}
	if has("permissions") {
		if n, err := strconv.ParseInt(get("permissions"), 10, 64); err == nil {
			r.Permissions = &n
		}
	}
	if has("expireDate") {
		r.Expiration = get("expireDate")
	}
	return r
}

// NormaliseClientPath cleans a path a client sent.
//
// A folder path arrives from one client with a trailing separator, and it is a
// virtual path in both directions. An unusable one becomes empty, which lists
// everything the caller owns: that is what the reference does with an absent
// path, and treating a malformed one as a filter nobody matches would silently
// hide every share instead.
func NormaliseClientPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	p = strings.TrimPrefix(p, "/")
	p = strings.TrimSuffix(p, "/")
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." || seg == "." {
			return ""
		}
	}
	return p
}

// ShareFilter is what a listing narrows on.
type ShareFilter struct {
	Path string
	// The three flags the reference compares against the literal string
	// "true", so other spellings are false there and are false here too.
	Reshares     bool
	Subfiles     bool
	SharedWithMe bool
}

// ParseShareFilter reads a listing's query.
func ParseShareFilter(get func(string) string) ShareFilter {
	return ShareFilter{
		Path:         NormaliseClientPath(get("path")),
		Reshares:     get("reshares") == "true",
		Subfiles:     get("subfiles") == "true",
		SharedWithMe: get("shared_with_me") == "true",
	}
}

func emptyToNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
