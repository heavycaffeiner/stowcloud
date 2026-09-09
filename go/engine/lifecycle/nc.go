//go:build linux && compat_nc

// Binding the other product's surface to this engine's services.
//
// Everything the surface cannot reach for itself is assembled here: the
// store-backed facts, the device-login flow, the base URL of a request, and
// the two capabilities that carry a signed claim. The surface owns every
// spelling on the wire and this file owns none of them, which is what keeps
// the vocabulary in one package.
package lifecycle

import (
	"context"
	"net/netip"
	"strings"

	"github.com/gofiber/fiber/v2"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/handler"
	"github.com/heavycaffeiner/stowcloud/go/engine/http/nc"
	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/cache"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/ident"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/state"
)

// ncVersion is the version the surface reports.
//
// It is not this build's version: it is the version whose feature set the
// clients gate on. Every capability the document advertises has a minimum
// version in at least one client, and a number below it turns the feature off
// in that client whatever the document says.
const ncVersion = "31.0.4"

// mountNCTagged claims the compatibility paths.
func (e *Engine) mountNCTagged(app *fiber.App) {
	e.ncServer().Mount(app)

	// The public link surface again, under the front-controller prefix. A
	// client builds that spelling from a token when a share record carries no
	// URL, and a person who was sent one has to be able to open it: without
	// these the address fell through to the interface and opened a document
	// instead of the file it names.
	app.Get(frontController+PublicLinkPrefix+"/:token", e.linkLanding)
	app.Post(frontController+PublicLinkPrefix+"/:token/auth", e.linkUnlock)
	app.Get(frontController+PublicLinkPrefix+"/:token/download", e.linkDownload)
	app.Get(frontController+PublicLinkPrefix+"/:token/zip", e.linkZip)
	app.Post(frontController+PublicLinkPrefix+"/:token/drop", e.linkDrop)
}

// frontController is the prefix the other product's clients prepend when they
// build a URL the long way.
const frontController = "/index.php"

// ncServer assembles the surface.
func (e *Engine) ncServer() *nc.Server {
	return nc.New(nc.Deps{
		Core:    e.Core,
		Auth:    e.Auth,
		Store:   ncStore{e: e},
		Uploads: e.Upload,
		Preview: e.Preview,
		Search:  e.Search,
		Flow:    e.ncFlow(),

		Features:       e.ncFeatures,
		Origin:         e.ncOrigin,
		ConsentPage:    e.ncLoginConsent,
		Resolve:        e.ncResolve,
		VpathOf:        e.ncVpathOf,
		LocateFile:     e.ncLocateFile,
		SealClaim:      e.sealContentClaim,
		OpenClaim:      e.openContentClaim,
		PublicLinkPath: func(token string) string { return PublicLinkPrefix + "/" + token },
		LockGuard:      e.ncLockGuard,

		Clock:  e.clk(),
		Logger: e.log(),
	})
}

// ncFeatures reports what this deployment can actually answer.
//
// Read per request rather than captured once, because an operator can turn
// thumbnails off while a client is connected and the document the client
// re-reads has to say so. The instance identity is the exception: it is
// minted once and never changes, so it is read at open and copied here
// rather than costing a query per capabilities call.
func (e *Engine) ncFeatures() nc.Features {
	return nc.Features{
		Version:    ncVersion,
		InstanceID: e.instanceID,

		Thumbnails: e.thumbnailEnabled() && e.Preview != nil,
		Search:     e.Search != nil,
		Trash:      true,
		Chunking:   e.Upload != nil,
		Favorites:  true,

		Sharing:      true,
		PublicLinks:  true,
		PublicUpload: true,

		DefaultSharePerms: nc.SharePermRead,
	}
}

// ncResolve turns a client path into a capability.
//
// Parsing the path is this tier's work: the presentation tier is handed
// resolutions rather than filesystem types, which is the same seam the native
// protocol mount is built on. A path that will not parse answers as absent,
// because a malformed one names nothing.
func (e *Engine) ncResolve(
	owner core.UserID, path string, need acl.Perms,
) (core.Resolved, error) {
	vp, err := vfs.ParseVpath(path)
	if err != nil {
		return core.Resolved{}, core.ErrNotFound
	}
	return e.Core.Resolve(owner, vp, need)
}

// ncVpathOf crosses a stored share-and-path pair back into the path a client
// addresses.
func (e *Engine) ncVpathOf(
	owner core.UserID, share core.ShareID, sharePath string,
) (string, error) {
	sp, err := vfs.ParseSharePath(sharePath)
	if err != nil {
		return "", core.ErrNotFound
	}
	vp, err := e.Core.VpathFor(owner, share, sp)
	if err != nil {
		return "", err
	}
	return vp.String(), nil
}

// ncOrigin renders the base URL a request arrived on.
//
// A forwarded name is honoured only from a trusted peer, because the header
// is a client's claim about who it reached and this value ends up in a URL a
// client will send a credential to. An absent host answers the empty string,
// which leaves the caller to refuse rather than to invent a name.
func (e *Engine) ncOrigin(r nc.OriginRequest) string {
	host := r.Host
	trusted := e.ncPeerTrusted(r.PeerAddr)
	if trusted && r.ForwardedHost != "" && !strings.ContainsAny(r.ForwardedHost, "/\\@") {
		host = r.ForwardedHost
	}
	if host == "" {
		return ""
	}

	scheme := "http"
	switch {
	case r.TLS:
		scheme = "https"
	case trusted && strings.EqualFold(r.ForwardedProto, "https"):
		scheme = "https"
	}
	return scheme + "://" + host
}

// ncPeerTrusted reports whether a peer address is one whose forwarding
// headers this deployment believes.
func (e *Engine) ncPeerTrusted(peer string) bool {
	if peer == "" {
		return false
	}
	addr, err := netip.ParseAddr(peer)
	if err != nil {
		return false
	}
	for _, prefix := range e.trustedPrefixes() {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// ncLocateFile maps a stable file id back to the path a caller addresses.
//
// The cache's reverse index answers it, so a lookup is one query rather than a
// walk: a client opening a notification, a preview or a direct link names a
// file by id alone, and a tree walk per thumbnail is a listing screen that
// never finishes.
func (e *Engine) ncLocateFile(ctx context.Context, user core.UserID, fileID uint64) (string, error) {
	if e.Cache == nil {
		return "", core.ErrNotFound
	}
	id, err := num.Narrow[int64](fileID)
	if err != nil {
		return "", core.ErrNotFound
	}
	share, path, err := e.Cache.Resolve(ctx, ident.FileID(id))
	if err != nil {
		return "", core.ErrNotFound
	}
	vp, err := e.Core.VpathFor(user, share, path)
	if err != nil {
		return "", core.ErrNotFound
	}
	return vp.String(), nil
}

// sealContentClaim mints the capability a direct media URL carries.
func (e *Engine) sealContentClaim(user core.UserID, path string) (string, error) {
	return handler.SealClaim(e.claimKey, handler.Claim{
		Purpose: handler.PurposeDownload,
		UserID:  int64(user),
		Path:    path,
	}, e.clk().Nanos())
}

// openContentClaim reads one back.
func (e *Engine) openContentClaim(token string) (core.UserID, string, error) {
	keys := map[uint32][]byte{e.claimKey.Version: e.claimKey.Key}
	cl, err := handler.OpenClaim(keys, handler.PurposeDownload, token, e.clk().Nanos())
	if err != nil {
		return 0, "", err
	}
	return core.UserID(cl.UserID), cl.Path, nil
}

// ncLockGuard refuses a write that a lock held through another protocol
// covers.
func (e *Engine) ncLockGuard(ctx context.Context, res core.Resolved, principal int64) error {
	return e.guardDavLock(ctx, uint32(res.Share()), res.Path().String(), principal)
}

// ncFlow adapts the device-login service to what the surface asks of it.
//
// Nil when the service is absent, which the surface answers as a feature this
// deployment does not have rather than as a failure.
func (e *Engine) ncFlow() nc.Flow {
	if e.Flow == nil {
		return nil
	}
	return ncFlow{flow: e.Flow}
}

type ncFlow struct{ flow *LoginFlow }

// Begin mints the tokens and lets the caller spell the URLs.
//
// The login URL and the poll endpoint are the other product's vocabulary, so
// the surface builds both from the token; this returns the token and the
// origin the flow is bound to and nothing else.
func (f ncFlow) Begin(ctx context.Context, origin string) (nc.FlowTokens, error) {
	tokens, err := f.flow.Begin(ctx, origin)
	if err != nil {
		return nc.FlowTokens{}, err
	}
	return nc.FlowTokens{PollToken: tokens.PollToken, LoginToken: tokens.LoginToken}, nil
}

func (f ncFlow) Approve(ctx context.Context, loginToken string, user int64, login string) error {
	return f.flow.Approve(ctx, loginToken, user, login)
}

func (f ncFlow) Poll(ctx context.Context, pollToken, origin string) (nc.FlowDelivery, error) {
	out, err := f.flow.Poll(ctx, pollToken, origin)
	if err != nil {
		return nc.FlowDelivery{}, err
	}
	return nc.FlowDelivery{
		Server:      out.Server,
		LoginName:   out.LoginName,
		AppPassword: out.AppPassword,
	}, nil
}

// ncStore answers the store-backed questions the surface cannot ask itself.
type ncStore struct{ e *Engine }

// FileID is the stable identity a client's sync journal is keyed on.
//
// A recorded override wins, because a past collision decision is never
// revisited. Otherwise the id is the pure derivation, which answers the same
// value for a first candidate that found no collision. Nothing here
// allocates: a read that minted an id would hand a client an identity for a
// file that may be gone by the time it acts, and would write during a
// listing.
func (s ncStore) FileID(ctx context.Context, entry core.Entry) (uint64, error) {
	if recorded, ok, err := s.e.State.LookupFileID(ctx, entry.Ident); err != nil {
		return 0, err
	} else if ok {
		return num.Narrow[uint64](recorded)
	}
	return num.Narrow[uint64](cache.DeriveID(entry.Ident, 0))
}

// Favorites reads the caller's starred set once for a whole request.
func (s ncStore) Favorites(ctx context.Context, user core.UserID) (nc.FavoriteSet, error) {
	rows, err := s.e.State.Favorites(ctx, int64(user))
	if err != nil {
		return nil, err
	}
	return favoriteRows(rows), nil
}

func (s ncStore) SetFavorite(
	ctx context.Context, user core.UserID, entry core.Entry, on bool,
) error {
	return s.e.State.SetFavorite(ctx, int64(user), state.Favorite{
		Ident: entry.Ident,
		Path:  entry.Path.String(),
	}, on)
}

func (s ncStore) EncryptedShares(ctx context.Context) (map[core.ShareID]bool, error) {
	ids, err := s.e.Core.EncryptedShares(ctx)
	if err != nil {
		return nil, err
	}
	set := make(map[core.ShareID]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	return set, nil
}

// favoriteRows is one account's starred set as the store holds it.
type favoriteRows []state.Favorite

// Has compares identities rather than paths, so a starred file keeps its star
// through a rename and loses it when the file is replaced by a new one at the
// same path.
func (f favoriteRows) Has(entry core.Entry) bool {
	for _, row := range f {
		if row.Ident.Equal(entry.Ident) {
			return true
		}
	}
	return false
}

func (f favoriteRows) List() []nc.Favorite {
	out := make([]nc.Favorite, 0, len(f))
	for _, row := range f {
		out = append(out, nc.Favorite{Share: row.Ident.Share, Path: row.Path})
	}
	return out
}
