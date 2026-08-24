// Linux only: it depends on packages that are Linux only.
//go:build linux

package handler

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/heavycaffeiner/stowcloud/go/internal/acl"
	"github.com/heavycaffeiner/stowcloud/go/internal/apierr"
	"github.com/heavycaffeiner/stowcloud/go/internal/upload"
	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
)

// The resumable upload surface.
//
// The status vocabulary here is the protocol's, not this server's, which is
// why this mount has its own mapper rather than the native one: a resumable
// client reads an offset mismatch as a specific code and retries from what the
// response tells it, and folding that into a generic conflict makes it
// indistinguishable from a destination that already exists.
//
// This mount is deliberately outside the body-size limit. The bound on an
// upload is the engine's declared length and the account's reservation, not a
// per-request ceiling, and a limit here would refuse exactly the requests the
// surface exists for.

// The protocol's headers.
const (
	hdrResumable = "Tus-Resumable"
	hdrVersion   = "Tus-Version"
	hdrExtension = "Tus-Extension"
	hdrOffset    = "Upload-Offset"
	hdrLength    = "Upload-Length"
	hdrMetadata  = "Upload-Metadata"
	hdrChecksum  = "Upload-Checksum"
	// hdrChunkSize is this server's own. A session fixes its chunk size at
	// creation, so a configuration change made afterwards cannot break a
	// session already in flight, and a resuming client follows this rather
	// than a value it remembered.
	hdrChunkSize = "Sc-Chunk-Size"
	// hdrRandomAccess is this server's own opt-in for chunks arriving out of
	// order. Without it a chunk has to land at the resumable offset, which is
	// what an ordinary client of this protocol does.
	hdrRandomAccess = "Sc-Random-Access"

	tusVersion = "1.0.0"
	// The extensions this build implements. Advertising one that is accepted
	// and ignored is worse than never claiming it: it turns a real guarantee
	// into a believed one.
	tusExtensions = "creation,creation-with-upload,checksum,termination"

	offsetContentType = "application/offset+octet-stream"
)

// UploadsOptions answers the protocol discovery request.
func UploadsOptions() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		h := w.Header()
		h.Set(hdrResumable, tusVersion)
		h.Set(hdrVersion, tusVersion)
		h.Set(hdrExtension, tusExtensions)
		w.WriteHeader(http.StatusNoContent)
	}
}

// unavailable is what every operation answers when no engine is wired.
//
// A refusal naming the subsystem, rather than a panic or a session id nothing
// backs. A client that receives an id and then cannot use it retries forever.
func unavailable() error {
	return &apierr.RequestError{
		Status:  http.StatusServiceUnavailable,
		Code:    apierr.CodeSubsystemUnavail,
		Message: "resumable uploads are not available in this build",
		Key:     "upload.unavailable",
	}
}

// UploadsCreate answers POST /api/uploads.
func UploadsCreate(d Deps) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		if d.Uploads == nil {
			return unavailable()
		}
		uid, cerr := userOf(r)
		if cerr != nil {
			return cerr
		}
		if verr := requireVersion(r); verr != nil {
			return verr
		}

		dest, derr := uploadDest(r.Header)
		if derr != nil {
			return derr
		}
		vp, perr := vfs.ParseVpath(dest)
		if perr != nil {
			return perr
		}
		resolved, rerr := d.Core.Resolve(uid, vp, acl.Write|acl.Create)
		if rerr != nil {
			return rerr
		}

		total, terr := declaredLength(r.Header)
		if terr != nil {
			return terr
		}
		spec := upload.SessionSpec{
			TotalLen:     total,
			RandomAccess: r.Header.Get(hdrRandomAccess) == "1",
			IfMatch:      r.Header.Get("If-Match"),
		}

		session, serr := d.Uploads.Create(r.Context(), resolved, spec)
		if serr != nil {
			return tusError(serr)
		}

		offset := session.Offset
		// The protocol lets a creation carry the first bytes, which saves a
		// round trip that matters most over a mobile link. Until this read the
		// body, a client using it believed it had uploaded a prefix it had
		// not: the request was answered as created and the bytes were dropped.
		if strings.HasPrefix(r.Header.Get("Content-Type"), offsetContentType) {
			sum, cerr := requestChecksum(r.Header)
			if cerr != nil {
				return cerr
			}
			n, perr := d.Uploads.PatchAt(r.Context(), resolved.Root(), session.ID, uid, 0, r.Body, sum)
			if perr != nil {
				return tusError(perr)
			}
			offset = n
		}

		h := w.Header()
		h.Set(hdrResumable, tusVersion)
		h.Set("Location", "/api/uploads/"+session.ID.String())
		// The offset the server actually reached, not the length of the body
		// it was handed: the two differ when the body overran the declared
		// length, and this is the number the client resumes from.
		h.Set(hdrOffset, strconv.FormatUint(offset, 10))
		w.WriteHeader(http.StatusCreated)
		return nil
	})
}

// UploadsHead answers HEAD /api/uploads/{id}, which is how a client resumes.
func UploadsHead(d Deps) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		if d.Uploads == nil {
			return unavailable()
		}
		uid, cerr := userOf(r)
		if cerr != nil {
			return cerr
		}
		if verr := requireVersion(r); verr != nil {
			return verr
		}

		id, ierr := upload.ParseSessionID(r.PathValue("id"))
		if ierr != nil {
			return tusError(ierr)
		}
		session, err := d.Uploads.Get(r.Context(), id, uid)
		if err != nil {
			return tusError(err)
		}
		// A session the client abandoned is gone as far as this protocol is
		// concerned. The row survives until the sweep takes its part file, but
		// reporting an offset for it would tell a client to resume into
		// something that is already being collected.
		if session.State != upload.StateReceiving {
			return tusError(upload.ErrNotFound)
		}

		h := w.Header()
		h.Set(hdrResumable, tusVersion)
		h.Set(hdrOffset, strconv.FormatUint(session.Offset, 10))
		if session.TotalLen != nil {
			h.Set(hdrLength, strconv.FormatUint(*session.TotalLen, 10))
		}
		h.Set(hdrChunkSize, strconv.FormatUint(session.ChunkSize, 10))
		// A resumable session must never be answered from a cache: a
		// remembered offset is a client writing over bytes it already sent.
		h.Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusNoContent)
		return nil
	})
}

// UploadsPatch answers PATCH /api/uploads/{id}, which is one chunk.
func UploadsPatch(d Deps) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		if d.Uploads == nil {
			return unavailable()
		}
		uid, cerr := userOf(r)
		if cerr != nil {
			return cerr
		}
		if verr := requireVersion(r); verr != nil {
			return verr
		}

		raw := r.Header.Get(hdrOffset)
		if raw == "" {
			return badTusRequest(hdrOffset + " is required")
		}
		off, perr := strconv.ParseUint(raw, 10, 64)
		if perr != nil {
			return badTusRequest(hdrOffset + " is not a number")
		}

		sum, kerr := requestChecksum(r.Header)
		if kerr != nil {
			return kerr
		}

		id, ierr := upload.ParseSessionID(r.PathValue("id"))
		if ierr != nil {
			return tusError(ierr)
		}
		session, gerr := d.Uploads.Get(r.Context(), id, uid)
		if gerr != nil {
			return tusError(gerr)
		}
		root, ok := d.Core.ShareRoot(session.Share)
		if !ok {
			return tusError(upload.ErrNotFound)
		}

		// No size ceiling on the body. The bound is the engine's declared
		// length and the account's reservation; the defence against a client
		// that opens this and stops sending is the read deadline on the
		// connection, which is a different thing from a size cap.
		next, werr := d.Uploads.PatchAt(r.Context(), root, id, uid, off, r.Body, sum)
		if werr != nil {
			return tusError(werr)
		}

		// The chunk that completes the upload publishes it. Without this the
		// bytes reach the part file and stop there: the session stays open, the
		// destination never appears, and a client that sent every byte sees a
		// successful upload of a file that is not in the listing.
		//
		// next is the contiguous prefix, so this is true only once every byte
		// below it has landed. Declared length absent means the client never
		// said how long it is, and no offset can mean "done".
		//
		// Several chunks are in flight at once, and the one that completes the
		// file is whichever fills the last hole, not the last to be sent. Two
		// can see a complete set together, so the losers of that race are
		// answered as the success they are: the bytes they carried are on disk
		// and the file is published. Reporting the second one as an error left
		// the interface showing a failed upload of a file that had in fact
		// arrived.
		if session.TotalLen != nil && next >= *session.TotalLen {
			// Resolved through the share label the session already belongs to,
			// so the permission check looks at the path that publishes rather
			// than one the request could name.
			vp, verr := d.Core.VpathFor(uid, session.Share, session.Dest)
			if verr != nil {
				return tusError(verr)
			}
			resolved, rerr := d.Core.Resolve(uid, vp, acl.Write|acl.Create)
			if rerr != nil {
				return rerr
			}
			if _, ferr := d.Uploads.Finalize(r.Context(), resolved, id); ferr != nil &&
				!errors.Is(ferr, upload.ErrNotFound) {
				return tusError(ferr)
			}
		}

		h := w.Header()
		h.Set(hdrResumable, tusVersion)
		h.Set(hdrOffset, strconv.FormatUint(next, 10))
		w.WriteHeader(http.StatusNoContent)
		return nil
	})
}

// UploadsDelete answers DELETE /api/uploads/{id}, which abandons a session.
func UploadsDelete(d Deps) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		if d.Uploads == nil {
			return unavailable()
		}
		uid, cerr := userOf(r)
		if cerr != nil {
			return cerr
		}
		if verr := requireVersion(r); verr != nil {
			return verr
		}
		id, ierr := upload.ParseSessionID(r.PathValue("id"))
		if ierr != nil {
			return tusError(ierr)
		}
		if err := d.Uploads.Abort(r.Context(), id, uid); err != nil {
			return tusError(err)
		}
		w.Header().Set(hdrResumable, tusVersion)
		w.WriteHeader(http.StatusNoContent)
		return nil
	})
}

// requireVersion refuses a client speaking a version this build does not.
//
// An absent header is accepted on a request that names no version at all,
// because the discovery request is how a client learns which to send.
func requireVersion(r *http.Request) error {
	got := r.Header.Get(hdrResumable)
	if got == "" || got == tusVersion {
		return nil
	}
	return &apierr.RequestError{
		Status:  http.StatusPreconditionFailed,
		Code:    apierr.CodeInvalidRequest,
		Message: "this server speaks upload protocol " + tusVersion,
		Key:     "upload.version_unsupported",
	}
}

// declaredLength reads the total size the client promises to send.
//
// Absent is allowed: the length can be deferred and supplied later, and
// finalizing requires it by then. A malformed one is refused rather than
// treated as absent, because the two mean different things to the reservation.
func declaredLength(h http.Header) (*uint64, error) {
	raw := h.Get(hdrLength)
	if raw == "" {
		return nil, nil
	}
	n, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return nil, badTusRequest(hdrLength + " is not a number")
	}
	return &n, nil
}

// uploadDest builds the destination path from the metadata header.
//
// The protocol has nowhere else to carry it, and a query parameter would put
// the path in every access log. The header names a destination directory and a
// leaf separately: neither alone is a target, because the directory is not a
// file and the leaf has no share label, which is the first segment resolution
// requires.
func uploadDest(h http.Header) (string, error) {
	meta := parseMetadata(h.Get(hdrMetadata))
	dir := meta["dest"]
	leaf := meta["relativePath"]
	if leaf == "" {
		leaf = meta["filename"]
	}
	if leaf == "" {
		return "", badTusRequest(hdrMetadata + " must carry a filename or a relativePath")
	}
	if dir == "" {
		return leaf, nil
	}
	return strings.TrimSuffix(dir, "/") + "/" + leaf, nil
}

// parseMetadata reads the comma-separated pairs the metadata header carries,
// each a key, a space, and the value in base64.
//
// A pair this cannot decode is dropped rather than failing the request: the
// header is extensible and a client may send keys this build has no use for,
// and the two keys that matter are checked by their absence above.
func parseMetadata(raw string) map[string]string {
	out := map[string]string{}
	if raw == "" {
		return out
	}
	for _, pair := range strings.Split(raw, ",") {
		key, b64, ok := strings.Cut(strings.TrimSpace(pair), " ")
		if !ok {
			// A bare key with no value is a legal way to say the key is
			// present and empty.
			out[strings.TrimSpace(pair)] = ""
			continue
		}
		value, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
		if err != nil {
			continue
		}
		out[key] = string(value)
	}
	return out
}

// requestChecksum reads the digest the client says the chunk has.
//
// A malformed value, or one naming an algorithm this build cannot verify, is
// refused rather than ignored. The extension is advertised, so accepting the
// header and not comparing anything is the failure worth avoiding: the
// client's integrity check passes without anything having been checked.
func requestChecksum(h http.Header) (*upload.Checksum, error) {
	raw := h.Get(hdrChecksum)
	if raw == "" {
		return nil, nil
	}
	sum, err := upload.ParseChecksum(raw)
	if err != nil {
		return nil, badTusRequest(hdrChecksum + " is malformed or names an algorithm this build cannot verify")
	}
	return &sum, nil
}

func badTusRequest(msg string) error {
	return &apierr.RequestError{
		Status:  http.StatusBadRequest,
		Code:    apierr.CodeInvalidRequest,
		Message: apierr.Message(msg),
		Key:     "upload.bad_request",
	}
}

// tusError maps an engine refusal onto this protocol's status vocabulary.
//
// It is separate from the native mapper because the vocabularies differ where
// it matters most: a chunk that arrived at the wrong offset has its own status
// here, and a client reads it as "resume from what the response says" rather
// than as a destination conflict.
func tusError(err error) error {
	switch {
	case errors.Is(err, upload.ErrOffsetConflict):
		return &apierr.RequestError{
			Status:  http.StatusConflict,
			Code:    apierr.CodeFsConflict,
			Message: "the chunk did not arrive at the resumable offset",
			Key:     "upload.offset_conflict",
		}
	case errors.Is(err, upload.ErrTooLarge):
		return &apierr.RequestError{
			Status:  http.StatusRequestEntityTooLarge,
			Code:    apierr.CodeLimitExceeded,
			Message: "the write goes past the declared length",
			Key:     "upload.past_declared_length",
		}
	case errors.Is(err, upload.ErrChunkTooSmall):
		return &apierr.RequestError{
			Status:  http.StatusBadRequest,
			Code:    apierr.CodeInvalidRequest,
			Message: "the chunk is below this session's minimum size",
			Key:     "upload.chunk_too_small",
		}
	case errors.Is(err, upload.ErrChecksum):
		// The protocol's own status for a chunk whose digest did not match, so
		// a client can tell it apart from a rejected request and resend the
		// same chunk rather than restarting.
		return &apierr.RequestError{
			Status:  460,
			Code:    apierr.CodeInvalidRequest,
			Message: "the chunk does not match the checksum sent with it",
			Key:     "upload.checksum_mismatch",
		}
	case errors.Is(err, upload.ErrNotFound):
		// An unknown session and one belonging to somebody else answer
		// identically, so a stranger cannot learn which ids exist.
		return &apierr.RequestError{
			Status:  http.StatusNotFound,
			Code:    apierr.CodeFsNotFound,
			Message: "no such upload session",
			Key:     "upload.not_found",
		}
	case errors.Is(err, upload.ErrBadRequest):
		return badTusRequest(fmt.Sprintf("the request is malformed: %v", err))
	}
	// Everything else is a domain error the native mapper already knows: a
	// permission refusal, a precondition, a full disk, a quota.
	return err
}
