package server

import (
	"net/http"
	"strings"

	"github.com/heavycaffeiner/stowcloud/go/internal/acl"
	"github.com/heavycaffeiner/stowcloud/go/internal/httpapi/handler"
	"github.com/heavycaffeiner/stowcloud/go/internal/httpapi/route"
	"github.com/heavycaffeiner/stowcloud/go/internal/httpapi/spa"
)

// routes is the whole table in one place: method, pattern, what the
// credential needs, and the handler. A route added here without a declared
// requirement fails startup validation, which is what makes the scope layer a
// layer rather than something each handler remembers.
//
// The /s/ and /api/setup mounts are public by construction; their
// requirement is AccessAny because the auth step never reaches them.
func routes(d handler.Deps, setup handler.Setup) []route.Route {
	perm := route.Requirement{Access: route.AccessPerms}
	req := func(p acl.Perms) route.Requirement {
		perm.Access = route.AccessPerms
		perm.Perms = p
		return perm
	}
	selfAdmin := route.Requirement{Access: route.AccessSelfAdmin}
	any := route.Requirement{Access: route.AccessAny}

	return []route.Route{
		// The health surface, reachable with no credential: one that needs a
		// credential is one the container runtime cannot use.
		{Method: "GET", Pattern: "/api/health", Req: any, Handler: handler.Health(d.Health)},

		// First-run bootstrap, reachable with no credential.
		{Method: "GET", Pattern: "/api/setup", Req: any, Handler: handler.SetupState(d, setup)},
		{Method: "POST", Pattern: "/api/setup", Req: any, Handler: handler.SetupComplete(d, setup)},

		// Signing in through a provider. All three are reachable with no
		// credential by necessity: the login screen asks whether to draw the
		// button before anyone has signed in, the start is where a person goes
		// to get one, and the callback is a browser coming back from the
		// provider carrying nothing of this server's yet.
		{Method: "GET", Pattern: "/api/auth/oidc/config", Req: any, Handler: handler.OIDCConfig(d)},
		{Method: "GET", Pattern: "/api/auth/oidc/start", Req: any, Handler: handler.OIDCStart(d)},
		{Method: "GET", Pattern: "/api/auth/oidc/callback", Req: any, Handler: handler.OIDCCallback(d)},

		// The resumable upload surface. The discovery request is public
		// because it is how a client learns which version to speak, and it
		// says nothing about the deployment.
		{Method: "OPTIONS", Pattern: "/api/uploads", Req: any, Handler: handler.UploadsOptions()},
		{Method: "OPTIONS", Pattern: "/api/uploads/{id}", Req: any, Handler: handler.UploadsOptions()},
		{Method: "POST", Pattern: "/api/uploads", Req: req(acl.Write | acl.Create), Handler: handler.UploadsCreate(d)},
		{Method: "HEAD", Pattern: "/api/uploads/{id}", Req: req(acl.Write), Handler: handler.UploadsHead(d)},
		{Method: "PATCH", Pattern: "/api/uploads/{id}", Req: req(acl.Write), Handler: handler.UploadsPatch(d)},
		{Method: "DELETE", Pattern: "/api/uploads/{id}", Req: req(acl.Write), Handler: handler.UploadsDelete(d)},

		// Sessions and credentials.
		// Signing in. It was mounted on the change-password path, which is a
		// different endpoint the client also calls, so the shipped interface
		// could not sign in at all while every test here stayed green.
		{Method: "POST", Pattern: "/api/auth/login", Req: any, Handler: handler.Login(d)},
		{Method: "GET", Pattern: "/api/auth/session", Req: selfAdmin, Handler: handler.Session(d)},
		{Method: "POST", Pattern: "/api/auth/logout", Req: any, Handler: handler.Logout(d)},
		{Method: "POST", Pattern: "/api/auth/password", Req: selfAdmin, Handler: handler.ChangePassword(d)},

		// The second factor. Every one of these re-confirms the account
		// password: a live session is what somebody at an unlocked screen
		// already has, and these outlive the session that created them.
		// The code screen, which is the second request of one sign-in. It needs
		// no credential: the challenge it carries is what proves the password
		// step already happened.
		{Method: "POST", Pattern: "/api/auth/login/totp", Req: any, Handler: handler.LoginTOTP(d)},

		{Method: "POST", Pattern: "/api/auth/totp/setup", Req: selfAdmin, Handler: handler.TOTPSetup(d)},
		{Method: "POST", Pattern: "/api/auth/totp/enroll", Req: selfAdmin, Handler: handler.TOTPEnroll(d)},
		{Method: "POST", Pattern: "/api/auth/totp/disable", Req: selfAdmin, Handler: handler.TOTPDisable(d)},
		{Method: "GET", Pattern: "/api/auth/totp/recovery-codes", Req: selfAdmin, Handler: handler.RecoveryCodes(d)},
		{Method: "POST", Pattern: "/api/auth/totp/recovery-codes", Req: selfAdmin, Handler: handler.RecoveryCodes(d)},

		// The file-sharing protocol's own credential and its two toggles.
		// Linking a provider identity to this account. Link-only: the provider
		// authenticates and never creates an account here.
		{Method: "POST", Pattern: "/api/auth/oidc/link/start", Req: selfAdmin, Handler: handler.OIDCLinkStart(d)},
		{Method: "DELETE", Pattern: "/api/auth/oidc/link", Req: selfAdmin, Handler: handler.OIDCUnlink(d)},

		{Method: "POST", Pattern: "/api/auth/smb", Req: selfAdmin, Handler: handler.SMBSettings(d)},
		{Method: "POST", Pattern: "/api/auth/smb/password", Req: selfAdmin, Handler: handler.SMBPassword(d)},
		{Method: "DELETE", Pattern: "/api/auth/smb/password", Req: selfAdmin, Handler: handler.SMBPassword(d)},

		{Method: "GET", Pattern: "/api/auth/app-passwords", Req: selfAdmin, Handler: handler.AppPasswords(d)},
		{Method: "POST", Pattern: "/api/auth/app-passwords", Req: selfAdmin, Handler: handler.AppPasswords(d)},
		{Method: "DELETE", Pattern: "/api/auth/app-passwords/{id}", Req: selfAdmin, Handler: handler.AppPasswordDelete(d)},
		{Method: "POST", Pattern: "/api/auth/app-passwords/{id}/wipe", Req: selfAdmin, Handler: handler.AppPasswordWipe(d)},
		{Method: "GET", Pattern: "/api/auth/sessions", Req: selfAdmin, Handler: handler.Sessions(d)},
		{Method: "DELETE", Pattern: "/api/auth/sessions/{id}", Req: selfAdmin, Handler: handler.Sessions(d)},

		// The filesystem surface.
		{Method: "GET", Pattern: "/api/fs/list", Req: req(acl.Read), Handler: handler.List(d)},
		{Method: "GET", Pattern: "/api/fs/stat", Req: req(acl.Read), Handler: handler.Stat(d)},
		{Method: "GET", Pattern: "/api/fs/read", Req: req(acl.Read | acl.Download), Handler: handler.Read(d)},
		{Method: "POST", Pattern: "/api/fs/mkdir", Req: req(acl.Create), Handler: handler.Mkdir(d)},
		{Method: "POST", Pattern: "/api/fs/rename", Req: req(acl.Rename), Handler: handler.Rename(d)},
		{Method: "POST", Pattern: "/api/fs/move", Req: req(acl.Move), Handler: handler.Move(d)},
		{Method: "POST", Pattern: "/api/fs/copy", Req: req(acl.Read | acl.Create), Handler: handler.Copy(d)},
		{Method: "POST", Pattern: "/api/fs/delete", Req: req(acl.Delete), Handler: handler.Delete(d)},
		// Both spellings. The shipped client sends the second, and mounting
		// only the first made writing a file answer that the method is not
		// allowed from a route that exists.
		{Method: "POST", Pattern: "/api/fs/write", Req: req(acl.Write), Handler: handler.Write(d)},
		{Method: "PUT", Pattern: "/api/fs/write", Req: req(acl.Write), Handler: handler.Write(d)},
		// Thumbnails, by path like every other read. The interface used to mint
		// a signed link per thumbnail, which needed a fileid a listing does not
		// carry, so the request could never be made.
		{Method: "GET", Pattern: "/api/fs/thumb", Req: req(acl.Read | acl.Download), Handler: handler.Thumb(d)},
		{Method: "GET", Pattern: "/api/fs/size", Req: req(acl.Read), Handler: handler.Size(d)},
		{Method: "GET", Pattern: "/api/fs/link", Req: req(acl.Read), Handler: handler.Links(d)},
		{Method: "POST", Pattern: "/api/fs/link", Req: req(acl.Read), Handler: handler.Links(d)},
		{Method: "DELETE", Pattern: "/api/fs/link/{id}", Req: req(acl.Read), Handler: handler.LinkDelete(d)},

		// The same links under the name the client uses for them. The
		// capability is the share right rather than read: minting a link hands
		// a file to whoever holds the URL.
		{Method: "GET", Pattern: "/api/shares", Req: req(acl.Share), Handler: handler.Links(d)},
		{Method: "POST", Pattern: "/api/shares", Req: req(acl.Share), Handler: handler.Links(d)},
		{Method: "PATCH", Pattern: "/api/shares/{id}", Req: req(acl.Share), Handler: handler.LinkUpdate(d)},
		{Method: "DELETE", Pattern: "/api/shares/{id}", Req: req(acl.Share), Handler: handler.LinkDelete(d)},

		// Listing the jobs this account has in flight.
		{Method: "GET", Pattern: "/api/jobs", Req: any, Handler: handler.Operations(d)},
		{Method: "GET", Pattern: "/api/fs/archive/list", Req: req(acl.Read), Handler: handler.ArchiveList(d)},
		{Method: "POST", Pattern: "/api/fs/archive", Req: req(acl.Read | acl.Create), Handler: handler.ArchiveCreate(d)},

		// Trash.
		{Method: "GET", Pattern: "/api/trash", Req: req(acl.Read), Handler: handler.Trash(d)},
		{Method: "POST", Pattern: "/api/trash/restore", Req: req(acl.Create), Handler: handler.TrashRestore(d)},
		{Method: "POST", Pattern: "/api/trash/purge", Req: req(acl.Delete), Handler: handler.TrashPurge(d)},

		// Operations.
		{Method: "GET", Pattern: "/api/jobs/{id}", Req: any, Handler: handler.Operation(d)},
		{Method: "POST", Pattern: "/api/jobs/{id}/cancel", Req: any, Handler: handler.OperationCancel(d)},
		// The spelling the shipped client uses. It was not mounted, so
		// cancelling a job answered that the method is not allowed.
		{Method: "DELETE", Pattern: "/api/jobs/{id}", Req: any, Handler: handler.OperationCancel(d)},
		{Method: "GET", Pattern: "/api/jobs/{id}/download", Req: any, Handler: handler.OperationDownload(d)},

		// The change channel.
		{Method: "GET", Pattern: "/api/events", Req: any, Handler: handler.Events(d)},

		// Searching, streamed as the hits are found: a walk over a large
		// share takes long enough that a screen showing nothing until it ends
		// looks broken.
		{Method: "GET", Pattern: "/api/search/stream", Req: req(acl.Read), Handler: handler.SearchStream(d)},

		// Recency: the route and the shape are owned here; the backing index
		// arrives with the search phase.
		{Method: "GET", Pattern: "/api/recent", Req: req(acl.Read), Handler: handler.Recent(d)},

		// Admin.
		// Accounts and groups. Sessions only, never an app password: an app
		// password is a filesystem capability handed to a device, and a device
		// that can create an administrator can grant itself anything.
		{Method: "GET", Pattern: "/api/admin/users", Req: selfAdmin, Handler: handler.AdminUsers(d)},
		{Method: "POST", Pattern: "/api/admin/users", Req: selfAdmin, Handler: handler.AdminUsers(d)},
		{Method: "PATCH", Pattern: "/api/admin/users/{id}", Req: selfAdmin, Handler: handler.AdminUser(d)},
		{Method: "DELETE", Pattern: "/api/admin/users/{id}", Req: selfAdmin, Handler: handler.AdminUser(d)},
		{Method: "GET", Pattern: "/api/admin/users/{id}/oidc", Req: selfAdmin, Handler: handler.AdminUserOIDC(d)},
		{Method: "PUT", Pattern: "/api/admin/users/{id}/oidc", Req: selfAdmin, Handler: handler.AdminUserOIDC(d)},
		{Method: "DELETE", Pattern: "/api/admin/users/{id}/oidc", Req: selfAdmin, Handler: handler.AdminUserOIDC(d)},
		{Method: "POST", Pattern: "/api/admin/smb/apply", Req: selfAdmin, Handler: handler.SMBApply(d)},
		{Method: "GET", Pattern: "/api/admin/groups", Req: selfAdmin, Handler: handler.AdminGroups(d)},
		{Method: "POST", Pattern: "/api/admin/groups", Req: selfAdmin, Handler: handler.AdminGroups(d)},
		{Method: "PATCH", Pattern: "/api/admin/groups/{id}", Req: selfAdmin, Handler: handler.AdminGroup(d)},
		{Method: "DELETE", Pattern: "/api/admin/groups/{id}", Req: selfAdmin, Handler: handler.AdminGroup(d)},
		{Method: "POST", Pattern: "/api/admin/groups/{gid}/members", Req: selfAdmin, Handler: handler.AdminGroupMembers(d)},
		{Method: "DELETE", Pattern: "/api/admin/groups/{gid}/members/{user}", Req: selfAdmin, Handler: handler.AdminGroupMembers(d)},

		// Grants. Every write reloads the evaluator before answering, so a
		// grant is never live in the database and stale in this process.
		{Method: "GET", Pattern: "/api/admin/grants", Req: selfAdmin, Handler: handler.AdminGrants(d)},
		{Method: "POST", Pattern: "/api/admin/grants", Req: selfAdmin, Handler: handler.AdminGrants(d)},
		{Method: "PATCH", Pattern: "/api/admin/grants/{id}", Req: selfAdmin, Handler: handler.AdminGrant(d)},
		{Method: "DELETE", Pattern: "/api/admin/grants/{id}", Req: selfAdmin, Handler: handler.AdminGrant(d)},

		// Operational reporting and the settings sections.
		{Method: "GET", Pattern: "/api/admin/storage", Req: selfAdmin, Handler: handler.AdminStorage(d)},
		{Method: "GET", Pattern: "/api/admin/audit", Req: selfAdmin, Handler: handler.AdminAudit(d)},
		{Method: "GET", Pattern: "/api/admin/index/estimate", Req: selfAdmin, Handler: handler.AdminIndexEstimate(d)},
		{Method: "POST", Pattern: "/api/admin/index/build", Req: selfAdmin, Handler: handler.AdminIndexBuild(d)},
		{Method: "GET", Pattern: "/api/admin/index/settings", Req: selfAdmin, Handler: handler.AdminIndexSettings(d)},
		{Method: "PATCH", Pattern: "/api/admin/index/settings", Req: selfAdmin, Handler: handler.AdminIndexSettings(d)},
		{Method: "PATCH", Pattern: "/api/admin/upload-settings", Req: selfAdmin, Handler: handler.AdminUploadSettings(d)},

		// The settings sections. One handler for all of them: they differ only
		// in which key they write, and a section nobody recognises is refused
		// rather than created.
		{Method: "PATCH", Pattern: "/api/admin/server-settings/{section}", Req: selfAdmin, Handler: handler.AdminServerSettingsSection(d)},
		{Method: "POST", Pattern: "/api/admin/server-settings/restart", Req: selfAdmin, Handler: handler.AdminServerSettingsRestart(d)},
		// The dry run. Same probes as the save, storing nothing, so an
		// administrator can see what a change would do before it does it.
		{Method: "POST", Pattern: "/api/admin/server-settings/{section}/check", Req: selfAdmin, Handler: handler.AdminServerSettingsCheck(d)},

		{Method: "GET", Pattern: "/api/admin/shares", Req: selfAdmin, Handler: handler.Shares(d)},
		{Method: "POST", Pattern: "/api/admin/shares", Req: selfAdmin, Handler: handler.Shares(d)},
		{Method: "PATCH", Pattern: "/api/admin/shares/{id}", Req: selfAdmin, Handler: handler.ShareUpdate(d)},
		{Method: "DELETE", Pattern: "/api/admin/shares/{id}", Req: selfAdmin, Handler: handler.ShareDelete(d)},
		{Method: "GET", Pattern: "/api/admin/server-settings", Req: selfAdmin, Handler: handler.Settings(d)},
		{Method: "PATCH", Pattern: "/api/admin/server-settings/network", Req: selfAdmin, Handler: handler.SettingsNetwork(d)},

		// Public share links. The token authenticates the request; no session
		// is involved, which is why they are public in the auth step.
		{Method: "GET", Pattern: "/s/{token}", Req: any, Handler: handler.LinkPublic(d)},
		{Method: "GET", Pattern: "/s/{token}/download", Req: any, Handler: handler.LinkDownload(d)},
	}
}

// mux builds the ServeMux from the table. Go 1.22's method and wildcard
// patterns cover every route on this surface, which was the confirmation
// step 5a's first task; no router module is needed.
func mux(table []route.Route, compat []compatMount, davs http.Handler) *http.ServeMux {
	m := http.NewServeMux()
	for _, rt := range table {
		m.Handle(rt.Method+" "+rt.Pattern, rt.Handler)
	}
	// The compatibility mounts, which are empty in a build without the tag.
	// Called unconditionally so the tag stays one file's concern.
	for _, cm := range compat {
		m.Handle(cm.Method+" "+cm.Pattern, cm.Handler)
	}

	// The frontend goes on the bare root, which every API route out-specifies,
	// so it catches only what the table did not claim. A build carrying no
	// bundle leaves the pattern unmounted rather than serving a 404 page that
	// looks like a broken frontend.
	//
	// The WebDAV mount is folded into the same registration rather than
	// registered beside it. One pattern is a single method on the bare root
	// and the other is every method on a prefix, and the router refuses that
	// pair in either order: neither is more specific than the other, so it
	// panics at startup rather than picking. Dispatching on the prefix here is
	// what leaves one pattern to register.
	root := davs
	if h, ok := spa.Handler(); ok {
		root = rootHandler(h, davs)
	}
	if root != nil {
		m.Handle("/", root)
	}
	return m
}

// rootHandler sends the WebDAV prefix to the protocol and everything else to
// the frontend.
//
// A request under the prefix that is not WebDAV does not fall through to the
// frontend: the prefix belongs to the protocol, and handing a WebDAV client an
// HTML page is how a sync client reports a corrupt server rather than a wrong
// path.
func rootHandler(spaHandler, davs http.Handler) http.Handler {
	if davs == nil {
		return spaHandler
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if p == davPrefix || strings.HasPrefix(p, davPrefix+"/") {
			davs.ServeHTTP(w, r)
			return
		}
		// The frontend answers reads only. Anything else on a path it owns is
		// a method it has no answer for, and saying so beats returning a
		// document with a success status.
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		spaHandler.ServeHTTP(w, r)
	})
}
