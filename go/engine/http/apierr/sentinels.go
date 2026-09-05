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
	"github.com/heavycaffeiner/stowcloud/go/engine/service/auth"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/preview"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/upload"
)

func sentinels() []classifier {
	out := make([]classifier, 0, 64)
	out = append(out, coreSentinels()...)
	out = append(out, authSentinels()...)
	out = append(out, uploadSentinels()...)
	out = append(out, previewSentinels()...)
	out = append(out, storeSentinels()...)
	return out
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
		{core.ErrWritesBlocked, SubsystemUnavailable, "store.writes_blocked"},
		{core.ErrGrantMalformed, Unprocessable, "admin.invalid_grant"},
		{core.ErrGrantAlreadyExists, Conflict, "admin.grant_exists"},
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
		{core.ErrDenied, Denied, "fs.denied"},
		{core.ErrNotFound, NotFound, "fs.not_found"},

		{core.ErrExists, Exists, "fs.exists"},
		{core.ErrNotEmpty, NotEmpty, "fs.not_empty"},
		{core.ErrCrossShare, Conflict, "fs.cross_share"},
		{core.ErrTrashDisabled, Conflict, "fs.trash_disabled"},
		{core.ErrConflict, Conflict, "fs.conflict"},

		{core.ErrPrecondition, Precondition, "fs.precondition_failed"},

		// A well-formed request the target's own state refuses, which is
		// neither a race nor a missing precondition header, so it takes
		// Unprocessable rather than Conflict or Precondition.
		{core.ErrUnprocessable, Unprocessable, "fs.unprocessable"},

		{core.ErrQuotaExceeded, NoSpace, "fs.quota_exceeded"},
		{core.ErrNoSpace, NoSpace, "fs.no_space"},

		{core.ErrShareBroken, ShareUnavailable, "fs.share_unavailable"},
		{core.ErrLinkExpired, Gone, "link.expired"},
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
		{upload.ErrDestMissing, NotFound, "upload.dest_missing"},
		{upload.ErrNotFound, NotFound, "upload.no_such_session"},

		{upload.ErrSessionExpired, Gone, "upload.session_expired"},
		{upload.ErrSessionState, Conflict, "upload.session_state"},
		{upload.ErrOffsetConflict, Conflict, "upload.offset_conflict"},
		{upload.ErrAliasTaken, Conflict, "upload.alias_taken"},

		{upload.ErrChecksum, Unprocessable, "upload.checksum_mismatch"},
		{upload.ErrVerify, Unprocessable, "upload.verify_failed"},
		{upload.ErrIncomplete, Unprocessable, "upload.incomplete"},
		{upload.ErrChunkTooSmall, Unprocessable, "upload.chunk_too_small"},
		{upload.ErrUnknownAlgo, Unprocessable, "upload.unknown_algorithm"},
		{upload.ErrBadRequest, Malformed, "upload.bad_request"},

		{upload.ErrTooLarge, BodyTooLarge, "upload.too_large"},
		{upload.ErrFragmented, LimitExceeded, "upload.too_fragmented"},
		// Both clear as the account's own uploads finish, so they answer 429
		// and a client waits. As 422 they told every client to give up, which
		// is what lost files from a batch that briefly crossed the bound.
		{upload.ErrExhausted, ResourceExhausted, "upload.limit_exceeded"},
		{upload.ErrCacheFull, ResourceExhausted, "upload.cache_full"},
		{upload.ErrNoCache, SubsystemUnavailable, "upload.cache_unavailable"},
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
