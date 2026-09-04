//go:build linux && compat_nc

package compat

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// GranteeKind is who a share is with.
type GranteeKind int

const (
	GranteeUser GranteeKind = iota
	GranteeGroup
	GranteeLink
)

// Wire numbers for share types in Nextcloud.
const (
	ShareTypeUser       = 0
	ShareTypeGroup      = 1
	ShareTypePublicLink = 3
)

// Wire permissions for share entries in Nextcloud.
const (
	SharePermRead   = 1
	SharePermUpdate = 2
	SharePermCreate = 4
	SharePermDelete = 8
	SharePermShare  = 16
	SharePermAll    = 31
)

// ShareTypeOf maps a grantee kind to its wire number.
func ShareTypeOf(k GranteeKind) int64 {
	switch k {
	case GranteeGroup:
		return ShareTypeGroup
	case GranteeLink:
		return ShareTypePublicLink
	default:
		return ShareTypeUser
	}
}

// KindOfShareType maps a wire number to its grantee kind.
func KindOfShareType(t int64) (GranteeKind, *OCSError) {
	switch t {
	case ShareTypeUser:
		return GranteeUser, nil
	case ShareTypeGroup:
		return GranteeGroup, nil
	case ShareTypePublicLink:
		return GranteeLink, nil
	default:
		return 0, BadRequest("Unknown share type")
	}
}

// Share is one share record formatted for the OCS response.
type Share struct {
	ID             int64
	Kind           GranteeKind
	Owner          string
	OwnerDisplay   string
	Perms          int64
	CreatedS       int64
	ExpiresS       *int64
	Token          string
	Note           string
	Label          string
	Path           string
	IsDir          bool
	FileID         uint64
	ParentFileID   uint64
	Grantee        string
	GranteeDisplay string
	HasPassword    bool
	URL            string
}

const redactedPassword = "redacted"

// FormatShare renders one share in the exact structure clients expect.
func FormatShare(s Share) Val {
	perms := s.Perms
	itemType := "file"
	mime := "application/octet-stream"
	if s.IsDir {
		itemType = "folder"
		mime = "httpd/unix-directory"
	}

	fields := []Pair{
		P("id", Str(strconv.FormatInt(s.ID, 10))),
		P("share_type", Int(ShareTypeOf(s.Kind))),
		P("uid_owner", Str(s.Owner)),
		P("displayname_owner", Str(s.OwnerDisplay)),
		P("permissions", Int(perms)),
		P("can_edit", Bool(true)),
		P("can_delete", Bool(true)),
		P("stime", Int(s.CreatedS)),
		P("parent", Empty()),
		P("expiration", expirationVal(s.ExpiresS)),
		P("token", Str(s.Token)),
		P("uid_file_owner", Str(s.Owner)),
		P("note", Str(s.Note)),
		P("label", Str(s.Label)),
		P("displayname_file_owner", Str(s.OwnerDisplay)),
		P("path", Str(s.Path)),
		P("item_type", Str(itemType)),
		P("item_permissions", Int(perms)),
		P("is-mount-root", Bool(false)),
		P("mount-type", Str("")),
		P("mimetype", Str(mime)),
		P("has_preview", Bool(false)),
		P("storage_id", Str("home")),
		P("storage", Int(1)),
		P("item_source", BytesVal(s.FileID)),
		P("file_source", BytesVal(s.FileID)),
		P("file_parent", BytesVal(s.ParentFileID)),
		P("file_target", Str(s.Path)),
	}

	switch s.Kind {
	case GranteeUser:
		display := s.GranteeDisplay
		if display == "" {
			display = s.Grantee
		}
		fields = append(fields,
			P("share_with", Str(s.Grantee)),
			P("share_with_displayname", Str(display)),
			P("share_with_displayname_unique", Str(s.Grantee)),
		)
	case GranteeGroup:
		display := s.GranteeDisplay
		if display == "" {
			display = s.Grantee
		}
		fields = append(fields,
			P("share_with", Str(s.Grantee)),
			P("share_with_displayname", Str(display)),
		)
	case GranteeLink:
		password := Empty()
		if s.HasPassword {
			password = Str(redactedPassword)
		}
		fields = append(fields,
			P("share_with", password),
			P("share_with_displayname", Str("(Shared link)")),
			P("password", password),
			P("send_password_by_talk", Bool(false)),
			P("url", Str(s.URL)),
		)
	}

	hidden := int64(0)
	fields = append(fields,
		P("mail_send", Int(0)),
		P("hide_download", Int(hidden)),
		P("attributes", Empty()),
	)
	return Map(fields...)
}

func expirationVal(unixS *int64) Val {
	if unixS == nil {
		return Str("")
	}
	t := time.Unix(*unixS, 0).UTC()
	return Str(fmt.Sprintf("%04d-%02d-%02d %02d:%02d:%02d",
		t.Year(), int(t.Month()), t.Day(), t.Hour(), t.Minute(), t.Second()))
}

// ShareRequest is a create or update request parsed from query or form.
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

// ShareRequestFromForm reads parameters from form or query lookups.
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

// NormaliseClientPath strips leading and trailing slashes and rejects traversal.
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

// ShareFilter is what an OCS shares query selects.
type ShareFilter struct {
	Path         string
	Reshares     bool
	Subfiles     bool
	SharedWithMe bool
}

// ParseShareFilter reads share filter parameters.
func ParseShareFilter(get func(string) string) ShareFilter {
	return ShareFilter{
		Path:         NormaliseClientPath(get("path")),
		Reshares:     get("reshares") == "true",
		Subfiles:     get("subfiles") == "true",
		SharedWithMe: get("shared_with_me") == "true",
	}
}

// SharePermissions converts an internal permission bitmask to Nextcloud's integer mask.
func SharePermissions(p PermBits) int64 {
	var bits int64
	if p.Has(PermRead) {
		bits |= SharePermRead
	}
	if p.Has(PermWrite) || p.Has(PermMove) {
		bits |= SharePermUpdate
	}
	if p.Has(PermCreate) {
		bits |= SharePermCreate
	}
	if p.Has(PermDelete) {
		bits |= SharePermDelete
	}
	if p.Has(PermShare) {
		bits |= SharePermShare
	}
	return bits
}
