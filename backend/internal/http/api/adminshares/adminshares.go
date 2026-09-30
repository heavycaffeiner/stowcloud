//go:build linux

// Shares and grants, as an administrator sees them.
package adminshares

// Host paths and credentials are never rendered by the projections below.

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/danielgtaylor/huma/v2"

	"github.com/heavycaffeiner/stowcloud/backend/internal/feature/shares/acl"
	"github.com/heavycaffeiner/stowcloud/backend/internal/files"
	"github.com/heavycaffeiner/stowcloud/backend/internal/fs/objstore"
	"github.com/heavycaffeiner/stowcloud/backend/internal/fs/vault"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/api/handler"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/api/humabridge"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/apierr"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/middleware"
	num "github.com/heavycaffeiner/stowcloud/backend/internal/platform/number"
	secret "github.com/heavycaffeiner/stowcloud/backend/internal/platform/security/secret"
)

// Deps supplies the narrow product services used by administrator share and
// grant routes. Runtime hooks are post-commit side effects of registration.
type Deps struct {
	Core                 *files.Core
	MarkSearchIncomplete func()
	WatchShare           func(files.ShareDef)
	UnwatchShare         func(files.ShareDef)
	Logger               *slog.Logger
}

// Register adds typed administrator share and grant operations. Paths are
// relative to the caller's API mount. Authorization is supplied by the
// application middleware before Huma validates an operation input.
func Register(api huma.API, d Deps) {
	h := &handlers{d: d}
	huma.Register[sharesListInput, sharesListOutput](api, huma.Operation{
		OperationID: "admin.shares.list", Method: http.MethodGet, Path: "/admin/shares",
	}, h.sharesListHuma)
	huma.Register[sharesCreateInput, shareCreatedOutput](api, huma.Operation{
		OperationID: "admin.shares.create", Method: http.MethodPost, Path: "/admin/shares",
	}, h.sharesCreateHuma)
	huma.Register[shareUpdateInput, shareOutput](api, huma.Operation{
		OperationID: "admin.shares.update", Method: http.MethodPatch, Path: "/admin/shares/{id}",
	}, h.sharesUpdateHuma)
	huma.Register[sharePathInput, shareOutput](api, huma.Operation{
		OperationID: "admin.shares.retry", Method: http.MethodPost, Path: "/admin/shares/{id}/retry",
	}, h.sharesRetryHuma)
	huma.Register[sharePathInput, noContentOutput](api, huma.Operation{
		OperationID: "admin.shares.delete", Method: http.MethodDelete, Path: "/admin/shares/{id}",
	}, h.sharesDeleteHuma)
	huma.Register[grantsListInput, grantsListOutput](api, huma.Operation{
		OperationID: "admin.grants.list", Method: http.MethodGet, Path: "/admin/grants",
	}, h.grantsListHuma)
	huma.Register[grantCreateInput, grantCreatedOutput](api, huma.Operation{
		OperationID: "admin.grants.create", Method: http.MethodPost, Path: "/admin/grants",
	}, h.grantsCreateHuma)
	huma.Register[grantUpdateInput, grantOutput](api, huma.Operation{
		OperationID: "admin.grants.update", Method: http.MethodPatch, Path: "/admin/grants/{id}",
	}, h.grantsUpdateHuma)
	huma.Register[grantPathInput, noContentOutput](api, huma.Operation{
		OperationID: "admin.grants.delete", Method: http.MethodDelete, Path: "/admin/grants/{id}",
	}, h.grantsDeleteHuma)
}

type handlers struct{ d Deps }

// Typed Huma request and response models preserve native JSON shapes. IDs and
// numeric query values remain strings because the native API accepts decimal
// JSON strings and treats invalid values as zero or not-found.
type sharesListInput struct{}
type sharesListOutput struct{ Body []handler.ShareView }

type sharesCreateInput struct {
	_    struct{} `json:"-" additionalProperties:"false"`
	Body createShareRequest
}
type shareCreatedOutput struct {
	Body   handler.ShareView
	Status int `status:"201"`
}

type sharePathInput struct {
	ID string `path:"id"`
}
type shareUpdateInput struct {
	_    struct{} `json:"-" additionalProperties:"false"`
	ID   string   `path:"id"`
	Body updateShareRequest
}
type shareOutput struct{ Body handler.ShareView }

type grantsListInput struct {
	User  string `query:"user"`
	Group string `query:"group"`
	Share string `query:"share"`
}
type grantsListOutput struct{ Body []handler.GrantView }
type grantCreateInput struct {
	_    struct{} `json:"-" additionalProperties:"false"`
	Body grantRequest
}
type grantCreatedOutput struct {
	Body   handler.GrantView
	Status int `status:"201"`
}
type grantPathInput struct {
	ID string `path:"id"`
}
type grantUpdateInput struct {
	_    struct{} `json:"-" additionalProperties:"false"`
	ID   string   `path:"id"`
	Body updateGrantRequest
}
type grantOutput struct{ Body handler.GrantView }
type noContentOutput struct {
	Status int `status:"204"`
}

func humaShareID(raw string) (files.ShareID, bool) {
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n <= 0 {
		return 0, false
	}
	narrowed, err := num.Narrow[uint32](n)
	if err != nil {
		return 0, false
	}
	return files.ShareID(narrowed), true
}

func humaGrantID(raw string) (int64, bool) {
	n, err := strconv.ParseInt(raw, 10, 64)
	return n, err == nil && n > 0
}

func (h *handlers) adminUser(ctx context.Context) (int64, error) {
	c := humabridge.Gin(ctx)
	v, ok := c.Get(string(middleware.KeyCredential))
	p, valid := v.(middleware.Principal)
	if !ok || !valid || p.UserID == 0 {
		return 0, humabridge.Refusal(apierr.Classified{Class: apierr.AuthRequired})
	}
	return p.UserID, nil
}

func (h *handlers) sharesListHuma(ctx context.Context, _ *sharesListInput) (*sharesListOutput, error) {
	empty := func(id files.ShareID) bool { return h.d.Core.ShareEmpty(ctx, id) }
	return &sharesListOutput{Body: handler.SharesOf(h.d.Core.Shares(), empty)}, nil
}

func (h *handlers) sharesCreateHuma(ctx context.Context, in *sharesCreateInput) (*shareCreatedOutput, error) {
	admin, err := h.adminUser(ctx)
	if err != nil {
		return nil, err
	}
	spec, err := shareSpecOf(in.Body)
	if err != nil {
		return nil, humabridge.Failure(ctx, err)
	}
	share, err := h.d.Core.CreateShare(ctx, spec)
	if err != nil {
		if errors.Is(err, files.ErrUnprocessable) {
			return nil, humabridge.Refusal(apierr.Classified{Class: apierr.Unprocessable, Key: "admin.share_backend_unknown"})
		}
		return nil, humabridge.Failure(ctx, err)
	}
	if h.d.MarkSearchIncomplete != nil {
		h.d.MarkSearchIncomplete()
	}
	if h.d.WatchShare != nil {
		h.d.WatchShare(share)
	}
	if err := h.grantShareToContext(ctx, admin, share); err != nil {
		if h.d.Logger == nil {
			h.d.Logger = slog.Default()
		}
		h.d.Logger.Warn("the new share was registered without a grant for its creator", "share", int64(share.ID), "error", err)
	}
	return &shareCreatedOutput{Body: handler.ShareOf(share), Status: http.StatusCreated}, nil
}

func (h *handlers) grantShareToContext(ctx context.Context, user int64, share files.ShareDef) error {
	_, err := h.d.Core.CreateGrant(ctx, files.GrantSpec{
		User: &user, Share: share.ID,
		Allow:   acl.Read | acl.Write | acl.Create | acl.Delete | acl.Rename | acl.Move | acl.Share | acl.Download,
		Inherit: true, Label: share.Name,
	})
	return err
}

func (h *handlers) sharesUpdateHuma(ctx context.Context, in *shareUpdateInput) (*shareOutput, error) {
	id, ok := humaShareID(in.ID)
	if !ok {
		return nil, humabridge.Failure(ctx, files.ErrNotFound)
	}
	req := in.Body
	if req.Name == nil && req.Host == nil && req.TrashEnabled == nil && req.Backend == nil && req.S3 == nil && req.Veracrypt == nil {
		return nil, humabridge.Refusal(apierr.Classified{Class: apierr.Malformed})
	}
	patch := files.SharePatch{Name: req.Name, Host: req.Host, TrashEnabled: req.TrashEnabled, Backend: req.Backend}
	if req.S3 != nil || req.Veracrypt != nil {
		current, found := h.d.Core.Share(id)
		if !found {
			return nil, humabridge.Failure(ctx, files.ErrNotFound)
		}
		if err := applyShareBackendPatch(&patch, current, req.S3, req.Veracrypt); err != nil {
			return nil, humabridge.Failure(ctx, err)
		}
	}
	share, err := h.d.Core.UpdateShare(ctx, id, patch)
	if err != nil {
		if errors.Is(err, files.ErrUnprocessable) {
			return nil, humabridge.Refusal(apierr.Classified{Class: apierr.Unprocessable, Key: "admin.share_backend_immutable"})
		}
		return nil, humabridge.Failure(ctx, err)
	}
	if h.d.MarkSearchIncomplete != nil {
		h.d.MarkSearchIncomplete()
	}
	if h.d.WatchShare != nil {
		h.d.WatchShare(share)
	}
	return &shareOutput{Body: handler.ShareOf(share)}, nil
}

func (h *handlers) sharesRetryHuma(ctx context.Context, in *sharePathInput) (*shareOutput, error) {
	id, ok := humaShareID(in.ID)
	if !ok {
		return nil, humabridge.Failure(ctx, files.ErrNotFound)
	}
	share, err := h.d.Core.RetryShare(ctx, id)
	if err != nil {
		return nil, humabridge.Failure(ctx, err)
	}
	return &shareOutput{Body: handler.ShareOf(share)}, nil
}

func (h *handlers) sharesDeleteHuma(ctx context.Context, in *sharePathInput) (*noContentOutput, error) {
	id, ok := humaShareID(in.ID)
	if !ok {
		return nil, humabridge.Failure(ctx, files.ErrNotFound)
	}
	share, found := h.d.Core.Share(id)
	if !found {
		return nil, humabridge.Failure(ctx, files.ErrNotFound)
	}
	if err := h.d.Core.DeleteShare(ctx, id); err != nil {
		return nil, humabridge.Failure(ctx, err)
	}
	if h.d.UnwatchShare != nil {
		h.d.UnwatchShare(share)
	}
	return &noContentOutput{Status: http.StatusNoContent}, nil
}

func (h *handlers) grantsListHuma(ctx context.Context, in *grantsListInput) (*grantsListOutput, error) {
	rows, err := h.d.Core.ListGrants(ctx, files.GrantFilter{
		User: queryInt(in.User), Group: queryInt(in.Group), Share: queryInt(in.Share),
	})
	if err != nil {
		return nil, humabridge.Failure(ctx, err)
	}
	return &grantsListOutput{Body: handler.GrantsOf(rows)}, nil
}

func (h *handlers) grantsCreateHuma(ctx context.Context, in *grantCreateInput) (*grantCreatedOutput, error) {
	spec, ok := grantSpecOf(in.Body)
	if !ok {
		return nil, humabridge.Refusal(apierr.Classified{Class: apierr.Unprocessable})
	}
	if spec.Label == "" && spec.Subpath == "" {
		if def, found := h.d.Core.Share(spec.Share); found {
			spec.Label = def.Name
		}
	}
	grant, err := h.d.Core.CreateGrant(ctx, spec)
	if err != nil {
		return nil, humabridge.Failure(ctx, err)
	}
	return &grantCreatedOutput{Body: handler.GrantOf(grant), Status: http.StatusCreated}, nil
}

func (h *handlers) grantsUpdateHuma(ctx context.Context, in *grantUpdateInput) (*grantOutput, error) {
	id, ok := humaGrantID(in.ID)
	if !ok {
		return nil, humabridge.Failure(ctx, files.ErrNotFound)
	}
	allow, ok := permsOf(in.Body.Allow)
	if !ok {
		return nil, humabridge.Refusal(apierr.Classified{Class: apierr.Unprocessable})
	}
	deny, ok := permsOf(in.Body.Deny)
	if !ok {
		return nil, humabridge.Refusal(apierr.Classified{Class: apierr.Unprocessable})
	}
	grant, err := h.d.Core.UpdateGrant(ctx, id, allow, deny, in.Body.Inherit, in.Body.Label)
	if err != nil {
		return nil, humabridge.Failure(ctx, err)
	}
	return &grantOutput{Body: handler.GrantOf(grant)}, nil
}

func (h *handlers) grantsDeleteHuma(ctx context.Context, in *grantPathInput) (*noContentOutput, error) {
	id, ok := humaGrantID(in.ID)
	if !ok {
		return nil, humabridge.Failure(ctx, files.ErrNotFound)
	}
	if err := h.d.Core.DeleteGrant(ctx, id); err != nil {
		return nil, humabridge.Failure(ctx, err)
	}
	return &noContentOutput{Status: http.StatusNoContent}, nil
}

func queryInt(raw string) int64 {
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
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
func shareSpecOf(req createShareRequest) (files.ShareSpec, error) {
	backend, berr := files.ParseBackend(req.Backend)
	if berr != nil {
		return files.ShareSpec{}, unprocessable("admin.share_backend_unknown")
	}
	spec := files.ShareSpec{Name: req.Name, Backend: backend}
	switch backend {
	case files.BackendLocal:
		if req.S3 != nil || req.Veracrypt != nil {
			return files.ShareSpec{}, unprocessable("admin.share_backend_extra_config")
		}
		if req.Host == "" {
			return files.ShareSpec{}, unprocessable("admin.share_host_required")
		}
		spec.Host = req.Host
	case files.BackendS3:
		if req.Veracrypt != nil || req.Host != "" {
			return files.ShareSpec{}, unprocessable("admin.share_backend_extra_config")
		}
		if req.S3 == nil {
			return files.ShareSpec{}, unprocessable("admin.share_backend_config_missing")
		}
		cfg, plain, cerr := s3ConfigForCreate(req.S3)
		if cerr != nil {
			return files.ShareSpec{}, cerr
		}
		configBytes, secretVal, merr := marshalAndSealS3(cfg, plain)
		if merr != nil {
			return files.ShareSpec{}, merr
		}
		spec.Config, spec.Secret = configBytes, secretVal
	case files.BackendVeracrypt:
		if req.S3 != nil || req.Host != "" {
			return files.ShareSpec{}, unprocessable("admin.share_backend_extra_config")
		}
		if req.Veracrypt == nil {
			return files.ShareSpec{}, unprocessable("admin.share_backend_config_missing")
		}
		cfg, plain, cerr := vaultConfigForCreate(req.Veracrypt)
		if cerr != nil {
			return files.ShareSpec{}, cerr
		}
		configBytes, secretVal, merr := marshalAndSealVault(cfg, plain)
		if merr != nil {
			return files.ShareSpec{}, merr
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
	patch *files.SharePatch, current files.ShareDef, s3 *shareS3Request, vc *shareVeracryptRequest,
) error {
	switch {
	case s3 != nil && current.Backend != files.BackendS3,
		vc != nil && current.Backend != files.BackendVeracrypt:
		return unprocessable("admin.share_backend_extra_config")
	case s3 == nil && vc == nil:
		return nil
	}

	switch current.Backend {
	case files.BackendS3:
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
	case files.BackendVeracrypt:
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

// grantSpecOf validates a request into a spec.
//
// The false return is one refusal for every way the request cannot be
// honoured, because each of them means the same thing to the caller: this
// grant was not stored. What must not happen is storing a different grant
// from the one described.
func grantSpecOf(req grantRequest) (files.GrantSpec, bool) {
	share, err := strconv.ParseUint(req.Share, 10, 32)
	if err != nil || share == 0 {
		return files.GrantSpec{}, false
	}

	spec := files.GrantSpec{
		Share:   files.ShareID(share),
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
		return files.GrantSpec{}, false
	case req.User != "":
		id, perr := strconv.ParseInt(req.User, 10, 64)
		if perr != nil || id <= 0 {
			return files.GrantSpec{}, false
		}
		spec.User = &id
	case req.Group != "":
		id, perr := strconv.ParseInt(req.Group, 10, 64)
		if perr != nil || id <= 0 {
			return files.GrantSpec{}, false
		}
		spec.Group = &id
	default:
		return files.GrantSpec{}, false
	}

	allow, ok := permsOf(req.Allow)
	if !ok {
		return files.GrantSpec{}, false
	}
	deny, ok := permsOf(req.Deny)
	if !ok {
		return files.GrantSpec{}, false
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
