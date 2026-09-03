//go:build linux && compat_nc

// The feature matrix, and the rule that ties it to what is actually wired.
//
// A capability is a promise: a client reading one stops looking for another
// way to do the thing and reports a failure to the user when the endpoint is
// not there. So a feature is advertised only when every port it needs is
// present, and construction refuses rather than shipping a route that answers
// 500 because something was left nil.
package compat

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ErrMissingPort reports an advertised feature with an unwired dependency.
var ErrMissingPort = errors.New("an advertised feature has an unwired port")

// Feature names one thing the compatibility layer may offer.
type Feature uint8

const (
	// FeatureUnset is the zero value and names nothing.
	FeatureUnset Feature = iota
	// FeatureStatus is the status and captive-portal probes.
	FeatureStatus
	// FeatureUser is current and other user lookup with quota.
	FeatureUser
	// FeatureSearch is unified search, recent and favorites.
	FeatureSearch
	// FeaturePreview is preview redirects and direct download URLs.
	FeaturePreview
	// FeatureAppPassword is app-password revocation.
	FeatureAppPassword
	// FeatureUserGroupSharing is OCS share CRUD over grants.
	FeatureUserGroupSharing
	// FeaturePublicLinks is public link shares.
	FeaturePublicLinks
	// FeatureFiles is the vendor DAV file tree.
	FeatureFiles
	// FeatureChunkedUpload is chunked upload v2.
	FeatureChunkedUpload
	// FeatureTrash is the flattened trash collection.
	FeatureTrash
	// FeatureLoginFlow is login flow v2.
	FeatureLoginFlow
)

// String is the feature's name in a diagnostic.
func (f Feature) String() string {
	switch f {
	case FeatureStatus:
		return "status"
	case FeatureUser:
		return "user"
	case FeatureSearch:
		return "search"
	case FeaturePreview:
		return "preview"
	case FeatureAppPassword:
		return "apppassword"
	case FeatureUserGroupSharing:
		return "usergroupsharing"
	case FeaturePublicLinks:
		return "publiclinks"
	case FeatureFiles:
		return "files"
	case FeatureChunkedUpload:
		return "chunkedupload"
	case FeatureTrash:
		return "trash"
	case FeatureLoginFlow:
		return "loginflow"
	case FeatureUnset:
		return "unset"
	default:
		return fmt.Sprintf("Feature(%d)", uint8(f))
	}
}

// Port names one dependency a feature needs from assembly.
type Port uint8

const (
	// PortUnset is the zero value and names nothing.
	PortUnset Port = iota
	// PortFiles resolves, lists and stats files, and looks up a stable id.
	PortFiles
	// PortAccount reads account info, quota and a scoped login.
	PortAccount
	// PortSearch does filename search and recent entries.
	PortSearch
	// PortFavorites reads and writes the starred set.
	PortFavorites
	// PortContentOrigin signs short-lived preview and download URLs.
	PortContentOrigin
	// PortTrash flattens, lists, restores, deletes and empties.
	PortTrash
	// PortUploads holds resumable aliases and chunks.
	PortUploads
	// PortSharing performs OCS share operations.
	PortSharing
	// PortLinks creates and manages public link shares.
	PortLinks
	// PortAppPassword revokes an app password.
	PortAppPassword
	// PortLoginFlow performs the login flow operations.
	PortLoginFlow
)

// String is the port's name in a diagnostic.
func (p Port) String() string {
	switch p {
	case PortFiles:
		return "Files"
	case PortAccount:
		return "Account"
	case PortSearch:
		return "Search"
	case PortFavorites:
		return "Favorites"
	case PortContentOrigin:
		return "ContentOrigin"
	case PortTrash:
		return "Trash"
	case PortUploads:
		return "Uploads"
	case PortSharing:
		return "Sharing"
	case PortLinks:
		return "Links"
	case PortAppPassword:
		return "AppPassword"
	case PortLoginFlow:
		return "LoginFlow"
	case PortUnset:
		return "unset"
	default:
		return fmt.Sprintf("Port(%d)", uint8(p))
	}
}

// requirements is what each feature needs before it may be advertised.
//
//nolint:gochecknoglobals // a lookup table, read-only after init.
var requirements = map[Feature][]Port{
	// The status probe answers from configuration alone.
	FeatureStatus: {},

	FeatureUser:             {PortAccount},
	FeatureSearch:           {PortFiles, PortSearch, PortFavorites},
	FeaturePreview:          {PortFiles, PortContentOrigin},
	FeatureAppPassword:      {PortAppPassword},
	FeatureUserGroupSharing: {PortFiles, PortSharing},
	FeaturePublicLinks:      {PortFiles, PortLinks},
	FeatureFiles:            {PortFiles},
	FeatureChunkedUpload:    {PortFiles, PortUploads},
	FeatureTrash:            {PortTrash},
	FeatureLoginFlow:        {PortLoginFlow, PortAppPassword},
}

// Features returns every feature the matrix knows, sorted by name.
//
// Sorted because Go iterates a map in random order and a caller listing the
// features would otherwise get a different order each call. Nothing here
// depends on the order today: the capability document is built from named
// lookups rather than by walking this list, so the sort is for a reader and
// for a future caller that does depend on it.
func Features() []Feature {
	out := make([]Feature, 0, len(requirements))
	for f := range requirements {
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out
}

// Requires returns what a feature needs, and whether the matrix knows it.
func Requires(f Feature) ([]Port, bool) {
	ports, ok := requirements[f]
	return ports, ok
}

// Wiring is what assembly actually attached.
type Wiring struct {
	// Present is the set of ports that are not nil.
	Present map[Port]bool
}

// Has reports whether one port is attached.
func (w Wiring) Has(p Port) bool { return w.Present[p] }

// Advertised returns the features this wiring may promise.
//
// Derived rather than configured: a deployment cannot turn on a capability
// whose port is missing, because the answer is computed from what is there.
func Advertised(w Wiring) []Feature {
	var out []Feature
	for _, f := range Features() {
		if missing(f, w) == nil {
			out = append(out, f)
		}
	}
	return out
}

// missing returns the first unwired port a feature needs, or nil.
func missing(f Feature, w Wiring) error {
	ports, known := Requires(f)
	if !known {
		return fmt.Errorf("%w: %s is not in the matrix", ErrMissingPort, f)
	}
	for _, p := range ports {
		if !w.Has(p) {
			return fmt.Errorf("%w: %s needs %s", ErrMissingPort, f, p)
		}
	}
	return nil
}

// Validate refuses a wiring that advertises what it cannot serve.
//
// want is what the deployment asked to offer. Every one of those must have its
// ports, and the check reports all of them at once rather than one restart at
// a time. The sentinel survives the joining, so a caller can still ask what
// kind of failure this was.
func Validate(want []Feature, w Wiring) error {
	var problems []string
	for _, f := range want {
		if err := missing(f, w); err != nil {
			problems = append(problems, strings.TrimPrefix(err.Error(), ErrMissingPort.Error()+": "))
		}
	}
	if len(problems) == 0 {
		return nil
	}
	sort.Strings(problems)
	return fmt.Errorf("%w: %s", ErrMissingPort, strings.Join(problems, "; "))
}

// Capabilities renders the advertised set as the wire document.
//
// A key that means "try this endpoint" is omitted rather than set false. A
// client reading false for a presence-sensitive key behaves differently from
// one reading nothing, and the reference omits.
func Capabilities(w Wiring, version string) Val {
	on := map[Feature]bool{}
	for _, f := range Advertised(w) {
		on[f] = true
	}

	files := []Pair{
		P("bigfilechunking", Bool(on[FeatureChunkedUpload])),
		P("undelete", Bool(on[FeatureTrash])),
	}
	if on[FeatureChunkedUpload] {
		// Advisory, not a ceiling: the server accepts a chunk of any size and
		// this only tells a client what to aim for.
		files = append(files, P("chunked_upload", Map(
			P("max_size", Int(0)),
			P("max_parallel_count", Int(5)),
		)))
	}

	caps := []Pair{
		P("core", Map(
			P("pollinterval", Int(60)),
			P("webdav-root", Str("remote.php/webdav")),
		)),
		P("dav", Map(
			P("chunking", Str("1.0")),
		)),
		P("theming", Map(
			P("name", Str("Stowcloud")),
			P("slogan", Str("Cloud Storage")),
			P("color", Str("#1a73e8")),
			P("text-color", Str("#ffffff")),
		)),
		P("files", Map(files...)),
	}
	if on[FeatureTrash] {
		caps = append(caps, P("files_trashbin", Bool(true)))
	}

	// Sharing appears only when something can serve it. An empty sharing block
	// tells a client sharing exists and every attempt then fails.
	if on[FeatureUserGroupSharing] || on[FeaturePublicLinks] {
		sharing := []Pair{
			P("api_enabled", Bool(true)),
			// Grant chains are not offered, so this is the truth rather than
			// a default.
			P("resharing", Bool(false)),
		}
		if on[FeaturePublicLinks] {
			sharing = append(sharing, P("public", Map(
				P("enabled", Bool(true)),
				P("upload", Bool(true)),
				P("password", Map(P("enforced", Bool(false)))),
			)))
		}
		if on[FeatureUserGroupSharing] {
			sharing = append(sharing,
				P("user", Map(P("send_mail", Bool(false)))),
				P("group_sharing", Bool(true)),
			)
		}
		caps = append(caps, P("files_sharing", Map(sharing...)))
	}
	return Map(
		P("version", Map(
			P("string", Str(version)),
			P("major", Int(31)),
			P("minor", Int(0)),
			P("micro", Int(4)),
			P("edition", Str("")),
		)),
		P("capabilities", Map(caps...)),
	)
}
