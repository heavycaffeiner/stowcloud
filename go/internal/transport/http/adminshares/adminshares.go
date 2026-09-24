//go:build linux

// Shares and grants, as an administrator sees them.
package adminshares

// Host paths and credentials are never rendered by the projections below.

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/heavycaffeiner/stowcloud/go/internal/feature/auth"
	core "github.com/heavycaffeiner/stowcloud/go/internal/feature/files"
	"github.com/heavycaffeiner/stowcloud/go/internal/feature/shares/acl"
	num "github.com/heavycaffeiner/stowcloud/go/internal/platform/number"
	secret "github.com/heavycaffeiner/stowcloud/go/internal/platform/security/secret"
	"github.com/heavycaffeiner/stowcloud/go/internal/platform/storage/objstore"
	"github.com/heavycaffeiner/stowcloud/go/internal/platform/storage/vault"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/apierr"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/handler"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/middleware"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/route"
)

// Deps supplies the narrow product services used by administrator share and
// grant routes. Runtime hooks are post-commit side effects of registration.
type Deps struct {
	Core                 *core.Core
	Auth                 *auth.Service
	MarkSearchIncomplete func()
	WatchShare           func(core.ShareDef)
	UnwatchShare         func(core.ShareDef)
	Logger               *slog.Logger
}

// NewHandlers builds administrator share and grant routes.
func NewHandlers(d Deps) map[string]gin.HandlerFunc {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	h := &handlers{d: d}
	return map[string]gin.HandlerFunc{
		"admin.shares.list": h.sharesList, "admin.shares.create": h.sharesCreate,
		"admin.shares.update": h.sharesUpdate, "admin.shares.retry": h.sharesRetry,
		"admin.shares.delete": h.sharesDelete, "admin.grants.list": h.grantsList,
		"admin.grants.create": h.grantsCreate, "admin.grants.update": h.grantsUpdate,
		"admin.grants.delete": h.grantsDelete,
	}
}

type handlers struct{ d Deps }

func (h *handlers) admin(c *gin.Context) (int64, bool) {
	v, ok := c.Get(string(middleware.KeyCredential))
	if !ok {
		refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return 0, false
	}
	p, ok := v.(middleware.Principal)
	if !ok || p.UserID == 0 {
		refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return 0, false
	}
	is, err := h.d.Auth.IsAdmin(c.Request.Context(), p.UserID)
	if err != nil {
		fail(c, err)
		return 0, false
	}
	if !is {
		refuse(c, apierr.Classified{Class: apierr.Denied})
		return 0, false
	}
	return p.UserID, true
}

func decode(c *gin.Context, v any) bool {
	if err := middleware.DecodeJSON(middleware.LimitBody(c.Request.Body, route.BodyJSON), v); err != nil {
		refuse(c, apierr.Classified{Class: apierr.Malformed})
		return false
	}
	return true
}
func json(c *gin.Context, status int, v any) { c.JSON(status, v) }
func notFound(c *gin.Context)                { fail(c, core.ErrNotFound) }
func refuse(c *gin.Context, class apierr.Classified) {
	status, body := apierr.REST(class)
	json(c, status, body)
}
func fail(c *gin.Context, err error) {
	if errors.Is(err, core.ErrNotFound) {
		refuse(c, apierr.Classified{Class: apierr.NotFound})
		return
	}
	middleware.SetCause(c, err)
	refuse(c, apierr.Classify(err, apierr.VisibilityKnown))
}
func pathID(c *gin.Context) (int64, bool) {
	n, err := strconv.ParseInt(c.Param("id"), 10, 64)
	return n, err == nil && n > 0
}
func queryInt(raw string) int64 {
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// adminSharesList answers every registered share.
//
// Including the broken ones. A share whose disk never came back is still
// registered, and dropping it from this listing is what once made an
// unreachable share indistinguishable from a deleted one.
func (h *handlers) sharesList(c *gin.Context) {
	if _, ok := h.admin(c); !ok {
		return
	}
	ctx := c.Request.Context()
	empty := func(id core.ShareID) bool { return h.d.Core.ShareEmpty(ctx, id) }
	json(c, http.StatusOK, handler.SharesOf(h.d.Core.Shares(), empty))
}

// createShareRequest registers a share, of whichever backend Backend names.
type createShareRequest struct {
	Name string `json:"name"`

	// Host is where it lives on the server's disk. It arrives from an
	// administrator and never goes back out. Meaningful only when Backend
	// is BackendLocal, which is also what an absent Backend means.
	Host string `json:"host"`

	// Backend selects which package opens the share's storage. Absent or
	// "" reads as local, so the first-run wizard and every client that
	// predates backends keep working unchanged.
	Backend string `json:"backend"`

	// S3 configures an s3 backend. Refused unless Backend is "s3".
	S3 *shareS3Request `json:"s3"`

	// Veracrypt configures a veracrypt backend. Refused unless Backend is
	// "veracrypt".
	Veracrypt *shareVeracryptRequest `json:"veracrypt"`
}

// shareS3Request is the "s3" object of a share create or patch request.
// Every field is a pointer, which on a patch is what separates leaving a
// field alone from setting it: an absent secret_access_key leaves the
// stored credential alone, and a present empty one is refused rather than
// treated as clearing it, since a share with no credential cannot serve.
type shareS3Request struct {
	Endpoint        *string `json:"endpoint"`
	Region          *string `json:"region"`
	Bucket          *string `json:"bucket"`
	Prefix          *string `json:"prefix"`
	AccessKeyID     *string `json:"access_key_id"`
	SecretAccessKey *string `json:"secret_access_key"`
	PathStyle       *bool   `json:"path_style"`
}

// shareVeracryptRequest is the "veracrypt" object. Create and SizeMiB are
// read only on creation, when a fresh container may be asked for; a patch
// naming either is refused, since that path runs once, at creation.
type shareVeracryptRequest struct {
	Container *string `json:"container"`
	Password  *string `json:"password"`
	Create    *bool   `json:"create"`
	SizeMiB   *uint64 `json:"size_mib"`
	// PIM is VeraCrypt's Personal Iterations Multiplier. Optional on both
	// create and patch: absent means the container carries none, and
	// vault.ParseConfig is what actually bounds it.
	PIM *uint32 `json:"pim"`
}

// adminSharesCreate registers one.
func (h *handlers) sharesCreate(c *gin.Context) {
	admin, ok := h.admin(c)
	if !ok {
		return
	}
	var req createShareRequest
	if !decode(c, &req) {
		return
	}
	spec, verr := shareSpecOf(req)
	if verr != nil {
		fail(c, verr)
		return
	}
	share, err := h.d.Core.CreateShare(c.Request.Context(), spec)
	if err != nil {
		if errors.Is(err, core.ErrUnprocessable) {
			refuse(c, apierr.Classified{Class: apierr.Unprocessable, Key: "admin.share_backend_unknown"})
			return
		}
		fail(c, err)
		return
	}
	if h.d.MarkSearchIncomplete != nil {
		h.d.MarkSearchIncomplete()
	}
	if h.d.WatchShare != nil {
		h.d.WatchShare(share)
	}
	if err := h.grantShareTo(c, admin, share); err != nil {
		h.d.Logger.Warn("the new share was registered without a grant for its creator", "share", int64(share.ID), "error", err)
	}
	json(c, http.StatusCreated, handler.ShareOf(share))
}

// unprocessable names a refusal about the request's own content, carrying
// the catalogue key the screen renders. A bare class would answer 422 with
// nothing saying which field of a seven-field form was wrong.
func unprocessable(key string) error {
	return apierr.AsClassified(apierr.Unprocessable, key)
}

// shareSpecOf validates a create request into a spec: the trust boundary
// for which backend fields may even be present. An s3 object naming a
// local share, or a veracrypt object with no password, refuses the whole
// request rather than storing a share that cannot serve.
func shareSpecOf(req createShareRequest) (core.ShareSpec, error) {
	backend, berr := core.ParseBackend(req.Backend)
	if berr != nil {
		return core.ShareSpec{}, unprocessable("admin.share_backend_unknown")
	}
	spec := core.ShareSpec{Name: req.Name, Backend: backend}
	switch backend {
	case core.BackendLocal:
		if req.S3 != nil || req.Veracrypt != nil {
			return core.ShareSpec{}, unprocessable("admin.share_backend_extra_config")
		}
		if req.Host == "" {
			return core.ShareSpec{}, unprocessable("admin.share_host_required")
		}
		spec.Host = req.Host
	case core.BackendS3:
		if req.Veracrypt != nil || req.Host != "" {
			return core.ShareSpec{}, unprocessable("admin.share_backend_extra_config")
		}
		if req.S3 == nil {
			return core.ShareSpec{}, unprocessable("admin.share_backend_config_missing")
		}
		cfg, plain, cerr := s3ConfigForCreate(req.S3)
		if cerr != nil {
			return core.ShareSpec{}, cerr
		}
		configBytes, secretVal, merr := marshalAndSealS3(cfg, plain)
		if merr != nil {
			return core.ShareSpec{}, merr
		}
		spec.Config, spec.Secret = configBytes, secretVal
	case core.BackendVeracrypt:
		if req.S3 != nil || req.Host != "" {
			return core.ShareSpec{}, unprocessable("admin.share_backend_extra_config")
		}
		if req.Veracrypt == nil {
			return core.ShareSpec{}, unprocessable("admin.share_backend_config_missing")
		}
		cfg, plain, cerr := vaultConfigForCreate(req.Veracrypt)
		if cerr != nil {
			return core.ShareSpec{}, cerr
		}
		configBytes, secretVal, merr := marshalAndSealVault(cfg, plain)
		if merr != nil {
			return core.ShareSpec{}, merr
		}
		spec.Config, spec.Secret = configBytes, secretVal
	}
	return spec, nil
}

// s3ConfigForCreate builds and requires every field an s3 backend needs
// to be created with. Region is required too: objstore.ParseConfig refuses
// an empty one, so leaving it optional here would only move the refusal
// one line down with a worse message.
func s3ConfigForCreate(req *shareS3Request) (objstore.Config, string, error) {
	if req.Endpoint == nil || req.Region == nil || req.Bucket == nil || req.AccessKeyID == nil {
		return objstore.Config{}, "", unprocessable("admin.share_s3_fields_required")
	}
	if req.SecretAccessKey == nil || *req.SecretAccessKey == "" {
		return objstore.Config{}, "", unprocessable("admin.share_s3_secret_required")
	}
	cfg := objstore.Config{
		Endpoint:  *req.Endpoint,
		Region:    *req.Region,
		Bucket:    *req.Bucket,
		AccessKey: *req.AccessKeyID,
	}
	if req.Prefix != nil {
		cfg.Prefix = *req.Prefix
	}
	if req.PathStyle != nil {
		cfg.PathStyle = *req.PathStyle
	}
	return cfg, *req.SecretAccessKey, nil
}

// vaultConfigForCreate builds and requires every field a veracrypt backend
// needs to be created with. A size is required exactly when create is
// true and refused otherwise, since it is meaningless for a container that
// must already exist.
func vaultConfigForCreate(req *shareVeracryptRequest) (vault.Config, string, error) {
	if req.Container == nil || *req.Container == "" {
		return vault.Config{}, "", unprocessable("admin.share_vault_container_required")
	}
	if req.Password == nil || *req.Password == "" {
		return vault.Config{}, "", unprocessable("admin.share_vault_password_required")
	}
	create := req.Create != nil && *req.Create
	hasSize := req.SizeMiB != nil && *req.SizeMiB > 0
	cfg := vault.Config{Container: *req.Container}
	if req.PIM != nil {
		cfg.PIM = *req.PIM
	}
	switch {
	case create && !hasSize:
		return vault.Config{}, "", unprocessable("admin.share_vault_size_required")
	case !create && hasSize:
		return vault.Config{}, "", unprocessable("admin.share_vault_size_unexpected")
	case create:
		cfg.CreateSizeMiB = *req.SizeMiB
	}
	return cfg, *req.Password, nil
}

// marshalAndSealS3 renders cfg through its own Marshal and validates the
// result by parsing it back, so the stored shape is exactly what
// objstore.ParseConfig will later accept.
//
// A config that will not parse back is the operator's own input failing
// that package's own trust boundary, which is a refusal about the request
// rather than a fault, so it carries a key rather than becoming a 500.
func marshalAndSealS3(cfg objstore.Config, plain string) ([]byte, secret.Secret, error) {
	b, err := cfg.Marshal()
	if err != nil {
		return nil, secret.Secret{}, err
	}
	if _, perr := objstore.ParseConfig(b); perr != nil {
		return nil, secret.Secret{}, unprocessable("admin.share_config_invalid")
	}
	return b, secret.New([]byte(plain)), nil
}

// marshalAndSealVault is marshalAndSealS3 for the veracrypt backend.
func marshalAndSealVault(cfg vault.Config, plain string) ([]byte, secret.Secret, error) {
	b, err := cfg.Marshal()
	if err != nil {
		return nil, secret.Secret{}, err
	}
	if _, perr := vault.ParseConfig(b); perr != nil {
		return nil, secret.Secret{}, unprocessable("admin.share_config_invalid")
	}
	return b, secret.New([]byte(plain)), nil
}

// grantShareTo gives one account full access to one share.
//
// The same permission set setup writes, and the share's own name as the
// label, so the two paths produce grants a reader cannot tell apart.
func (h *handlers) grantShareTo(c *gin.Context, user int64, share core.ShareDef) error {
	_, err := h.d.Core.CreateGrant(c.Request.Context(), core.GrantSpec{
		User: &user, Share: share.ID,
		Allow:   acl.Read | acl.Write | acl.Create | acl.Delete | acl.Rename | acl.Move | acl.Share | acl.Download,
		Inherit: true, Label: share.Name,
	})
	return err
}

// updateShareRequest carries only what changes. Pointers separate an absent
// field from a cleared one, which is the difference between leaving the trash
// alone and turning it off.
type updateShareRequest struct {
	Name         *string `json:"name"`
	Host         *string `json:"host"`
	TrashEnabled *bool   `json:"trash_enabled"`

	// Backend is accepted only so that naming a different one is refused
	// with a reason. A share's backend is fixed at creation: every grant,
	// share link and cached identity references data the old backend holds,
	// and repointing the share would leave all of them naming something
	// that is not there. Without the field the decoder would refuse the
	// whole body as malformed and say nothing about why.
	Backend *string `json:"backend"`

	// S3 patches an s3 share's own fields. Refused unless the share's
	// current backend is s3.
	S3 *shareS3Request `json:"s3"`

	// Veracrypt patches a veracrypt share's own fields. Refused unless the
	// share's current backend is veracrypt.
	Veracrypt *shareVeracryptRequest `json:"veracrypt"`
}

// adminSharesUpdate changes one.
func (h *handlers) sharesUpdate(c *gin.Context) {
	if _, ok := h.admin(c); !ok {
		return
	}
	id, ok := shareIDOf(c)
	if !ok {
		notFound(c)
		return
	}
	var req updateShareRequest
	if !decode(c, &req) {
		return
	}
	patch := core.SharePatch{Name: req.Name, Host: req.Host, TrashEnabled: req.TrashEnabled, Backend: req.Backend}
	if req.S3 != nil || req.Veracrypt != nil {
		current, found := h.d.Core.Share(id)
		if !found {
			notFound(c)
			return
		}
		if err := applyShareBackendPatch(&patch, current, req.S3, req.Veracrypt); err != nil {
			fail(c, err)
			return
		}
	}
	share, err := h.d.Core.UpdateShare(c.Request.Context(), id, patch)
	if err != nil {
		if errors.Is(err, core.ErrUnprocessable) {
			refuse(c, apierr.Classified{Class: apierr.Unprocessable, Key: "admin.share_backend_immutable"})
			return
		}
		fail(c, err)
		return
	}
	if h.d.MarkSearchIncomplete != nil {
		h.d.MarkSearchIncomplete()
	}
	if h.d.WatchShare != nil {
		h.d.WatchShare(share)
	}
	json(c, http.StatusOK, handler.ShareOf(share))
}

// applyShareBackendPatch validates the request's s3 and veracrypt objects
// against the share's own current backend, and folds the change into
// patch.
//
// A patch may carry only the object matching the share's own backend: an
// s3 object against a veracrypt share, or either against a local one,
// names a field the receiving backend does not have. current.Config is
// parsed and only the fields the request actually names are overwritten,
// so a patch that touches one field of an s3 share does not have to
// repeat every other one.
func applyShareBackendPatch(
	patch *core.SharePatch, current core.ShareDef, s3 *shareS3Request, vc *shareVeracryptRequest,
) error {
	switch {
	case s3 != nil && current.Backend != core.BackendS3,
		vc != nil && current.Backend != core.BackendVeracrypt:
		return unprocessable("admin.share_backend_extra_config")
	case s3 == nil && vc == nil:
		return nil
	}

	switch current.Backend {
	case core.BackendS3:
		cfg, perr := objstore.ParseConfig(current.Config)
		if perr != nil {
			return perr
		}
		plain, aerr := applyS3Patch(&cfg, s3)
		if aerr != nil {
			return aerr
		}
		b, sec, merr := marshalAndSealS3(cfg, plain)
		if merr != nil {
			return merr
		}
		patch.Config = &b
		if plain != "" {
			patch.Secret = &sec
		}
	case core.BackendVeracrypt:
		cfg, perr := vault.ParseConfig(current.Config)
		if perr != nil {
			return perr
		}
		plain, aerr := applyVeracryptPatch(&cfg, vc)
		if aerr != nil {
			return aerr
		}
		b, sec, merr := marshalAndSealVault(cfg, plain)
		if merr != nil {
			return merr
		}
		patch.Config = &b
		if plain != "" {
			patch.Secret = &sec
		}
	}
	return nil
}

// applyS3Patch overwrites cfg with whichever fields req names, and reports
// the new credential, empty when the request left it alone.
func applyS3Patch(cfg *objstore.Config, req *shareS3Request) (string, error) {
	if req.Endpoint != nil {
		cfg.Endpoint = *req.Endpoint
	}
	if req.Region != nil {
		cfg.Region = *req.Region
	}
	if req.Bucket != nil {
		cfg.Bucket = *req.Bucket
	}
	if req.Prefix != nil {
		cfg.Prefix = *req.Prefix
	}
	if req.AccessKeyID != nil {
		cfg.AccessKey = *req.AccessKeyID
	}
	if req.PathStyle != nil {
		cfg.PathStyle = *req.PathStyle
	}
	if req.SecretAccessKey == nil {
		return "", nil
	}
	if *req.SecretAccessKey == "" {
		return "", unprocessable("admin.share_secret_not_clearable")
	}
	return *req.SecretAccessKey, nil
}

// applyVeracryptPatch is applyS3Patch for the veracrypt backend. Create and
// SizeMiB are refused outright: that path runs once, at creation, and a
// patch asking for it again would either try to recreate a container in
// use or silently do nothing, neither of which is what the field name
// promises.
func applyVeracryptPatch(cfg *vault.Config, req *shareVeracryptRequest) (string, error) {
	if req.Create != nil || req.SizeMiB != nil {
		return "", unprocessable("admin.share_vault_create_immutable")
	}
	if req.Container != nil {
		cfg.Container = *req.Container
	}
	if req.PIM != nil {
		cfg.PIM = *req.PIM
	}
	if req.Password == nil {
		return "", nil
	}
	if *req.Password == "" {
		return "", unprocessable("admin.share_secret_not_clearable")
	}
	return *req.Password, nil
}

// adminSharesRetry re-opens a share whose backing was unavailable.
//
// A separate route rather than something the listing does on its own: opening
// a dead mount can block, and a listing that retried every broken share would
// take as long as the slowest one every time an administrator looked at the
// screen.
func (h *handlers) sharesRetry(c *gin.Context) {
	if _, ok := h.admin(c); !ok {
		return
	}
	id, ok := shareIDOf(c)
	if !ok {
		notFound(c)
		return
	}
	share, err := h.d.Core.RetryShare(c.Request.Context(), id)
	if err != nil {
		fail(c, err)
		return
	}
	json(c, http.StatusOK, handler.ShareOf(share))
}

// adminSharesDelete unregisters one.
//
// The stored files are not touched. Unregistering is an administrative act
// about what this deployment serves; deleting the data would make a mistyped
// id destroy a directory nobody meant to name.
func (h *handlers) sharesDelete(c *gin.Context) {
	if _, ok := h.admin(c); !ok {
		return
	}
	id, ok := shareIDOf(c)
	if !ok {
		notFound(c)
		return
	}
	share, found := h.d.Core.Share(id)
	if !found {
		notFound(c)
		return
	}
	if err := h.d.Core.DeleteShare(c.Request.Context(), id); err != nil {
		fail(c, err)
		return
	}
	if h.d.UnwatchShare != nil {
		h.d.UnwatchShare(share)
	}
	c.Status(http.StatusNoContent)
}

// shareIDOf reads the path's share id.
//
// A share id is narrower than the decimal a path can carry, so the value is
// narrowed rather than converted: a converted id past the width wraps onto a
// different share, which turns a mistyped number into a delete of something
// nobody named.
func shareIDOf(c *gin.Context) (core.ShareID, bool) {
	raw, ok := pathID(c)
	if !ok {
		return 0, false
	}
	narrowed, err := num.Narrow[uint32](raw)
	if err != nil {
		return 0, false
	}
	return core.ShareID(narrowed), true
}

// adminGrantsList answers the grants, optionally for one subject or share.
func (h *handlers) grantsList(c *gin.Context) {
	if _, ok := h.admin(c); !ok {
		return
	}
	rows, err := h.d.Core.ListGrants(c.Request.Context(), core.GrantFilter{User: queryInt(c.Query("user")), Group: queryInt(c.Query("group")), Share: queryInt(c.Query("share"))})
	if err != nil {
		fail(c, err)
		return
	}
	json(c, http.StatusOK, handler.GrantsOf(rows))
}

// grantRequest is one permission assignment.
type grantRequest struct {
	// Exactly one of these names the subject. A grant to both would be two
	// grants, and a grant to neither would apply to nobody.
	User  string `json:"user"`
	Group string `json:"group"`

	Share   string `json:"share"`
	Subpath string `json:"subpath"`

	// Allow and Deny are permission names. Unknown ones are refused rather
	// than dropped: storing a grant weaker than the one the screen showed is
	// how an administrator believes they gave access that nobody has.
	Allow []string `json:"allow"`
	Deny  []string `json:"deny"`

	Inherit bool   `json:"inherit"`
	Label   string `json:"label"`
}

// adminGrantsCreate adds one.
func (h *handlers) grantsCreate(c *gin.Context) {
	if _, ok := h.admin(c); !ok {
		return
	}
	var req grantRequest
	if !decode(c, &req) {
		return
	}
	spec, ok := grantSpecOf(req)
	if !ok {
		refuse(c, apierr.Classified{Class: apierr.Unprocessable})
		return
	}
	if spec.Label == "" && spec.Subpath == "" {
		if def, found := h.d.Core.Share(spec.Share); found {
			spec.Label = def.Name
		}
	}
	grant, err := h.d.Core.CreateGrant(c.Request.Context(), spec)
	if err != nil {
		fail(c, err)
		return
	}
	json(c, http.StatusCreated, handler.GrantOf(grant))
}

// grantSpecOf validates a request into a spec.
//
// The false return is one refusal for every way the request cannot be
// honoured, because each of them means the same thing to the caller: this
// grant was not stored. What must not happen is storing a different grant
// from the one described.
func grantSpecOf(req grantRequest) (core.GrantSpec, bool) {
	share, err := strconv.ParseUint(req.Share, 10, 32)
	if err != nil || share == 0 {
		return core.GrantSpec{}, false
	}

	spec := core.GrantSpec{
		Share:   core.ShareID(share),
		Subpath: req.Subpath,
		Inherit: req.Inherit,
		Label:   req.Label,
	}

	// Exactly one subject. Both would be ambiguous about who it applies to,
	// and neither would be a grant nobody holds.
	//
	// The store refuses both cases too, so this is where the refusal happens
	// rather than the only place it could. Measured: removing the "neither"
	// branch changes no answer, because the store rejects a subjectless grant;
	// removing the "both" branch does change one, since this is what decides
	// which subject wins when a request names two.
	switch {
	case req.User != "" && req.Group != "":
		return core.GrantSpec{}, false
	case req.User != "":
		id, perr := strconv.ParseInt(req.User, 10, 64)
		if perr != nil || id <= 0 {
			return core.GrantSpec{}, false
		}
		spec.User = &id
	case req.Group != "":
		id, perr := strconv.ParseInt(req.Group, 10, 64)
		if perr != nil || id <= 0 {
			return core.GrantSpec{}, false
		}
		spec.Group = &id
	default:
		return core.GrantSpec{}, false
	}

	allow, ok := permsOf(req.Allow)
	if !ok {
		return core.GrantSpec{}, false
	}
	deny, ok := permsOf(req.Deny)
	if !ok {
		return core.GrantSpec{}, false
	}
	spec.Allow, spec.Deny = allow, deny
	return spec, true
}

// permsOf turns permission names into a set.
//
// One unknown name refuses the whole list. Skipping it would store a grant
// that differs from the one requested, and the difference is silent: the
// administrator sees the name they typed and the system holds a set without
// it.
func permsOf(names []string) (acl.Perms, bool) {
	var out acl.Perms
	for _, name := range names {
		bit, known := acl.PermByName(name)
		if !known {
			return 0, false
		}
		out |= bit
	}
	return out, true
}

// updateGrantRequest replaces a grant's permissions.
type updateGrantRequest struct {
	Allow   []string `json:"allow"`
	Deny    []string `json:"deny"`
	Inherit bool     `json:"inherit"`
	Label   string   `json:"label"`
}

// adminGrantsUpdate changes one.
func (h *handlers) grantsUpdate(c *gin.Context) {
	if _, ok := h.admin(c); !ok {
		return
	}
	id, ok := pathID(c)
	if !ok {
		notFound(c)
		return
	}
	var req updateGrantRequest
	if !decode(c, &req) {
		return
	}
	allow, ok := permsOf(req.Allow)
	if !ok {
		refuse(c, apierr.Classified{Class: apierr.Unprocessable})
		return
	}
	deny, ok := permsOf(req.Deny)
	if !ok {
		refuse(c, apierr.Classified{Class: apierr.Unprocessable})
		return
	}
	grant, err := h.d.Core.UpdateGrant(c.Request.Context(), id, allow, deny, req.Inherit, req.Label)
	if err != nil {
		fail(c, err)
		return
	}
	json(c, http.StatusOK, handler.GrantOf(grant))
}

// adminGrantsDelete revokes one.
func (h *handlers) grantsDelete(c *gin.Context) {
	if _, ok := h.admin(c); !ok {
		return
	}
	id, ok := pathID(c)
	if !ok {
		notFound(c)
		return
	}
	if err := h.d.Core.DeleteGrant(c.Request.Context(), id); err != nil {
		fail(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
