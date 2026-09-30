// Linux only, because it classifies errors from services that are Linux only.
//go:build linux

// The sentinel table: every service error this build recognises, and what it
// means to a protocol.
//
// One table, consulted once, by Classify. The old tree had this ladder three
// times and the copies disagreed, which is a disclosure difference nobody
// chose: the same refusal became 404 on one surface and 403 on another.
//
// Order is specific before general, because errors.Is matches a wrapped error
// and two sentinels can both match. A test asserts every sentinel the service
// packages export appears here, so a new one fails the build rather than
// silently classifying as Internal.

package apierr

import (
	"github.com/heavycaffeiner/stowcloud/backend/internal/feature/auth"
	"github.com/heavycaffeiner/stowcloud/backend/internal/feature/oidc"
	"github.com/heavycaffeiner/stowcloud/backend/internal/feature/preview"
	"github.com/heavycaffeiner/stowcloud/backend/internal/files"
	"github.com/heavycaffeiner/stowcloud/backend/internal/uploads"
)

func sentinels() []classifier {
	out := make([]classifier, 0, 64)
	out = append(out, coreSentinels()...)
	out = append(out, authSentinels()...)
	out = append(out, uploadSentinels()...)
	out = append(out, previewSentinels()...)
	out = append(out, storeSentinels()...)
	out = append(out, oidcSentinels()...)
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
func oidcSentinels() []classifier {
	return []classifier{
		{oidc.ErrDiscovery, OIDCProviderUnavailable, "oidc.provider_unavailable"},
		{oidc.ErrProvider, OIDCProviderUnavailable, "oidc.provider_unavailable"},
		{oidc.ErrNoTrustAnchors, OIDCProviderUnavailable, "oidc.provider_unavailable"},
		{oidc.ErrAddressBlocked, OIDCProviderUnavailable, "oidc.provider_unavailable"},
		{oidc.ErrTokenVerify, AuthInvalid, "auth.invalid_credentials"},
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
func storeSentinels() []classifier {
	return []classifier{
		{files.ErrWritesBlocked, SubsystemUnavailable, "store.writes_blocked"},
		{files.ErrGrantMalformed, Unprocessable, "admin.invalid_grant"},
		{files.ErrGrantAlreadyExists, Conflict, "admin.grant_exists"},
	}
}

// coreSentinels is the filesystem domain.
//
// ErrDenied and ErrNotFound classify separately here and the visibility rule
// folds them together where the caller must not learn which it was. Doing the
// fold in the table instead would make a denial unreportable even on a surface
// the caller reached legitimately.
func coreSentinels() []classifier {
	return []classifier{
		{files.ErrDenied, Denied, "fs.denied"},
		{files.ErrNotFound, NotFound, "fs.not_found"},

		{files.ErrExists, Exists, "fs.exists"},
		{files.ErrNotEmpty, NotEmpty, "fs.not_empty"},
		{files.ErrCrossShare, Conflict, "fs.cross_share"},
		{files.ErrTrashDisabled, Conflict, "fs.trash_disabled"},
		{files.ErrConflict, Conflict, "fs.conflict"},

		{files.ErrPrecondition, Precondition, "fs.precondition_failed"},

		// A well-formed request the target's own state refuses, which is
		// neither a race nor a missing precondition header, so it takes
		// Unprocessable rather than Conflict or Precondition.
		{files.ErrUnprocessable, Unprocessable, "fs.unprocessable"},

		{files.ErrQuotaExceeded, NoSpace, "fs.quota_exceeded"},
		{files.ErrNoSpace, NoSpace, "fs.no_space"},

		{files.ErrShareBroken, ShareUnavailable, "fs.share_unavailable"},
		{files.ErrLinkExpired, Gone, "link.expired"},
	}
}

// authSentinels is credentials, accounts and the identity flows.
//
// ErrCredentials and ErrSecondFactor are deliberately different classes: the
// first is a refusal and the second is the next step of a flow that is going
// correctly, and rendering them alike would leave an enrolled account unable to
// present its code.
func authSentinels() []classifier {
	return []classifier{
		{auth.ErrRateLimited, RateLimited, "auth.rate_limited"},
		{auth.ErrAccountDisabled, AccountDisabled, "auth.account_disabled"},
		{auth.ErrSecondFactor, AuthRequired, "auth.totp_required"},
		{auth.ErrCredentials, AuthInvalid, "auth.invalid_credentials"},

		{auth.ErrLastAdmin, LastAdmin, "admin.last_admin"},
		{auth.ErrWeakPassword, WeakPassword, "auth.weak_password"},
		{auth.ErrNameTaken, NameTaken, "admin.name_taken"},
		{auth.ErrNameInvalid, Unprocessable, "admin.invalid_name"},
		{auth.ErrInvalidQuota, Unprocessable, "admin.invalid_quota"},
		{auth.ErrRecoverySetSize, Unprocessable, "auth.recovery_set_size"},

		{auth.ErrNotFound, NotFound, "admin.not_found"},

		{auth.ErrOIDCLinkTaken, Conflict, "oidc.link_taken"},
		{auth.ErrNoOIDCLink, NotFound, "oidc.no_link"},
		{auth.ErrNoOIDCFlow, FlowUnknown, "oidc.no_flow"},
		{auth.ErrFlowUnknown, FlowUnknown, "oidc.flow_unknown"},
		{auth.ErrFlowPending, FlowPending, "oidc.flow_pending"},
		{auth.ErrFlowApproved, FlowApproved, "oidc.flow_approved"},
		{auth.ErrFlowRateLimited, FlowTooSoon, "oidc.flow_too_soon"},

		// The key ring is infrastructure: a deployment without one cannot
		// serve, and the caller learns nothing useful from the distinction.
		{auth.ErrNoKeyRing, Internal, "internal"},
		{auth.ErrKeyVersionMissing, Internal, "internal"},
		{auth.ErrKeyEnvForbidden, Internal, "internal"},
		{auth.ErrBadCrockford, Internal, "internal"},
		{auth.ErrCiphertextTooShort, Internal, "internal"},
	}
}

// uploadSentinels is the resumable protocol.
//
// TUS keeps its own status vocabulary, which is why several of these classify
// to something the REST adapter renders differently from what TUS will: the
// class is what the error means, and the protocol decides how to say it.
func uploadSentinels() []classifier {
	return []classifier{
		{uploads.ErrDestMissing, NotFound, "upload.dest_missing"},
		{uploads.ErrNotFound, NotFound, "upload.no_such_session"},

		{uploads.ErrSessionExpired, Gone, "upload.session_expired"},
		{uploads.ErrSessionState, Conflict, "upload.session_state"},
		{uploads.ErrOffsetConflict, Conflict, "upload.offset_conflict"},
		{uploads.ErrAliasTaken, Conflict, "upload.alias_taken"},

		{uploads.ErrChecksum, Unprocessable, "upload.checksum_mismatch"},
		{uploads.ErrVerify, Unprocessable, "upload.verify_failed"},
		{uploads.ErrIncomplete, Unprocessable, "upload.incomplete"},
		{uploads.ErrChunkTooSmall, Unprocessable, "upload.chunk_too_small"},
		{uploads.ErrBadRequest, Malformed, "upload.bad_request"},

		{uploads.ErrTooLarge, BodyTooLarge, "upload.too_large"},
		{uploads.ErrFragmented, LimitExceeded, "upload.too_fragmented"},
		// Both clear as the account's own uploads finish, so they answer 429
		// and a client waits. As 422 they told every client to give up, which
		// is what lost files from a batch that briefly crossed the bound.
		{uploads.ErrExhausted, ResourceExhausted, "upload.limit_exceeded"},
		{uploads.ErrCacheFull, ResourceExhausted, "upload.cache_full"},
		{uploads.ErrNoCache, SubsystemUnavailable, "upload.cache_unavailable"},
	}
}

// previewSentinels is thumbnailing and archive listing.
//
// A worker that died or is busy is a subsystem condition rather than a fault in
// the request: the same request would succeed with a worker free, and telling
// the caller it was malformed would be wrong.
func previewSentinels() []classifier {
	return []classifier{
		{preview.ErrNotArchive, Unprocessable, "preview.not_an_archive"},
		{preview.ErrUnsupported, NotImplemented, "preview.unsupported"},
		{preview.ErrNotImplemented, NotImplemented, "preview.not_implemented"},
		{preview.ErrTooLarge, LimitExceeded, "preview.too_large"},
		{preview.ErrDecode, Unprocessable, "preview.decode_failed"},
		{preview.ErrWorkerBusy, SubsystemUnavailable, "preview.busy"},
		{preview.ErrWorkerDied, SubsystemUnavailable, "preview.unavailable"},
		{preview.ErrPoolClosed, SubsystemUnavailable, "preview.unavailable"},
		{preview.ErrProtocol, Internal, "internal"},
	}
}
