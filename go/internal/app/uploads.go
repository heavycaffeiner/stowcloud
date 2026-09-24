//go:build linux

// Resumable uploads.
//
// The protocol carries its state in headers rather than a body, so these
// handlers read and write headers and the body is the bytes themselves. A
// chunk that arrives at the wrong offset is refused with the offset the server
// actually holds, which is what lets a client resume rather than start again.
package app

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/heavycaffeiner/stowcloud/go/internal/feature/files"
	"github.com/heavycaffeiner/stowcloud/go/internal/feature/shares/acl"
	"github.com/heavycaffeiner/stowcloud/go/internal/feature/uploads"
	num "github.com/heavycaffeiner/stowcloud/go/internal/platform/number"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/apierr"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/handler"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/server"
	"github.com/stowcloud/transfer"
)

// uploadsDiscover answers what this server supports, before any credential.
//
// A client asks this to find out whether resumable uploads exist here at all,
// which is why the route is public: it precedes having anything to present.
func (e *Engine) uploadsDiscover(c *gin.Context) {
	e.setTusHeaders(c)
	c.Header("Allow", "OPTIONS, POST")
	c.Status(http.StatusNoContent)
}

// uploadsDiscoverOne is the same for a session that already exists.
func (e *Engine) uploadsDiscoverOne(c *gin.Context) {
	e.setTusHeaders(c)
	c.Header("Allow", "OPTIONS, HEAD, PATCH, DELETE")
	c.Status(http.StatusNoContent)
}

// setTusHeaders states the protocol this server speaks.
//
// The version is advertised on every response, not only on discovery: a
// client that reaches a session route directly still has to be able to tell
// it is talking to a server that speaks the protocol.
func (e *Engine) setTusHeaders(c *gin.Context) {
	c.Header(handler.TusResumable, handler.TusProtocolVersion)
	c.Header(handler.TusVersion, handler.TusProtocolVersion)
	c.Header(handler.TusExtension, tusExtensions)

	// Which digests a chunk may carry. Advertised rather than assumed: this
	// server does not do sha256, and a client that guessed it would compute a
	// digest for every chunk and have every one refused.
	c.Header(tusChecksumAlgorithm, checksumAlgorithms())
}

// tusChecksumAlgorithm is where the protocol lists the digests a server
// accepts.
const tusChecksumAlgorithm = "Tus-Checksum-Algorithm"

// checksumAlgorithms renders what the upload engine actually implements, so
// the advertisement cannot drift from the parser.
func checksumAlgorithms() string {
	algos := transfer.Algorithms()
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
func (e *Engine) uploadsCreate(c *gin.Context) {
	owner, ok := ownerOf(c)
	if !ok {
		refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	engine, ok := e.uploads(c)
	if !ok {
		refuse(c, apierr.Classified{Class: apierr.SubsystemUnavailable})
		return
	}
	e.setTusHeaders(c)
	if err := handler.CheckResumable(c.GetHeader(handler.TusResumable)); err != nil {
		refuseTus(c, err)
		return
	}
	length, err := handler.ParseLength(c.GetHeader(handler.UploadLength), c.GetHeader(handler.UploadDefer))
	if err != nil {
		refuseTus(c, err)
		return
	}
	meta, err := handler.ParseMetadata(c.GetHeader(handler.UploadMetadata), tusMetadataMaxPairs)
	if err != nil {
		refuseTus(c, err)
		return
	}
	dest := meta["dest"]
	leaf := meta["relativePath"]
	if leaf == "" {
		leaf = meta["filename"]
	}
	if leaf == "" {
		refuse(c, apierr.Classified{Class: apierr.Unprocessable})
		return
	}
	if dest != "" {
		dest = strings.TrimSuffix(dest, "/") + "/" + leaf
	} else {
		dest = leaf
	}
	r, rerr := e.resolve(owner, dest, acl.Write|acl.Create)
	if rerr != nil {
		fail(c, rerr)
		return
	}
	spec := upload.SessionSpec{IfMatch: c.GetHeader("If-Match"), Meta: uploadMetaOf(meta), RandomAccess: c.GetHeader(handler.ScRandomAccess) == "1"}
	if !length.Deferred {
		total := length.Value
		spec.TotalLen = &total
	}
	if !length.Deferred {
		if qerr := e.Core.CheckQuota(c.Request.Context(), core.UserID(owner), length.Value); qerr != nil {
			fail(c, qerr)
			return
		}
	}
	sess, cerr := engine.Create(c.Request.Context(), r, spec)
	if cerr != nil {
		fail(c, cerr)
		return
	}
	c.Header("Location", server.Base+"/uploads/"+sess.ID.String())
	c.Header(handler.UploadOffset, strconv.FormatUint(sess.Offset, 10))
	c.Status(http.StatusCreated)
}

// tusMetadataMaxPairs bounds the metadata header. It is client-supplied and
// parsed before anything is authorised, so an unbounded one is work a caller
// can ask for without holding anything.
const tusMetadataMaxPairs = 32

// uploadsStatus reports how far a session has got.
//
// This is what a resume asks first: the offset it answers is the byte the
// next chunk has to start at.
func (e *Engine) uploadsStatus(c *gin.Context) {
	owner, ok := ownerOf(c)
	if !ok {
		refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	engine, ok := e.uploads(c)
	if !ok {
		refuse(c, apierr.Classified{Class: apierr.SubsystemUnavailable})
		return
	}
	e.setTusHeaders(c)
	if err := handler.CheckResumable(c.GetHeader(handler.TusResumable)); err != nil {
		refuseTus(c, err)
		return
	}
	id, ok := sessionIDOf(c)
	if !ok {
		notFound(c)
		return
	}
	sess, err := engine.Get(c.Request.Context(), id, owner)
	if err != nil {
		fail(c, err)
		return
	}
	if terminal, _ := handler.TerminalUploadState(sess.State.StateName()); terminal {
		notFound(c)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Header(handler.UploadOffset, strconv.FormatUint(sess.Offset, 10))
	if sess.TotalLen != nil {
		c.Header(handler.UploadLength, strconv.FormatUint(*sess.TotalLen, 10))
	} else {
		c.Header(handler.UploadDefer, "1")
	}
	c.Status(http.StatusOK)
}

// uploadsPatch takes one chunk.
func (e *Engine) uploadsPatch(c *gin.Context) {
	owner, ok := ownerOf(c)
	if !ok {
		refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	engine, ok := e.uploads(c)
	if !ok {
		refuse(c, apierr.Classified{Class: apierr.SubsystemUnavailable})
		return
	}
	e.setTusHeaders(c)
	if err := handler.CheckResumable(c.GetHeader(handler.TusResumable)); err != nil {
		refuseTus(c, err)
		return
	}
	id, ok := sessionIDOf(c)
	if !ok {
		notFound(c)
		return
	}
	if ct := c.GetHeader("Content-Type"); ct != tusChunkType {
		refuse(c, apierr.Classified{Class: apierr.Unprocessable})
		return
	}
	offset, oerr := handler.ParseOffset(c.GetHeader(handler.UploadOffset))
	if oerr != nil {
		refuseTus(c, oerr)
		return
	}
	sum, serr := chunkChecksum(c.GetHeader(handler.UploadChecksum))
	if serr != nil {
		refuseTus(c, serr)
		return
	}
	sess, gerr := engine.Get(c.Request.Context(), id, owner)
	if gerr != nil {
		fail(c, gerr)
		return
	}
	root, ok := e.Core.ShareRoot(sess.Share)
	if !ok {
		fail(c, gerr)
		return
	}
	next, perr := engine.PatchAt(c.Request.Context(), root, id, owner, offset, requestBodyReader(c), sum)
	if perr != nil {
		failUpload(c, perr)
		return
	}
	if sess.TotalLen != nil && next >= *sess.TotalLen {
		if !e.publishUpload(c, engine, sess, id, owner) {
			return
		}
	}
	c.Header(handler.UploadOffset, strconv.FormatUint(next, 10))
	c.Status(http.StatusNoContent)
}

// publishUpload moves a completed session to its destination, reporting
// whether it did and, when it did not, the already-written response.
//
// The destination is resolved fresh rather than carried from create. Access
// is checked at publication, so revoking a grant during a transfer prevents
// publication.
func (e *Engine) publishUpload(
	c *gin.Context, engine *upload.Engine, sess upload.Session,
	id upload.SessionID, owner core.UserID,
) bool {
	dest, err := e.Core.VpathFor(owner, sess.Share, sess.Dest.Share())
	if err != nil {
		fail(c, core.ErrNotFound)
		return false
	}

	resolved, err := e.Core.Resolve(owner, dest, acl.Write|acl.Create)
	if err != nil {
		fail(c, err)
		return false
	}
	if _, err := engine.Finalize(c.Request.Context(), resolved, id); err != nil {
		fail(c, err)
		return false
	}
	return true
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
func (e *Engine) uploadsAbort(c *gin.Context) {
	owner, ok := ownerOf(c)
	if !ok {
		refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	engine, ok := e.uploads(c)
	if !ok {
		refuse(c, apierr.Classified{Class: apierr.SubsystemUnavailable})
		return
	}
	e.setTusHeaders(c)
	if err := handler.CheckResumable(c.GetHeader(handler.TusResumable)); err != nil {
		refuseTus(c, err)
		return
	}
	id, ok := sessionIDOf(c)
	if !ok {
		notFound(c)
		return
	}
	if err := engine.Abort(c.Request.Context(), id, owner); err != nil {
		fail(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// uploads reports the resumable engine, which a deployment may not have.
//
// Its absence is a degradation rather than a fault: a spool directory that
// could not be opened costs resumable transfers and nothing else, and the
// rest of the server still serves.
func (e *Engine) uploads(c *gin.Context) (*upload.Engine, bool) {
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
func (e *Engine) uploadSettingsPatch(c *gin.Context) {
	if _, ok := e.admin(c); !ok {
		return
	}
	engine, ok := e.uploads(c)
	if !ok {
		refuse(c, apierr.Classified{Class: apierr.Unprocessable})
		return
	}
	var req uploadSettingsRequest
	if err := decodeBody(c, &req); err != nil {
		refuse(c, apierr.Classified{Class: apierr.Malformed})
		return
	}
	var minBytes, defaultBytes *uint64
	if req.ChunkMin != nil {
		v, err := num.Narrow[uint64](*req.ChunkMin)
		if err != nil {
			refuse(c, apierr.Classified{Class: apierr.Unprocessable})
			return
		}
		minBytes = &v
	}
	if req.ChunkDefault != nil {
		v, err := num.Narrow[uint64](*req.ChunkDefault)
		if err != nil {
			refuse(c, apierr.Classified{Class: apierr.Unprocessable})
			return
		}
		defaultBytes = &v
	}
	if minBytes != nil || defaultBytes != nil {
		if err := engine.ApplySettings(c.Request.Context(), minBytes, defaultBytes); err != nil {
			fail(c, err)
			return
		}
	}
	if req.CacheEnabled != nil && *req.CacheEnabled != engine.CacheEnabled() {
		if err := engine.SetCacheEnabled(c.Request.Context(), *req.CacheEnabled); err != nil {
			fail(c, err)
			return
		}
	}
	storedMin, storedDefault := engine.Settings().Snapshot()
	viewMin, minErr := num.Narrow[int64](storedMin)
	viewDefault, defErr := num.Narrow[int64](storedDefault)
	if minErr != nil || defErr != nil {
		failKnown(c, minErr)
		return
	}
	writeJSON(c, http.StatusOK, handler.UploadSettingsView{ChunkMin: viewMin, ChunkDefault: viewDefault, CacheEnabled: engine.CacheEnabled(), CacheAvailable: engine.CacheAvailable()})
}

// sessionIDOf reads the path's session id.
func sessionIDOf(c *gin.Context) (upload.SessionID, bool) {
	id, err := transfer.ParseSessionID(c.Param("id"))
	if err != nil {
		return upload.SessionID{}, false
	}
	return id, true
}

// refuseTus answers a protocol-level refusal.
//
// A version mismatch is its own status, because the client has to know to
// speak a different version rather than to fix its request.
func refuseTus(c *gin.Context, err error) {
	if errors.Is(err, handler.ErrTusVersion) {
		refuse(c, apierr.Classified{Class: apierr.Precondition})
		return
	}
	refuse(c, apierr.Classified{Class: apierr.Malformed})
}

// failUpload renders a chunk failure, with the protocol's own status for a
// digest that did not match.
//
// The shared mapper has no code for it: 460 is this protocol's invention and
// no general classifier would produce it, while a client watching for it
// retries the chunk rather than the whole transfer.
func failUpload(c *gin.Context, err error) {
	if errors.Is(err, upload.ErrChecksum) {
		writeJSON(c, handler.StatusChecksumMismatch, map[string]string{"error": "checksum_mismatch"})
		return
	}
	fail(c, err)
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
