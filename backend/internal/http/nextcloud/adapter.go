//go:build linux && compat_nc

package nc

import (
	"context"
	"net"
	"net/netip"
	"net/url"
	"strings"

	"github.com/heavycaffeiner/stowcloud/backend/internal/feature/auth"
	core "github.com/heavycaffeiner/stowcloud/backend/internal/feature/files"
	"github.com/heavycaffeiner/stowcloud/backend/internal/feature/shares/acl"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/api/handler"
	num "github.com/heavycaffeiner/stowcloud/backend/internal/platform/number"
	"github.com/heavycaffeiner/stowcloud/backend/internal/storage/vfs"
	"github.com/heavycaffeiner/stowcloud/backend/internal/store/cache"
	"github.com/heavycaffeiner/stowcloud/backend/internal/store/ident"
	"github.com/heavycaffeiner/stowcloud/backend/internal/store/state"
)

// StoreDeps are the durable services needed by the compatibility store.
type StoreDeps struct {
	Core  *core.Core
	State *state.DB
	Cache *cache.DB
}

// NewStore adapts the durable services to the compatibility store contract.
func NewStore(d StoreDeps) Store { return store{d: d} }

type store struct{ d StoreDeps }

func (s store) FileID(ctx context.Context, entry core.Entry) (uint64, error) {
	if recorded, ok, err := s.d.State.LookupFileID(ctx, entry.Ident); err != nil {
		return 0, err
	} else if ok {
		return num.Narrow[uint64](recorded)
	}
	return num.Narrow[uint64](cache.DeriveID(entry.Ident, 0))
}

func (s store) RecordIDs(ctx context.Context, entries []core.Entry) error {
	return s.d.Core.RecordFileIDs(ctx, entries)
}

func (s store) Favorites(ctx context.Context, user core.UserID) (FavoriteSet, error) {
	rows, err := s.d.State.Favorites(ctx, int64(user))
	if err != nil {
		return nil, err
	}
	return favoriteRows(rows), nil
}

func (s store) SetFavorite(ctx context.Context, user core.UserID, entry core.Entry, on bool) error {
	return s.d.State.SetFavorite(ctx, int64(user), state.Favorite{Ident: entry.Ident, Path: entry.Path.String()}, on)
}

func (s store) EncryptedShares(ctx context.Context) (map[core.ShareID]bool, error) {
	ids, err := s.d.Core.EncryptedShares(ctx)
	if err != nil {
		return nil, err
	}
	set := make(map[core.ShareID]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	return set, nil
}

type favoriteRows []state.Favorite

func (f favoriteRows) Has(entry core.Entry) bool {
	for _, row := range f {
		if row.Ident.Equal(entry.Ident) {
			return true
		}
	}
	return false
}

func (f favoriteRows) List() []Favorite {
	out := make([]Favorite, 0, len(f))
	for _, row := range f {
		out = append(out, Favorite{Share: row.Ident.Share, Path: row.Path})
	}
	return out
}

// NewFlow adapts the device-login service to the compatibility flow contract.
func NewFlow(flow *auth.LoginFlow) Flow {
	if flow == nil {
		return nil
	}
	return loginFlow{flow: flow}
}

type loginFlow struct{ flow *auth.LoginFlow }

func (f loginFlow) Begin(ctx context.Context, origin string) (FlowTokens, error) {
	tokens, err := f.flow.Begin(ctx, origin)
	if err != nil {
		return FlowTokens{}, err
	}
	return FlowTokens{PollToken: tokens.PollToken, LoginToken: tokens.LoginToken}, nil
}

func (f loginFlow) Approve(ctx context.Context, loginToken string, user int64, login string) error {
	return f.flow.Approve(ctx, loginToken, user, login)
}

func (f loginFlow) Poll(ctx context.Context, pollToken, origin string) (FlowDelivery, error) {
	out, err := f.flow.Poll(ctx, pollToken, origin)
	if err != nil {
		return FlowDelivery{}, err
	}
	return FlowDelivery{Server: out.Server, LoginName: out.LoginName, AppPassword: out.AppPassword}, nil
}

// Resolve parses a client path and resolves it through the core service.
func Resolve(coreSvc *core.Core, owner core.UserID, path string, need acl.Perms) (core.Resolved, error) {
	vp, err := vfs.ParseVpath(path)
	if err != nil {
		return core.Resolved{}, core.ErrNotFound
	}
	return coreSvc.Resolve(owner, vp, need)
}

// VpathOf renders a stored share and relative path as a client path.
func VpathOf(coreSvc *core.Core, owner core.UserID, share core.ShareID, sharePath string) (string, error) {
	sp, err := vfs.ParseSharePath(sharePath)
	if err != nil {
		return "", core.ErrNotFound
	}
	vp, err := coreSvc.VpathFor(owner, share, sp)
	if err != nil {
		return "", err
	}
	return vp.String(), nil
}

// LocateFile resolves a stable file id through the cache reverse index.
func LocateFile(ctx context.Context, coreSvc *core.Core, cacheDB *cache.DB, user core.UserID, fileID uint64) (string, error) {
	if cacheDB == nil {
		return "", core.ErrNotFound
	}
	id, err := num.Narrow[int64](fileID)
	if err != nil {
		return "", core.ErrNotFound
	}
	share, path, err := cacheDB.Resolve(ctx, ident.FileID(id))
	if err != nil {
		return "", core.ErrNotFound
	}
	vp, err := coreSvc.VpathFor(user, share, path)
	if err != nil {
		return "", core.ErrNotFound
	}
	return vp.String(), nil
}

// OriginConfig supplies the mutable deployment settings used for absolute URLs.
type OriginConfig struct {
	CanonicalURL string
	ContentHosts []string
	Trusted      []netip.Prefix
}

// Origin renders the base URL a request arrived on.
func Origin(cfg func() OriginConfig, r OriginRequest) string {
	c := cfg()
	host, scheme := authority(c.Trusted, r)
	if host == "" {
		return c.CanonicalURL
	}
	return scheme + "://" + host
}

// ContentOrigin renders the base URL used for direct streams.
func ContentOrigin(cfg func() OriginConfig, r OriginRequest) string {
	c := cfg()
	if len(c.ContentHosts) == 0 {
		return Origin(cfg, r)
	}
	host, scheme := authority(c.Trusted, r)
	if host == "" {
		canonical, err := url.Parse(c.CanonicalURL)
		if err != nil || canonical.Host == "" {
			return ""
		}
		host, scheme = canonical.Host, canonical.Scheme
	}
	name := c.ContentHosts[0]
	if _, port, err := net.SplitHostPort(host); err == nil && port != "" {
		name = net.JoinHostPort(strings.Trim(name, "[]"), port)
	}
	return scheme + "://" + name
}

func authority(trusted []netip.Prefix, r OriginRequest) (host, scheme string) {
	host = r.Host
	trustedPeer := peerTrusted(trusted, r.PeerAddr)
	if trustedPeer && r.ForwardedHost != "" && !strings.ContainsAny(r.ForwardedHost, "/\\@") {
		host = r.ForwardedHost
	}
	scheme = "http"
	switch {
	case r.TLS:
		scheme = "https"
	case trustedPeer && strings.EqualFold(r.ForwardedProto, "https"):
		scheme = "https"
	}
	return host, scheme
}

func peerTrusted(prefixes []netip.Prefix, peer string) bool {
	if peer == "" {
		return false
	}
	addr, err := netip.ParseAddr(peer)
	if err != nil {
		return false
	}
	for _, prefix := range prefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// FeaturesFor reports the compatibility capabilities of a deployment.
func FeaturesFor(instanceID string, thumbnails, chunking bool) Features {
	return Features{Version: "31.0.4", InstanceID: instanceID, Thumbnails: thumbnails, Trash: true, Chunking: chunking, Favorites: true, Sharing: true, PublicLinks: true, PublicUpload: true, DefaultSharePerms: SharePermRead}
}

// Claims returns the content claim codec used by direct media URLs.
func Claims(key handler.ClaimKey, now func() int64) (func(core.UserID, string) (string, error), func(string) (core.UserID, string, error)) {
	seal := func(user core.UserID, path string) (string, error) {
		return handler.SealClaim(key, handler.Claim{Purpose: handler.PurposeDownload, UserID: int64(user), Path: path}, now())
	}
	open := func(token string) (core.UserID, string, error) {
		keys := map[uint32][]byte{key.Version: key.Key}
		cl, err := handler.OpenClaim(keys, handler.PurposeDownload, token, now())
		if err != nil {
			return 0, "", err
		}
		return core.UserID(cl.UserID), cl.Path, nil
	}
	return seal, open
}
