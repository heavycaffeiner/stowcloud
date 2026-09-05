// The v1 route table.
//
// One entry per endpoint the native web client calls, and nothing else: the
// public link surface, WebDAV, the Nextcloud compatibility routes and the
// emergency door own their own wire paths and are mounted elsewhere.
//
// Access classes come from the category rather than from each route, so what a
// row declares is the exception rather than the rule. The categories and their
// defaults are stated once in defaultAccess below; a route needing something
// else says so and says why.

package server

import (
	"github.com/heavycaffeiner/stowcloud/go/engine/http/route"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
)

// Base is the prefix every route here carries. The version is in the path so
// the next breaking change is a v2 mounted beside v1 rather than another flag
// day.
const Base = "/api/v1"

// defaultAccess is the credential class a category demands unless a route
// overrides it.
//
// Declaring it per category rather than per route is what makes an exception
// visible: a session-only route inside a permission-scoped category is one line
// that reads differently from its neighbours, where ninety separate
// declarations would hide it.
func defaultAccess() map[string]route.Requirement {
	return map[string]route.Requirement{
		// Administration is the browser's own surface. An app password is a
		// filesystem credential handed to a device, and a device must not be able
		// to create users or rewrite the settings document.
		"admin": {Access: route.AccessSession},

		// Authentication and account self-management are session-only for the same
		// reason, with the login and OIDC entry points as the public exceptions
		// below: they are where a session comes from.
		"auth":    {Access: route.AccessSession},
		"account": {Access: route.AccessSession},

		// The file surfaces are permission-scoped and reachable with an app
		// password carrying the bits, which is what makes a sync client possible.
		"files":   {Access: route.AccessPerms, Perms: acl.Read},
		"trash":   {Access: route.AccessPerms, Perms: acl.Read},
		"uploads": {Access: route.AccessPerms, Perms: acl.Write | acl.Create},
		"search":  {Access: route.AccessPerms, Perms: acl.Read},

		// Minting a link for a stranger is sharing, so the whole category demands
		// the sharing bit. The old tree had one half of this family requiring only
		// Read; merging them tightened that half rather than loosening the other.
		"links": {Access: route.AccessPerms, Perms: acl.Share},

		// Bookkeeping a caller does about work it started, which a device does
		// legitimately on its own behalf.
		"jobs":   {Access: route.AccessAnyCredential},
		"events": {Access: route.AccessAnyCredential},

		// This category serves a share's salt and verifier, the two public
		// values a client needs to derive and check a passphrase.
		"encryption": {Access: route.AccessAnyCredential},

		// The system category has no default worth having: health is public
		// and setup has the setup gate's own rule, so both say so per route.
		"system": {Access: route.AccessUnset},
	}
}

// exception is a route whose access differs from its category's default, with
// the reason recorded beside it.
type exception struct {
	req route.Requirement
	why string
}

// exceptions maps "METHOD /path" to the class that route demands instead.
//
// Every entry carries its reason. An exception without one is a hole somebody
// added and nobody has to defend.
func exceptions() map[string]exception {
	return map[string]exception{
		// Where a session comes from. A caller reaching these has no session yet,
		// so demanding one would close the only door in.
		"POST " + Base + "/auth/login": {
			route.Requirement{Access: route.AccessPublic},
			"the login itself: the caller has no session to present",
		},
		"POST " + Base + "/auth/login/totp": {
			route.Requirement{Access: route.AccessPublic},
			"the second factor step, still before a session exists",
		},
		"GET " + Base + "/auth/oidc/config": {
			route.Requirement{Access: route.AccessPublic},
			"the sign-in screen reads it to decide whether to offer the provider",
		},
		"GET " + Base + "/auth/oidc/start": {
			route.Requirement{Access: route.AccessPublic},
			"a browser navigation that begins the flow",
		},
		"GET " + Base + "/auth/oidc/callback": {
			route.Requirement{Access: route.AccessPublic},
			"the provider sends the browser here with no credential of ours",
		},

		// Logging out with an app password is a device disposing of its own
		// session, which is a thing to permit rather than refuse.
		"POST " + Base + "/auth/logout": {
			route.Requirement{Access: route.AccessAnyCredential},
			"any authenticated caller may end its own session",
		},

		// Protocol discovery carries no credential by definition: the client is
		// asking what the server supports before it has anything to present.
		"OPTIONS " + Base + "/uploads": {
			route.Requirement{Access: route.AccessPublic},
			"resumable-protocol discovery, which precedes any credential",
		},
		"OPTIONS " + Base + "/uploads/{id}": {
			route.Requirement{Access: route.AccessPublic},
			"per-resource protocol discovery, same reason",
		},

		// A container probe runs before anything is configured.
		"GET " + Base + "/system/health": {
			route.Requirement{Access: route.AccessPublic},
			"the container probe, which runs before a deployment has accounts",
		},
		// The setup gate closes for good the instant an administrator exists, so
		// its own rule governs these rather than a credential class.
		"GET " + Base + "/system/setup": {
			route.Requirement{Access: route.AccessPublic},
			"first-boot only: the gate refuses once any administrator exists",
		},
		"POST " + Base + "/system/setup": {
			route.Requirement{Access: route.AccessPublic},
			"first-boot only, guarded by the same gate rather than by a credential",
		},

		// Reading a file needs Read; writing to one needs more. Stated per route
		// because the files category is one noun covering both.
		"POST " + Base + "/files/mkdir": {
			route.Requirement{Access: route.AccessPerms, Perms: acl.Create},
			"creating a directory is a create rather than a read",
		},
		"POST " + Base + "/files/write": {
			route.Requirement{Access: route.AccessPerms, Perms: acl.Write | acl.Create},
			"writing a file may create it",
		},
		"POST " + Base + "/files/delete": {
			route.Requirement{Access: route.AccessPerms, Perms: acl.Delete},
			"deleting is its own bit",
		},
		"POST " + Base + "/files/move": {
			route.Requirement{Access: route.AccessPerms, Perms: acl.Move},
			"moving is its own bit",
		},
		"POST " + Base + "/files/copy": {
			route.Requirement{Access: route.AccessPerms, Perms: acl.Read | acl.Create},
			"a copy reads the source and creates the destination",
		},
		"POST " + Base + "/files/rename": {
			route.Requirement{Access: route.AccessPerms, Perms: acl.Rename},
			"renaming is its own bit",
		},
		"GET " + Base + "/files/read": {
			route.Requirement{Access: route.AccessPerms, Perms: acl.Read | acl.Download},
			"reading the bytes is a download, which is a bit of its own",
		},
		"GET " + Base + "/files/thumbnail": {
			route.Requirement{Access: route.AccessPerms, Perms: acl.Read | acl.Download},
			"a thumbnail derives from the bytes, so seeing one is seeing the file",
		},
		"POST " + Base + "/files/archive": {
			route.Requirement{Access: route.AccessPerms, Perms: acl.Read | acl.Download},
			"an archive streams the bytes out, which is a download",
		},
		"POST " + Base + "/files/download": {
			route.Requirement{Access: route.AccessPerms, Perms: acl.Read | acl.Download},
			"minting a download ticket is agreeing to hand over the bytes",
		},

		// Restoring and purging change the tree rather than reading it.
		"POST " + Base + "/trash/restore": {
			route.Requirement{Access: route.AccessPerms, Perms: acl.Create},
			"a restore puts a file back, which is a create",
		},
		"POST " + Base + "/trash/purge": {
			route.Requirement{Access: route.AccessPerms, Perms: acl.Delete},
			"a purge is the delete that cannot be undone",
		},

		// The verifier lets an offline attacker check passphrase guesses at
		// rclone's fixed scrypt cost, so reading it demands the same browser
		// session as the two mutations below rather than any app password.
		"GET " + Base + "/encryption": {
			route.Requirement{Access: route.AccessSession},
			"the verifier is an offline dictionary-attack target, so only a browser session may read it",
		},

		// Turning a share's encryption on or off is a decision about the
		// deployment's own share registry, the same class of decision every
		// other admin/* mutation demands a browser session for.
		"POST " + Base + "/encryption/{id}": {
			route.Requirement{Access: route.AccessSession},
			"enabling a share's encryption is administrative, and only a browser session authenticates one",
		},
		"DELETE " + Base + "/encryption/{id}": {
			route.Requirement{Access: route.AccessSession},
			"disabling carries the same administrative weight as enabling",
		},
	}
}

// Table is the complete v1 surface.
//
// The order is the category order the document uses, which is also the order a
// route dump prints in, so comparing a dump against the document is reading two
// lists in the same sequence.
func Table() []route.Route {
	var out []route.Route
	add := func(method, path, name string, body route.BodyClass) {
		out = append(out, route.Route{
			Method: method, Path: Base + path, Name: name,
			Requirement: requirementFor(method, Base+path),
			Body:        body,
		})
	}

	// auth: authentication itself.
	add("POST", "/auth/login", "auth.login", route.BodyJSON)
	add("POST", "/auth/login/totp", "auth.login.totp", route.BodyJSON)
	add("POST", "/auth/logout", "auth.logout", route.BodyNone)
	add("GET", "/auth/session", "auth.session", route.BodyNone)
	add("GET", "/auth/oidc/config", "auth.oidc.config", route.BodyNone)
	add("GET", "/auth/oidc/start", "auth.oidc.start", route.BodyNone)
	add("GET", "/auth/oidc/callback", "auth.oidc.callback", route.BodyNone)

	// account: the caller's own account.
	add("POST", "/account/password", "account.password", route.BodyJSON)
	add("GET", "/account/sessions", "account.sessions.list", route.BodyNone)
	add("DELETE", "/account/sessions/{id}", "account.sessions.delete", route.BodyNone)
	add("GET", "/account/app-passwords", "account.app-passwords.list", route.BodyNone)
	add("POST", "/account/app-passwords", "account.app-passwords.create", route.BodyJSON)
	add("DELETE", "/account/app-passwords/{id}", "account.app-passwords.delete", route.BodyNone)
	add("POST", "/account/app-passwords/{id}/wipe", "account.app-passwords.wipe", route.BodyNone)
	add("POST", "/account/totp/setup", "account.totp.setup", route.BodyNone)
	add("POST", "/account/totp/enroll", "account.totp.enroll", route.BodyJSON)
	add("POST", "/account/totp/disable", "account.totp.disable", route.BodyJSON)
	add("GET", "/account/totp/recovery-codes", "account.totp.recovery-codes.list", route.BodyNone)
	add("POST", "/account/totp/recovery-codes", "account.totp.recovery-codes.create", route.BodyJSON)
	add("POST", "/account/smb", "account.smb.create", route.BodyJSON)
	add("POST", "/account/smb/password", "account.smb.password.set", route.BodyJSON)
	add("DELETE", "/account/smb/password", "account.smb.password.delete", route.BodyNone)
	add("POST", "/account/oidc-link/start", "account.oidc-link.start", route.BodyJSON)
	add("DELETE", "/account/oidc-link", "account.oidc-link.delete", route.BodyNone)

	// files: RPC verbs, with the path as an argument rather than a URL segment.
	add("GET", "/files/list", "files.list", route.BodyNone)
	add("GET", "/files/stat", "files.stat", route.BodyNone)
	add("GET", "/files/read", "files.read", route.BodyNone)
	add("GET", "/files/size", "files.size", route.BodyNone)
	add("GET", "/files/thumbnail", "files.thumbnail", route.BodyNone)
	add("POST", "/files/mkdir", "files.mkdir", route.BodyJSON)
	add("POST", "/files/write", "files.write", route.BodyStream)
	add("POST", "/files/delete", "files.delete", route.BodyJSON)
	add("POST", "/files/move", "files.move", route.BodyJSON)
	add("POST", "/files/copy", "files.copy", route.BodyJSON)
	add("POST", "/files/rename", "files.rename", route.BodyJSON)
	add("POST", "/files/archive", "files.archive", route.BodyJSON)
	add("GET", "/files/archive/fetch", "files.archive.fetch", route.BodyNone)
	add("GET", "/files/archive/list", "files.archive.list", route.BodyNone)
	add("POST", "/files/download", "files.download", route.BodyJSON)
	add("GET", "/files/download/fetch", "files.download.fetch", route.BodyNone)
	add("GET", "/files/recent", "files.recent", route.BodyNone)

	// links: the caller's public share links.
	add("GET", "/links", "links.list", route.BodyNone)
	add("POST", "/links", "links.create", route.BodyJSON)
	add("PATCH", "/links/{id}", "links.update", route.BodyJSON)
	add("DELETE", "/links/{id}", "links.delete", route.BodyNone)

	// trash.
	add("GET", "/trash", "trash.list", route.BodyNone)
	add("POST", "/trash/restore", "trash.restore", route.BodyJSON)
	add("POST", "/trash/purge", "trash.purge", route.BodyJSON)

	// jobs: long operations.
	add("GET", "/jobs", "jobs.list", route.BodyNone)
	add("GET", "/jobs/{id}", "jobs.get", route.BodyNone)
	add("POST", "/jobs/{id}/cancel", "jobs.cancel", route.BodyNone)

	// uploads: the resumable protocol.
	add("OPTIONS", "/uploads", "uploads.discover", route.BodyNone)
	add("POST", "/uploads", "uploads.create", route.BodyNone)
	add("HEAD", "/uploads/{id}", "uploads.status", route.BodyNone)
	add("PATCH", "/uploads/{id}", "uploads.patch", route.BodyStream)
	add("DELETE", "/uploads/{id}", "uploads.abort", route.BodyNone)
	add("OPTIONS", "/uploads/{id}", "uploads.discover.one", route.BodyNone)

	// search.
	add("GET", "/search/stream", "search.stream", route.BodyNone)

	// encryption: opt-in, zero-knowledge per-share content encryption.
	add("GET", "/encryption", "encryption.list", route.BodyNone)
	add("POST", "/encryption/{id}", "admin.encryption.enable", route.BodyJSON)
	add("DELETE", "/encryption/{id}", "admin.encryption.disable", route.BodyNone)

	// admin.
	add("GET", "/admin/users", "admin.users.list", route.BodyNone)
	add("POST", "/admin/users", "admin.users.create", route.BodyJSON)
	add("PATCH", "/admin/users/{id}", "admin.users.update", route.BodyJSON)
	add("DELETE", "/admin/users/{id}", "admin.users.delete", route.BodyNone)
	add("GET", "/admin/users/{id}/oidc", "admin.users.oidc.get", route.BodyNone)
	add("DELETE", "/admin/users/{id}/oidc", "admin.users.oidc.delete", route.BodyNone)
	add("GET", "/admin/groups", "admin.groups.list", route.BodyNone)
	add("POST", "/admin/groups", "admin.groups.create", route.BodyJSON)
	add("PATCH", "/admin/groups/{id}", "admin.groups.update", route.BodyJSON)
	add("DELETE", "/admin/groups/{id}", "admin.groups.delete", route.BodyNone)
	add("POST", "/admin/groups/{id}/members", "admin.groups.members.add", route.BodyJSON)
	add("DELETE", "/admin/groups/{id}/members/{user}", "admin.groups.members.remove", route.BodyNone)
	add("GET", "/admin/grants", "admin.grants.list", route.BodyNone)
	add("POST", "/admin/grants", "admin.grants.create", route.BodyJSON)
	add("PATCH", "/admin/grants/{id}", "admin.grants.update", route.BodyJSON)
	add("DELETE", "/admin/grants/{id}", "admin.grants.delete", route.BodyNone)
	add("GET", "/admin/shares", "admin.shares.list", route.BodyNone)
	add("POST", "/admin/shares", "admin.shares.create", route.BodyJSON)
	add("PATCH", "/admin/shares/{id}", "admin.shares.update", route.BodyJSON)
	add("DELETE", "/admin/shares/{id}", "admin.shares.delete", route.BodyNone)
	add("POST", "/admin/shares/{id}/retry", "admin.shares.retry", route.BodyNone)
	add("GET", "/admin/audit", "admin.audit", route.BodyNone)
	add("GET", "/admin/logs", "admin.logs.list", route.BodyNone)
	add("GET", "/admin/logs/timeline", "admin.logs.timeline", route.BodyNone)
	add("GET", "/admin/storage", "admin.storage", route.BodyNone)
	add("POST", "/admin/smb/apply", "admin.smb.apply", route.BodyNone)
	add("POST", "/admin/index/build", "admin.index.build", route.BodyJSON)
	add("GET", "/admin/index/estimate", "admin.index.estimate", route.BodyNone)
	add("GET", "/admin/settings", "admin.settings.get", route.BodyNone)
	add("PATCH", "/admin/settings/{section}", "admin.settings.patch", route.BodyJSON)
	add("POST", "/admin/system/restart", "admin.system.restart", route.BodyNone)

	// system.
	add("GET", "/system/health", "system.health", route.BodyNone)
	add("GET", "/system/setup", "system.setup.get", route.BodyNone)
	add("POST", "/system/setup", "system.setup.post", route.BodyJSON)
	add("GET", "/events", "events", route.BodyNone)

	return out
}

// requirementFor resolves a route's credential demand: its exception if it has
// one, otherwise its category's default.
func requirementFor(method, path string) route.Requirement {
	if e, ok := exceptions()[method+" "+path]; ok {
		return e.req
	}
	return defaultAccess()[categoryOf(path)]
}

// categoryOf is the first segment after the base, which is what the access
// defaults are keyed on.
func categoryOf(path string) string {
	rest := path
	if len(path) > len(Base) {
		rest = path[len(Base)+1:]
	}
	for i := range len(rest) {
		if rest[i] == '/' {
			return rest[:i]
		}
	}
	return rest
}

// ExceptionReason returns why a route departs from its category default, and
// whether it does at all. The route dump prints it, so an operator reading the
// table sees the justification rather than an unexplained difference.
func ExceptionReason(method, path string) (string, bool) {
	e, ok := exceptions()[method+" "+path]
	return e.why, ok
}
