//go:build linux

// Administration: shares and the grants over them.
//
// Nothing here reports a host path. A share's on-disk location is server
// configuration, and a client that learns it learns the layout of the machine.
// The projection has no field for one, so a future edit has to add it
// deliberately rather than by widening a struct.
package lifecycle

import (
	"errors"
	"strconv"

	"github.com/gofiber/fiber/v2"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vault"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/secret"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/apierr"
	"github.com/heavycaffeiner/stowcloud/go/engine/http/handler"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/objstore"
)

// adminSharesList answers every registered share.
//
// Including the broken ones. A share whose disk never came back is still
// registered, and dropping it from this listing is what once made an
// unreachable share indistinguishable from a deleted one.
func (e *Engine) adminSharesList(c *fiber.Ctx) error {
	if _, ok, written := e.admin(c); !ok {
		return written
	}
	// One storage walk per share, bounded: the walk stops at the first entry
	// it finds, so an occupied share costs one readdir and an empty one costs
	// two (the tree and its trash).
	ctx := c.UserContext()
	empty := func(id core.ShareID) bool { return e.Core.ShareEmpty(ctx, id) }
	return writeJSON(c, fiber.StatusOK, handler.SharesOf(e.Core.Shares(), empty))
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
}

// adminSharesCreate registers one.
func (e *Engine) adminSharesCreate(c *fiber.Ctx) error {
	admin, ok, written := e.admin(c)
	if !ok {
		return written
	}

	var req createShareRequest
	if err := decodeBody(c, &req); err != nil {
		return refuse(c, apierr.Classified{Class: apierr.Malformed})
	}

	spec, verr := shareSpecOf(req)
	if verr != nil {
		return fail(c, verr)
	}

	share, err := e.Core.CreateShare(c.UserContext(), spec)
	if err != nil {
		// CreateShare calls a backend name it does not implement
		// unprocessable, which shareSpecOf has already refused with its own
		// reason; reaching here means the two disagree, so the general class
		// is the honest answer.
		if errors.Is(err, core.ErrUnprocessable) {
			return refuse(c, apierr.Classified{
				Class: apierr.Unprocessable, Key: "admin.share_backend_unknown",
			})
		}
		return fail(c, err)
	}
	// The watcher learns about it now rather than at the next restart.
	// Without this a share registered while the server is running is one no
	// change is ever reported under, and the symptom is a folder that updates
	// for everybody except the person who just created it.
	e.watchShare(share)

	// The administrator who registered it can reach it. Access is granted
	// separately from registration by design, and everybody else still needs
	// a grant, but a folder that is invisible to the person who just added it
	// reads as the registration having failed. Setup does the same for the
	// shares that exist when it runs.
	if gerr := e.grantShareTo(c, admin, share); gerr != nil {
		e.logger.Warn("the new share was registered without a grant for its creator",
			"share", int64(share.ID), "error", gerr)
	}
	return writeJSON(c, fiber.StatusCreated, handler.ShareOf(share))
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
func (e *Engine) grantShareTo(c *fiber.Ctx, user int64, share core.ShareDef) error {
	_, err := e.Core.CreateGrant(c.UserContext(), core.GrantSpec{
		User:    &user,
		Share:   share.ID,
		Allow:   acl.Read | acl.Write | acl.Create | acl.Delete | acl.Rename | acl.Move | acl.Share | acl.Download,
		Inherit: true,
		Label:   share.Name,
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
func (e *Engine) adminSharesUpdate(c *fiber.Ctx) error {
	if _, ok, written := e.admin(c); !ok {
		return written
	}
	id, ok := shareIDOf(c)
	if !ok {
		return notFound(c)
	}

	var req updateShareRequest
	if err := decodeBody(c, &req); err != nil {
		return refuse(c, apierr.Classified{Class: apierr.Malformed})
	}

	patch := core.SharePatch{
		Name:         req.Name,
		Host:         req.Host,
		TrashEnabled: req.TrashEnabled,
		Backend:      req.Backend,
	}
	if req.S3 != nil || req.Veracrypt != nil {
		current, found := e.Core.Share(id)
		if !found {
			return notFound(c)
		}
		if perr := applyShareBackendPatch(&patch, current, req.S3, req.Veracrypt); perr != nil {
			return fail(c, perr)
		}
	}

	share, err := e.Core.UpdateShare(c.UserContext(), id, patch)
	if err != nil {
		// The only thing UpdateShare calls unprocessable is the patch naming
		// a backend the share does not already have, so the refusal is
		// reported as that rather than as the general class.
		if errors.Is(err, core.ErrUnprocessable) {
			return refuse(c, apierr.Classified{
				Class: apierr.Unprocessable, Key: "admin.share_backend_immutable",
			})
		}
		return fail(c, err)
	}
	return writeJSON(c, fiber.StatusOK, handler.ShareOf(share))
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
func (e *Engine) adminSharesRetry(c *fiber.Ctx) error {
	if _, ok, written := e.admin(c); !ok {
		return written
	}
	id, ok := shareIDOf(c)
	if !ok {
		return notFound(c)
	}

	share, err := e.Core.RetryShare(c.UserContext(), id)
	if err != nil {
		return fail(c, err)
	}
	return writeJSON(c, fiber.StatusOK, handler.ShareOf(share))
}

// adminSharesDelete unregisters one.
//
// The stored files are not touched. Unregistering is an administrative act
// about what this deployment serves; deleting the data would make a mistyped
// id destroy a directory nobody meant to name.
func (e *Engine) adminSharesDelete(c *fiber.Ctx) error {
	if _, ok, written := e.admin(c); !ok {
		return written
	}
	id, ok := shareIDOf(c)
	if !ok {
		return notFound(c)
	}

	if err := e.Core.DeleteShare(c.UserContext(), id); err != nil {
		return fail(c, err)
	}
	return c.SendStatus(fiber.StatusNoContent)
}

// shareIDOf reads the path's share id.
//
// A share id is narrower than the decimal a path can carry, so the value is
// narrowed rather than converted: a converted id past the width wraps onto a
// different share, which turns a mistyped number into a delete of something
// nobody named.
func shareIDOf(c *fiber.Ctx) (core.ShareID, bool) {
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
func (e *Engine) adminGrantsList(c *fiber.Ctx) error {
	if _, ok, written := e.admin(c); !ok {
		return written
	}

	rows, err := e.Core.ListGrants(c.UserContext(), core.GrantFilter{
		User:  queryInt(c.Query("user")),
		Group: queryInt(c.Query("group")),
		Share: queryInt(c.Query("share")),
	})
	if err != nil {
		return fail(c, err)
	}
	return writeJSON(c, fiber.StatusOK, handler.GrantsOf(rows))
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
func (e *Engine) adminGrantsCreate(c *fiber.Ctx) error {
	if _, ok, written := e.admin(c); !ok {
		return written
	}

	var req grantRequest
	if err := decodeBody(c, &req); err != nil {
		return refuse(c, apierr.Classified{Class: apierr.Malformed})
	}

	spec, ok := grantSpecOf(req)
	if !ok {
		return refuse(c, apierr.Classified{Class: apierr.Unprocessable})
	}

	// An unlabelled grant over a whole share takes the share's own name. The
	// screen draws the label, and without one the listing falls back to a
	// generated placeholder naming the share's id rather than the folder
	// somebody picked.
	if spec.Label == "" && spec.Subpath == "" {
		if def, found := e.Core.Share(spec.Share); found {
			spec.Label = def.Name
		}
	}

	grant, err := e.Core.CreateGrant(c.UserContext(), spec)
	if err != nil {
		return fail(c, err)
	}
	// The whole grant, because the screen appends it to the list it is already
	// showing. An id alone left every rendered row reading permission arrays
	// that were not there.
	return writeJSON(c, fiber.StatusCreated, handler.GrantOf(grant))
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
func (e *Engine) adminGrantsUpdate(c *fiber.Ctx) error {
	if _, ok, written := e.admin(c); !ok {
		return written
	}
	id, ok := pathID(c)
	if !ok {
		return notFound(c)
	}

	var req updateGrantRequest
	if err := decodeBody(c, &req); err != nil {
		return refuse(c, apierr.Classified{Class: apierr.Malformed})
	}

	allow, ok := permsOf(req.Allow)
	if !ok {
		return refuse(c, apierr.Classified{Class: apierr.Unprocessable})
	}
	deny, ok := permsOf(req.Deny)
	if !ok {
		return refuse(c, apierr.Classified{Class: apierr.Unprocessable})
	}

	grant, err := e.Core.UpdateGrant(c.UserContext(), id, allow, deny, req.Inherit, req.Label)
	if err != nil {
		return fail(c, err)
	}
	// The whole grant, as the create route answers: the screen swaps the
	// edited row for what came back, and every rendered row reads the
	// permission arrays. Answering no content left it with nothing to swap
	// in, and the change applied while the dialogue said it had not.
	return writeJSON(c, fiber.StatusOK, handler.GrantOf(grant))
}

// adminGrantsDelete revokes one.
func (e *Engine) adminGrantsDelete(c *fiber.Ctx) error {
	if _, ok, written := e.admin(c); !ok {
		return written
	}
	id, ok := pathID(c)
	if !ok {
		return notFound(c)
	}

	if err := e.Core.DeleteGrant(c.UserContext(), id); err != nil {
		return fail(c, err)
	}
	return c.SendStatus(fiber.StatusNoContent)
}
