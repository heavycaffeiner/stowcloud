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

// GrantIDBase separates two id spaces the reference keeps in one.
//
// A link and a grant are numbered by their own tables here, so a bare id
// names one of each and a client asking about "share 1" gets whichever the
// server looks at first: an app deleting the share it listed could remove a
// different one. Grants are offset past any id a link table will reach, and
// the offset is a power of two so a reader can see which space an id is in.
const GrantIDBase int64 = 1 << 40

// GrantShareID renders a grant's id in the wire space.
func GrantShareID(id int64) int64 { return id + GrantIDBase }

// ShareIDOf reads a wire id back, reporting whether it names a grant.
func ShareIDOf(wire int64) (int64, bool) {
	if wire >= GrantIDBase {
		return wire - GrantIDBase, true
	}
	return wire, false
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

// Sharee is one candidate target of a share.
type Sharee struct {
	// Kind selects the wire share type.
	Kind GranteeKind
	// ID is the name a share names the target by.
	ID string
	// Display is what the picker shows.
	Display string
}

// ShareeQuery is what a sharee search asked for.
type ShareeQuery struct {
	Search  string
	Page    int
	PerPage int
}

// ParseShareeQuery reads the sharee search parameters.
//
// A page or size that is absent, unparseable or out of range takes the
// reference's default rather than refusing: the picker sends what its library
// filled in, and a refusal there reads to a person as a server that cannot
// list anybody.
func ParseShareeQuery(get func(string) string) ShareeQuery {
	q := ShareeQuery{Search: strings.TrimSpace(get("search")), Page: 1, PerPage: 200}
	if n, err := strconv.Atoi(get("page")); err == nil && n > 0 {
		q.Page = n
	}
	if n, err := strconv.Atoi(get("perPage")); err == nil && n > 0 {
		q.PerPage = min(n, 500)
	}
	return q
}

// Matches reports whether a candidate answers the search.
//
// Case-insensitive on both the name and the label, because a person types the
// display name they were shown and the reference matches either.
func (q ShareeQuery) Matches(s Sharee) bool {
	if q.Search == "" {
		return true
	}
	needle := strings.ToLower(q.Search)
	return strings.Contains(strings.ToLower(s.ID), needle) ||
		strings.Contains(strings.ToLower(s.Display), needle)
}

// Exact reports whether a candidate is the thing named rather than a near miss.
func (q ShareeQuery) Exact(s Sharee) bool {
	return q.Search != "" &&
		(strings.EqualFold(s.ID, q.Search) || strings.EqualFold(s.Display, q.Search))
}

// Window narrows a candidate list to the slice the query's page asked for.
func (q ShareeQuery) Window(items []Val) []Val {
	start := (q.Page - 1) * q.PerPage
	if start >= len(items) {
		return nil
	}
	return items[start:min(start+q.PerPage, len(items))]
}

// ShareeEntry renders one candidate.
func ShareeEntry(s Sharee) Val {
	return Map(
		P("label", Str(s.Display)),
		P("name", Str(s.ID)),
		P("shareWithDisplayNameUnique", Str(s.ID)),
		P("value", Map(
			P("shareType", Int(ShareTypeOf(s.Kind))),
			P("shareWith", Str(s.ID)),
		)),
	)
}

// ShareesPage renders the sharee search document.
//
// Every list the reference sends is present even when empty. The client reads
// each array by name and aborts its parse when one is missing, so an omitted
// empty list is a failed search rather than an empty one, and the person sees
// an error where they should have seen "no matches".
func ShareesPage(exactUsers, exactGroups, users, groups []Val) Val {
	empty := func() Val { return ListOf(nil) }
	return Map(
		P("exact", Map(
			P("users", ListOf(exactUsers)),
			P("groups", ListOf(exactGroups)),
			P("remotes", empty()),
			P("remote_groups", empty()),
			P("emails", empty()),
			P("circles", empty()),
			P("rooms", empty()),
		)),
		P("users", ListOf(users)),
		P("groups", ListOf(groups)),
		P("remotes", empty()),
		P("remote_groups", empty()),
		P("emails", empty()),
		P("circles", empty()),
		P("rooms", empty()),
		P("lookup", empty()),
		P("lookupEnabled", Bool(false)),
	)
}
