//go:build linux

// The sentinel table: every service error this build recognises, and what it
// means to a protocol.
//
// One table, consulted by one apierr.Classifier. The old tree had this ladder
// three times and the copies disagreed, which is a disclosure difference nobody
// chose: the same refusal became 404 on one surface and 403 on another.
//
// Order is specific before general, because errors.Is matches a wrapped error
// and two sentinels can both match.

package app

import (
	"github.com/heavycaffeiner/stowcloud/backend/internal/auth"
	"github.com/heavycaffeiner/stowcloud/backend/internal/files"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/server"
	"github.com/heavycaffeiner/stowcloud/backend/internal/oidc"
	"github.com/heavycaffeiner/stowcloud/backend/internal/preview"
	"github.com/heavycaffeiner/stowcloud/backend/internal/server/apierr"
	"github.com/heavycaffeiner/stowcloud/backend/internal/uploads"
)

// ErrorClassifier classifies errors against this build's sentinel table.
func ErrorClassifier() *apierr.Classifier { return apierr.NewClassifier(errorTable()) }

func errorTable() []apierr.Sentinel {
	out := make([]apierr.Sentinel, 0, 64)
	out = append(out, coreSentinels()...)
	out = append(out, authSentinels()...)
	out = append(out, uploadSentinels()...)
	out = append(out, previewSentinels()...)
	out = append(out, storeSentinels()...)
	out = append(out, oidcSentinels()...)
	out = append(out, setupSentinels()...)
	return out
}

// oidcSentinels is the relying-party client: discovery, the back-channel
// exchange, address safety and token verification.
//
// ErrTokenVerify classifies as AuthInvalid rather than a class of its own: a
// token that arrived and failed verification (missing kid, bad signature,
// wrong issuer or audience) is a credential presented and rejected, the
// same shape as any other failed credential, and defect 15's no-kid case is
// exactly this path. Everything else here is the back channel failing to
// produce a usable answer at all, which is OIDCProviderUnavailable.
func oidcSentinels() []apierr.Sentinel {
	return []apierr.Sentinel{
		{Err: oidc.ErrDiscovery, Class: apierr.OIDCProviderUnavailable, Key: "oidc.provider_unavailable"},
		{Err: oidc.ErrProvider, Class: apierr.OIDCProviderUnavailable, Key: "oidc.provider_unavailable"},
		{Err: oidc.ErrNoTrustAnchors, Class: apierr.OIDCProviderUnavailable, Key: "oidc.provider_unavailable"},
		{Err: oidc.ErrAddressBlocked, Class: apierr.OIDCProviderUnavailable, Key: "oidc.provider_unavailable"},
		{Err: oidc.ErrTokenVerify, Class: apierr.AuthInvalid, Key: "auth.invalid_credentials"},
	}
}

// storeSentinels is the database layer, reached through the service tier.
//
// The size guard's refusal is a decision an operator made, and reporting it
// as an internal error left them with a screen that failed for no stated
// reason while the setting they had set was working exactly as configured.
// A malformed grant is the caller's own mistake to correct. A duplicate
// grant is neither: the request was well formed and the operator's own
// prior grant is what it conflicts with, the same shape as the create
// collisions above classify by, so it takes Conflict rather than
// Unprocessable. Everything else the store raises is a fault rather than a
// decision, and reporting a fault to a caller names tables and paths.
func storeSentinels() []apierr.Sentinel {
	return []apierr.Sentinel{
		{Err: files.ErrWritesBlocked, Class: apierr.SubsystemUnavailable, Key: "store.writes_blocked"},
		{Err: files.ErrGrantMalformed, Class: apierr.Unprocessable, Key: "admin.invalid_grant"},
		{Err: files.ErrGrantAlreadyExists, Class: apierr.Conflict, Key: "admin.grant_exists"},
	}
}

// coreSentinels is the filesystem domain.
//
// ErrDenied and ErrNotFound classify separately here and the visibility rule
// folds them together where the caller must not learn which it was. Doing the
// fold in the table instead would make a denial unreportable even on a surface
// the caller reached legitimately.
func coreSentinels() []apierr.Sentinel {
	return []apierr.Sentinel{
		{Err: files.ErrDenied, Class: apierr.Denied, Key: "fs.denied"},
		{Err: files.ErrNotFound, Class: apierr.NotFound, Key: "fs.not_found"},

		{Err: files.ErrExists, Class: apierr.Exists, Key: "fs.exists"},
		{Err: files.ErrNotEmpty, Class: apierr.NotEmpty, Key: "fs.not_empty"},
		{Err: files.ErrCrossShare, Class: apierr.Conflict, Key: "fs.cross_share"},
		{Err: files.ErrTrashDisabled, Class: apierr.Conflict, Key: "fs.trash_disabled"},
		{Err: files.ErrConflict, Class: apierr.Conflict, Key: "fs.conflict"},

		{Err: files.ErrPrecondition, Class: apierr.Precondition, Key: "fs.precondition_failed"},

		// A well-formed request the target's own state refuses, which is
		// neither a race nor a missing precondition header, so it takes
		// Unprocessable rather than Conflict or Precondition.
		{Err: files.ErrUnprocessable, Class: apierr.Unprocessable, Key: "fs.unprocessable"},

		{Err: files.ErrQuotaExceeded, Class: apierr.NoSpace, Key: "fs.quota_exceeded"},
		{Err: files.ErrNoSpace, Class: apierr.NoSpace, Key: "fs.no_space"},

		{Err: files.ErrShareBroken, Class: apierr.ShareUnavailable, Key: "fs.share_unavailable"},
		{Err: files.ErrLinkExpired, Class: apierr.Gone, Key: "link.expired"},
	}
}

// authSentinels is credentials, accounts and the identity flows.
//
// ErrCredentials and ErrSecondFactor are deliberately different classes: the
// first is a refusal and the second is the next step of a flow that is going
// correctly, and rendering them alike would leave an enrolled account unable to
// present its code.
func authSentinels() []apierr.Sentinel {
	return []apierr.Sentinel{
		{Err: auth.ErrRateLimited, Class: apierr.RateLimited, Key: "auth.rate_limited"},
		{Err: auth.ErrAccountDisabled, Class: apierr.AccountDisabled, Key: "auth.account_disabled"},
		{Err: auth.ErrSecondFactor, Class: apierr.AuthRequired, Key: "auth.totp_required"},
		{Err: auth.ErrCredentials, Class: apierr.AuthInvalid, Key: "auth.invalid_credentials"},

		{Err: auth.ErrLastAdmin, Class: apierr.LastAdmin, Key: "admin.last_admin"},
		{Err: auth.ErrWeakPassword, Class: apierr.WeakPassword, Key: "auth.weak_password"},
		{Err: auth.ErrNameTaken, Class: apierr.NameTaken, Key: "admin.name_taken"},
		{Err: auth.ErrNameInvalid, Class: apierr.Unprocessable, Key: "admin.invalid_name"},
		{Err: auth.ErrInvalidQuota, Class: apierr.Unprocessable, Key: "admin.invalid_quota"},
		{Err: auth.ErrRecoverySetSize, Class: apierr.Unprocessable, Key: "auth.recovery_set_size"},

		{Err: auth.ErrNotFound, Class: apierr.NotFound, Key: "admin.not_found"},

		{Err: auth.ErrOIDCLinkTaken, Class: apierr.Conflict, Key: "oidc.link_taken"},
		{Err: auth.ErrNoOIDCLink, Class: apierr.NotFound, Key: "oidc.no_link"},
		{Err: auth.ErrNoOIDCFlow, Class: apierr.FlowUnknown, Key: "oidc.no_flow"},
		{Err: auth.ErrFlowUnknown, Class: apierr.FlowUnknown, Key: "oidc.flow_unknown"},
		{Err: auth.ErrFlowPending, Class: apierr.FlowPending, Key: "oidc.flow_pending"},
		{Err: auth.ErrFlowApproved, Class: apierr.FlowApproved, Key: "oidc.flow_approved"},
		{Err: auth.ErrFlowRateLimited, Class: apierr.FlowTooSoon, Key: "oidc.flow_too_soon"},

		// The key ring is infrastructure: a deployment without one cannot
		// serve, and the caller learns nothing useful from the distinction.
		{Err: auth.ErrNoKeyRing, Class: apierr.Internal, Key: "internal"},
		{Err: auth.ErrKeyVersionMissing, Class: apierr.Internal, Key: "internal"},
		{Err: auth.ErrKeyEnvForbidden, Class: apierr.Internal, Key: "internal"},
		{Err: auth.ErrBadCrockford, Class: apierr.Internal, Key: "internal"},
		{Err: auth.ErrCiphertextTooShort, Class: apierr.Internal, Key: "internal"},
	}
}

// uploadSentinels is the resumable protocol.
//
// TUS keeps its own status vocabulary, which is why several of these classify
// to something the REST adapter renders differently from what TUS will: the
// class is what the error means, and the protocol decides how to say it.
func uploadSentinels() []apierr.Sentinel {
	return []apierr.Sentinel{
		{Err: uploads.ErrDestMissing, Class: apierr.NotFound, Key: "upload.dest_missing"},
		{Err: uploads.ErrNotFound, Class: apierr.NotFound, Key: "upload.no_such_session"},

		{Err: uploads.ErrSessionExpired, Class: apierr.Gone, Key: "upload.session_expired"},
		{Err: uploads.ErrSessionState, Class: apierr.Conflict, Key: "upload.session_state"},
		{Err: uploads.ErrOffsetConflict, Class: apierr.Conflict, Key: "upload.offset_conflict"},
		{Err: uploads.ErrAliasTaken, Class: apierr.Conflict, Key: "upload.alias_taken"},

		{Err: uploads.ErrChecksum, Class: apierr.Unprocessable, Key: "upload.checksum_mismatch"},
		{Err: uploads.ErrVerify, Class: apierr.Unprocessable, Key: "upload.verify_failed"},
		{Err: uploads.ErrIncomplete, Class: apierr.Unprocessable, Key: "upload.incomplete"},
		{Err: uploads.ErrChunkTooSmall, Class: apierr.Unprocessable, Key: "upload.chunk_too_small"},
		{Err: uploads.ErrBadRequest, Class: apierr.Malformed, Key: "upload.bad_request"},

		{Err: uploads.ErrTooLarge, Class: apierr.BodyTooLarge, Key: "upload.too_large"},
		{Err: uploads.ErrFragmented, Class: apierr.LimitExceeded, Key: "upload.too_fragmented"},
		// Both clear as the account's own uploads finish, so they answer 429
		// and a client waits. As 422 they told every client to give up, which
		// is what lost files from a batch that briefly crossed the bound.
		{Err: uploads.ErrExhausted, Class: apierr.ResourceExhausted, Key: "upload.limit_exceeded"},
		{Err: uploads.ErrCacheFull, Class: apierr.ResourceExhausted, Key: "upload.cache_full"},
		{Err: uploads.ErrNoCache, Class: apierr.SubsystemUnavailable, Key: "upload.cache_unavailable"},
	}
}

// previewSentinels is thumbnailing and archive listing.
//
// A worker that died or is busy is a subsystem condition rather than a fault in
// the request: the same request would succeed with a worker free, and telling
// the caller it was malformed would be wrong.
func previewSentinels() []apierr.Sentinel {
	return []apierr.Sentinel{
		{Err: preview.ErrNotArchive, Class: apierr.Unprocessable, Key: "preview.not_an_archive"},
		{Err: preview.ErrUnsupported, Class: apierr.NotImplemented, Key: "preview.unsupported"},
		{Err: preview.ErrNotImplemented, Class: apierr.NotImplemented, Key: "preview.not_implemented"},
		{Err: preview.ErrTooLarge, Class: apierr.LimitExceeded, Key: "preview.too_large"},
		{Err: preview.ErrDecode, Class: apierr.Unprocessable, Key: "preview.decode_failed"},
		{Err: preview.ErrWorkerBusy, Class: apierr.SubsystemUnavailable, Key: "preview.busy"},
		{Err: preview.ErrWorkerDied, Class: apierr.SubsystemUnavailable, Key: "preview.unavailable"},
		{Err: preview.ErrPoolClosed, Class: apierr.SubsystemUnavailable, Key: "preview.unavailable"},
		{Err: preview.ErrProtocol, Class: apierr.Internal, Key: "internal"},
	}
}

// setupSentinels is the first-run gate.
func setupSentinels() []apierr.Sentinel {
	return []apierr.Sentinel{
		{Err: server.ErrSetupClosed, Class: apierr.SetupComplete, Key: "setup.complete"},
		{Err: server.ErrSetupNotIssued, Class: apierr.SetupExpired, Key: "setup.not_issued"},
		{Err: server.ErrSetupToken, Class: apierr.SetupInvalidToken, Key: "setup.invalid_token"},
	}
}
