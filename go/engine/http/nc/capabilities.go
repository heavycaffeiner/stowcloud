//go:build linux && compat_nc

package nc

import (
	"strconv"
	"strings"
)

// The capabilities document, the one response every client reads before it
// decides which of its own features to turn on.
//
// A key here is a promise: a client that reads it stops probing for another
// way to do the thing, and shows a person a broken feature when the promise
// does not hold. So every key below is gated on the Features field that
// actually wires the endpoint it promises, and a feature this deployment
// cannot serve is left out rather than set false, because a client checking
// for the key's mere presence (activity, notifications) cannot tell "false"
// from "not asked yet".
//
// The iOS client decodes this with a strict Codable: version.major/minor/
// micro must be JSON numbers, version.string a string, and a present
// files_sharing.public object's "enabled" a boolean. A string where a number
// belongs there fails the whole document, not just that field, and the app
// runs with zero capabilities until the next successful fetch.

// capabilities renders the data block of GET /cloud/capabilities.
func (s *Server) capabilities() Val {
	f := s.deps.Features()
	major, minor, micro := versionParts(f.Version)

	core := []Pair{
		P("pollinterval", Int(60)),
		P("webdav-root", Str("remote.php/webdav")),
		// No rich-link preview endpoint exists behind this; advertised false
		// rather than left out, because the key's presence alone is not what
		// any client here gates on.
		P("reference-api", Bool(false)),
		// Both URL spellings are mounted unconditionally, so there is no
		// broken form for a client's clean-URL detection to find.
		P("mod-rewrite-working", Bool(true)),
	}

	files := []Pair{
		P("bigfilechunking", Bool(f.Chunking)),
		// No locking key: this surface cannot take a lock, and a client shown
		// the capability offers a lock action that fails every time it is
		// used. Nothing here needs the key to be present.
		P("undelete", Bool(f.Trash)),
		// No version store and no comment store exist behind either key.
		P("versioning", Bool(false)),
		P("comments", Bool(false)),
		P("favorites", Bool(f.Favorites)),
		P("blacklisted_files", List()),
		P("forbidden_filenames", List()),
		P("forbidden_filename_basenames", List()),
		P("forbidden_filename_characters", List()),
		P("forbidden_filename_extensions", List()),
	}
	if f.Chunking {
		// Advisory only: the engine accepts a chunk of any size. This is
		// what the desktop client reads to size its own upload sessions.
		files = append(files, P("chunked_upload", Obj(
			P("max_size", Int(0)),
			P("max_parallel_count", Int(5)),
		)))
	}
	// directEditing is deliberately absent: its presence is itself the
	// signal a client uses to call the endpoint behind it, and this
	// deployment has no editor to open a session with.

	dav := []Pair{}
	if f.Chunking {
		dav = append(dav, P("chunking", Str("1.0")))
	}
	// bulkupload is never set: the collection it names is not implemented,
	// and the desktop client string-compares the value before using it, so
	// an empty or absent key both correctly read as "not supported".

	caps := []Pair{
		P("core", Obj(core...)),
		P("dav", Obj(dav...)),
		P("files", Obj(files...)),
	}

	if f.Sharing {
		sharing := []Pair{
			P("api_enabled", Bool(true)),
			// No re-share chain exists past the owner's own grant.
			P("resharing", Bool(false)),
			P("default_permissions", Int(f.DefaultSharePerms)),
			P("group_sharing", Bool(f.UserGroupSharing)),
			P("sharee", Obj(
				P("query_lookup_default", Bool(false)),
				P("always_show_unique", Bool(true)),
			)),
			// No federated share backend exists in this engine.
			P("federation", Obj(
				P("outgoing", Bool(false)),
				P("incoming", Bool(false)),
			)),
		}
		if f.PublicLinks {
			sharing = append(sharing, P("public", Obj(
				P("enabled", Bool(true)),
				P("upload", Bool(f.PublicUpload)),
				P("upload_files_drop", Bool(f.PublicUpload)),
				P("multiple_links", Bool(true)),
				P("send_mail", Bool(false)),
				P("password", Obj(
					P("enforced", Bool(f.LinkPasswordEnforced)),
					P("askForOptionalPassword", Bool(false)),
				)),
				P("expire_date", Obj(
					P("enabled", Bool(true)),
					P("enforced", Bool(f.LinkExpiryEnforced)),
					P("days", Int(int64(f.LinkExpiryDays))),
				)),
			)))
			if f.LinkPasswordEnforced {
				// No policy detail (minimum length, character classes) is
				// configurable in this deployment, so the node reports only
				// that a policy exists and is enforced.
				caps = append(caps, P("password_policy", Obj()))
			}
		}
		if f.UserGroupSharing {
			// Grants carry no expiry field, so a user or group share can
			// never expire here, whatever a link share can do.
			sharing = append(sharing,
				P("user", Obj(
					P("send_mail", Bool(false)),
					P("expire_date", Obj(P("enabled", Bool(false)))),
				)),
				P("group", Obj(
					P("enabled", Bool(true)),
					P("expire_date", Obj(P("enabled", Bool(false)))),
				)),
			)
		}
		caps = append(caps, P("files_sharing", Obj(sharing...)))
	}

	// Both endpoints answer through quietRoute with a sensible empty shape,
	// so advertising them is honest: a client that acts on the key's
	// presence gets a real answer, just an empty one.
	caps = append(caps,
		P("notifications", Obj(P("ocs-endpoints", List()))),
		P("user_status", Obj(
			P("enabled", Bool(true)),
			P("restore", Bool(true)),
			P("supports_emoji", Bool(false)),
		)),
	)

	caps = append(caps, P("end-to-end-encryption", Obj(P("enabled", Bool(false)))))

	caps = append(caps, P("theming", Obj(
		P("name", Str("Stowcloud")),
		P("url", Str("")),
		P("slogan", Str("Cloud Storage")),
		P("color", Str("#1a73e8")),
		P("color-text", Str("#ffffff")),
		P("color-element", Str("#1a73e8")),
		P("color-element-bright", Str("#1a73e8")),
		P("color-element-dark", Str("#1a73e8")),
		P("background", Str("#1a73e8")),
		P("background-plain", Bool(true)),
		P("background-default", Bool(true)),
		P("logo", Str("")),
		P("favicon", Str("")),
	)))

	return Obj(
		P("version", Obj(
			P("major", Int(major)),
			P("minor", Int(minor)),
			P("micro", Int(micro)),
			P("string", Str(f.Version)),
			P("edition", Str("")),
			P("extendedSupport", Bool(false)),
		)),
		P("capabilities", Obj(caps...)),
	)
}

// versionParts splits the dot-separated version into the three numbers the
// document's version block carries. A missing or malformed component reads
// as zero rather than failing the whole document.
func versionParts(v string) (major, minor, micro int64) {
	parts := strings.SplitN(v, ".", 3)
	if len(parts) > 0 {
		major = versionPart(parts[0])
	}
	if len(parts) > 1 {
		minor = versionPart(parts[1])
	}
	if len(parts) > 2 {
		micro = versionPart(parts[2])
	}
	return major, minor, micro
}

// versionPart reads one component. A component that is not a number reads as
// zero, which is the documented behaviour of the block above: a client
// compares the numbers and a refusal here would leave it with no document at
// all.
func versionPart(s string) int64 {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0
	}
	return n
}
