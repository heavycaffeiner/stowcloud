//go:build linux

// Resumable uploads.
//
// The protocol carries its state in headers rather than a body, so these
// handlers read and write headers and the body is the bytes themselves. A
// chunk that arrives at the wrong offset is refused with the offset the server
// actually holds, which is what lets a client resume rather than start again.
package lifecycle

import (
	"errors"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/apierr"
	"github.com/heavycaffeiner/stowcloud/go/engine/http/handler"
	"github.com/heavycaffeiner/stowcloud/go/engine/http/server"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/upload"
)

// uploadsDiscover answers what this server supports, before any credential.
//
// A client asks this to find out whether resumable uploads exist here at all,
// which is why the route is public: it precedes having anything to present.
func (e *Engine) uploadsDiscover(c *fiber.Ctx) error {
	e.setTusHeaders(c)
	c.Set(fiber.HeaderAllow, "OPTIONS, POST")
	return c.SendStatus(fiber.StatusNoContent)
}

// uploadsDiscoverOne is the same for a session that already exists.
func (e *Engine) uploadsDiscoverOne(c *fiber.Ctx) error {
	e.setTusHeaders(c)
	c.Set(fiber.HeaderAllow, "OPTIONS, HEAD, PATCH, DELETE")
	return c.SendStatus(fiber.StatusNoContent)
}

// setTusHeaders states the protocol this server speaks.
//
// The version is advertised on every response, not only on discovery: a
// client that reaches a session route directly still has to be able to tell
// it is talking to a server that speaks the protocol.
func (e *Engine) setTusHeaders(c *fiber.Ctx) {
	c.Set(handler.TusResumable, handler.TusProtocolVersion)
	c.Set(handler.TusVersion, handler.TusProtocolVersion)
	c.Set(handler.TusExtension, tusExtensions)

	// Which digests a chunk may carry. Advertised rather than assumed: this
	// server does not do sha256, and a client that guessed it would compute a
	// digest for every chunk and have every one refused.
	c.Set(tusChecksumAlgorithm, checksumAlgorithms())
}

// tusChecksumAlgorithm is where the protocol lists the digests a server
// accepts.
const tusChecksumAlgorithm = "Tus-Checksum-Algorithm"

// checksumAlgorithms renders what the upload engine actually implements, so
// the advertisement cannot drift from the parser.
func checksumAlgorithms() string {
	algos := upload.Algorithms()
	names := make([]string, 0, len(algos))
	for _, a := range algos {
		names = append(names, a.String())
	}
	return strings.Join(names, ",")
}

// tusExtensions names what this build actually implements. Advertising one it
// does not would make a client take a path that then fails.
const tusExtensions = "creation,creation-defer-length,termination,checksum"

// uploadsCreate opens a session.
func (e *Engine) uploadsCreate(c *fiber.Ctx) error {
	owner, ok := ownerOf(c)
	if !ok {
		return refuse(c, apierr.Classified{Class: apierr.AuthRequired})
	}
	engine, ok := e.uploads(c)
	if !ok {
		return refuse(c, apierr.Classified{Class: apierr.SubsystemUnavailable})
	}
	e.setTusHeaders(c)

	if err := handler.CheckResumable(c.Get(handler.TusResumable)); err != nil {
		return refuseTus(c, err)
	}

	length, err := handler.ParseLength(c.Get(handler.UploadLength), c.Get(handler.UploadDefer))
	if err != nil {
		return refuseTus(c, err)
	}

	// The destination comes from the metadata header, which is where the
	// protocol puts everything that is not a length or an offset.
	meta, err := handler.ParseMetadata(c.Get(handler.UploadMetadata), tusMetadataMaxPairs)
	if err != nil {
		return refuseTus(c, err)
	}

	// The header names a destination directory and a leaf separately, and
	// neither alone is a target: the directory is not a file, and the leaf has
	// no share label, which is the first segment resolution requires. The leaf
	// prefers the relative path, which is what makes a folder upload land in
	// its own subdirectory rather than flattening into the destination.
	dest := meta["dest"]
	leaf := meta["relativePath"]
	if leaf == "" {
		leaf = meta["filename"]
	}
	if leaf == "" {
		return refuse(c, apierr.Classified{Class: apierr.Unprocessable})
	}
	if dest != "" {
		dest = strings.TrimSuffix(dest, "/") + "/" + leaf
	} else {
		dest = leaf
	}

	r, rerr := e.resolve(owner, dest, acl.Write|acl.Create)
	if rerr != nil {
		return fail(c, rerr)
	}

	spec := upload.SessionSpec{
		IfMatch:      c.Get(fiber.HeaderIfMatch),
		Meta:         uploadMetaOf(meta),
		RandomAccess: c.Get(handler.ScRandomAccess) == "1",
	}
	if !length.Deferred {
		total := length.Value
		spec.TotalLen = &total
	}

	sess, cerr := engine.Create(c.UserContext(), r, spec)
	if cerr != nil {
		return fail(c, cerr)
	}

	// The location is where every later request for this session goes. A
	// client that had to build it would be reimplementing the route table.
	c.Set(fiber.HeaderLocation, server.Base+"/uploads/"+sess.ID.String())
	c.Set(handler.UploadOffset, strconv.FormatUint(sess.Offset, 10))
	return c.SendStatus(fiber.StatusCreated)
}

// tusMetadataMaxPairs bounds the metadata header. It is client-supplied and
// parsed before anything is authorised, so an unbounded one is work a caller
// can ask for without holding anything.
const tusMetadataMaxPairs = 32

// uploadsStatus reports how far a session has got.
//
// This is what a resume asks first: the offset it answers is the byte the
// next chunk has to start at.
func (e *Engine) uploadsStatus(c *fiber.Ctx) error {
	owner, ok := ownerOf(c)
	if !ok {
		return refuse(c, apierr.Classified{Class: apierr.AuthRequired})
	}
	engine, ok := e.uploads(c)
	if !ok {
		return refuse(c, apierr.Classified{Class: apierr.SubsystemUnavailable})
	}
	e.setTusHeaders(c)

	if err := handler.CheckResumable(c.Get(handler.TusResumable)); err != nil {
		return refuseTus(c, err)
	}
	id, ok := sessionIDOf(c)
	if !ok {
		return notFound(c)
	}

	sess, err := engine.Get(c.UserContext(), id, owner)
	if err != nil {
		return fail(c, err)
	}

	// A session that has finished, been aborted or expired is not a session
	// to resume. Reporting an offset for one tells a client to carry on
	// sending into an upload that will never publish, and the row survives an
	// abort by design so the sweep can claim the part file.
	if terminal, _ := handler.TerminalUploadState(sess.State.StateName()); terminal {
		return notFound(c)
	}

	// Caching a session's progress is exactly wrong: the value changes with
	// every chunk, and a cached one sends the next chunk to a stale offset.
	c.Set(fiber.HeaderCacheControl, "no-store")
	c.Set(handler.UploadOffset, strconv.FormatUint(sess.Offset, 10))
	if sess.TotalLen != nil {
		c.Set(handler.UploadLength, strconv.FormatUint(*sess.TotalLen, 10))
	} else {
		c.Set(handler.UploadDefer, "1")
	}
	return c.SendStatus(fiber.StatusOK)
}

// uploadsPatch takes one chunk.
func (e *Engine) uploadsPatch(c *fiber.Ctx) error {
	owner, ok := ownerOf(c)
	if !ok {
		return refuse(c, apierr.Classified{Class: apierr.AuthRequired})
	}
	engine, ok := e.uploads(c)
	if !ok {
		return refuse(c, apierr.Classified{Class: apierr.SubsystemUnavailable})
	}
	e.setTusHeaders(c)

	if err := handler.CheckResumable(c.Get(handler.TusResumable)); err != nil {
		return refuseTus(c, err)
	}
	id, ok := sessionIDOf(c)
	if !ok {
		return notFound(c)
	}

	// The protocol fixes the body type, and a request sending something else
	// is describing different content from what it carries.
	if ct := c.Get(fiber.HeaderContentType); ct != tusChunkType {
		return refuse(c, apierr.Classified{Class: apierr.Unprocessable})
	}

	offset, oerr := handler.ParseOffset(c.Get(handler.UploadOffset))
	if oerr != nil {
		return refuseTus(c, oerr)
	}

	sum, serr := chunkChecksum(c.Get(handler.UploadChecksum))
	if serr != nil {
		return refuseTus(c, serr)
	}

	sess, gerr := engine.Get(c.UserContext(), id, owner)
	if gerr != nil {
		return fail(c, gerr)
	}
	root, ok := e.Core.ShareRoot(sess.Share)
	if !ok {
		return fail(c, gerr)
	}

	// The caller's own id, which is what the engine checks the session
	// against. Passing sess.User instead is measured to change no answer,
	// because Get above already refused a session belonging to somebody else,
	// so the two values are equal by the time this runs. The caller's is used
	// anyway: it is the one the request proved, and it stays correct if Get
	// ever stops checking.
	next, perr := engine.PatchAt(c.UserContext(), root, id, owner,
		offset, requestBodyReader(c), sum)
	if perr != nil {
		return failUpload(c, perr)
	}

	// The last chunk publishes. Without this the bytes sit in a part file
	// nobody can reach: every chunk succeeds, the offset reaches the length,
	// and the destination does not exist. The protocol has no separate
	// finish request, so the arrival of the final byte is the signal.
	if sess.TotalLen != nil && next >= *sess.TotalLen {
		if ferr := e.publishUpload(c, engine, sess, id, owner); ferr != nil {
			return ferr
		}
	}

	c.Set(handler.UploadOffset, strconv.FormatUint(next, 10))
	return c.SendStatus(fiber.StatusNoContent)
}

// publishUpload moves a completed session to its destination.
//
// The destination is resolved fresh rather than carried from the create: the
// account's access is checked at the moment of the write, so a grant revoked
// during a long transfer does not publish anyway.
func (e *Engine) publishUpload(
	c *fiber.Ctx, engine *upload.Engine, sess upload.Session,
	id upload.SessionID, owner core.UserID,
) error {
	// The destination is the vpath under the label the caller's own grant
	// projects the share as, not the share's registered name: the two differ
	// whenever a grant is labeled, and resolving under the registered name
	// answers not-found for the very upload the client just completed.
	dest, err := e.Core.VpathFor(owner, sess.Share, sess.Dest.Share())
	if err != nil {
		return fail(c, core.ErrNotFound)
	}

	r, err := e.Core.Resolve(owner, dest, acl.Write|acl.Create)
	if err != nil {
		return fail(c, err)
	}
	if _, ferr := engine.Finalize(c.UserContext(), r, id); ferr != nil {
		return fail(c, ferr)
	}
	return nil
}

// tusChunkType is the one body type a chunk may carry.
const tusChunkType = "application/offset+octet-stream"

// chunkChecksum reads the optional digest header.
func chunkChecksum(header string) (*upload.Checksum, error) {
	if header == "" {
		return nil, nil
	}
	sum, err := upload.ParseChecksum(header)
	if err != nil {
		return nil, err
	}
	return &sum, nil
}

// uploadsAbort discards a session and its part file.
func (e *Engine) uploadsAbort(c *fiber.Ctx) error {
	owner, ok := ownerOf(c)
	if !ok {
		return refuse(c, apierr.Classified{Class: apierr.AuthRequired})
	}
	engine, ok := e.uploads(c)
	if !ok {
		return refuse(c, apierr.Classified{Class: apierr.SubsystemUnavailable})
	}
	e.setTusHeaders(c)

	if err := handler.CheckResumable(c.Get(handler.TusResumable)); err != nil {
		return refuseTus(c, err)
	}
	id, ok := sessionIDOf(c)
	if !ok {
		return notFound(c)
	}

	if err := engine.Abort(c.UserContext(), id, owner); err != nil {
		return fail(c, err)
	}
	return c.SendStatus(fiber.StatusNoContent)
}

// uploads reports the resumable engine, which a deployment may not have.
//
// Its absence is a degradation rather than a fault: a spool directory that
// could not be opened costs resumable transfers and nothing else, and the
// rest of the server still serves.
func (e *Engine) uploads(c *fiber.Ctx) (*upload.Engine, bool) {
	_ = c
	return e.Upload, e.Upload != nil
}

// uploadSettingsRequest is the administrator's chunk configuration.
//
// Pointers throughout: a patch names what it changes, so a screen saving the
// chunk sizes must not silently turn the spool off, and one toggling the
// spool must not reset the sizes to whatever it last rendered.
type uploadSettingsRequest struct {
	ChunkMin     *int64 `json:"chunk_min"`
	ChunkDefault *int64 `json:"chunk_default"`
	CacheEnabled *bool  `json:"cache_enabled"`
}

// uploadSettingsPatch applies the server-global chunk bounds and the spool
// switch.
//
// Its own route rather than a section of the settings document: both values
// live in the upload engine's tables, and it owns the clamping that keeps a
// live bound from falling under the compiled-in floor.
func (e *Engine) uploadSettingsPatch(c *fiber.Ctx) error {
	if _, ok, written := e.admin(c); !ok {
		return written
	}
	engine, ok := e.uploads(c)
	if !ok {
		// No spool, so there is nothing to configure. Reported rather than
		// silently accepted: a save that stored nothing must not come back
		// looking like one that did.
		return refuse(c, apierr.Classified{Class: apierr.Unprocessable})
	}

	var req uploadSettingsRequest
	if err := decodeBody(c, &req); err != nil {
		return refuse(c, apierr.Classified{Class: apierr.Malformed})
	}

	// Narrowed through the checked helper rather than converted: a negative
	// bound cannot be a byte count, and an unchecked conversion would turn
	// one into an enormous ceiling.
	var minBytes, defaultBytes *uint64
	if req.ChunkMin != nil {
		v, err := num.Narrow[uint64](*req.ChunkMin)
		if err != nil {
			return refuse(c, apierr.Classified{Class: apierr.Unprocessable})
		}
		minBytes = &v
	}
	if req.ChunkDefault != nil {
		v, err := num.Narrow[uint64](*req.ChunkDefault)
		if err != nil {
			return refuse(c, apierr.Classified{Class: apierr.Unprocessable})
		}
		defaultBytes = &v
	}
	if minBytes != nil || defaultBytes != nil {
		if err := engine.ApplySettings(c.UserContext(), minBytes, defaultBytes); err != nil {
			return fail(c, err)
		}
	}

	// After the bounds, so a request carrying both reports the spool state it
	// ends on rather than the one it started from.
	if req.CacheEnabled != nil && *req.CacheEnabled != engine.CacheEnabled() {
		if err := engine.SetCacheEnabled(c.UserContext(), *req.CacheEnabled); err != nil {
			return fail(c, err)
		}
	}

	storedMin, storedDefault := engine.Settings().Snapshot()
	// Both are bounded by the chunk ceiling, far inside the signed range, so
	// a narrowing failure here is a stored value nothing should have written.
	viewMin, minErr := num.Narrow[int64](storedMin)
	viewDefault, defErr := num.Narrow[int64](storedDefault)
	if minErr != nil || defErr != nil {
		return failKnown(c, minErr)
	}
	return writeJSON(c, fiber.StatusOK, handler.UploadSettingsView{
		ChunkMin:       viewMin,
		ChunkDefault:   viewDefault,
		CacheEnabled:   engine.CacheEnabled(),
		CacheAvailable: engine.CacheAvailable(),
	})
}

// sessionIDOf reads the path's session id.
func sessionIDOf(c *fiber.Ctx) (upload.SessionID, bool) {
	id, err := upload.ParseSessionID(c.Params("id"))
	if err != nil {
		return upload.SessionID{}, false
	}
	return id, true
}

// refuseTus answers a protocol-level refusal.
//
// A version mismatch is its own status, because the client has to know to
// speak a different version rather than to fix its request.
func refuseTus(c *fiber.Ctx, err error) error {
	if errors.Is(err, handler.ErrTusVersion) {
		return refuse(c, apierr.Classified{Class: apierr.Precondition})
	}
	return refuse(c, apierr.Classified{Class: apierr.Malformed})
}

// failUpload renders a chunk failure, with the protocol's own status for a
// digest that did not match.
//
// The shared mapper has no code for it: 460 is this protocol's invention and
// no general classifier would produce it, while a client watching for it
// retries the chunk rather than the whole transfer.
func failUpload(c *fiber.Ctx, err error) error {
	if errors.Is(err, upload.ErrChecksum) {
		return writeJSON(c, handler.StatusChecksumMismatch, map[string]string{
			"error": "checksum_mismatch",
		})
	}
	return fail(c, err)
}

// uploadMetaOf lifts the file's own metadata out of the header map.
//
// mtime matters more than it looks: the finalizer stamps the published file
// with it, and a sync client compares that stamp against its local copy on
// the next pass. A stamp that cannot be parsed costs the upload the courtesy,
// never the bytes.
func uploadMetaOf(meta map[string]string) upload.Meta {
	out := upload.Meta{
		Filename:     meta["filename"],
		RelativePath: meta["relativePath"],
		Mime:         meta["filetype"],
	}
	if raw := meta["mtime"]; raw != "" {
		if ns, err := strconv.ParseInt(raw, 10, 64); err == nil {
			out.MtimeNs = &ns
		}
	}
	return out
}
