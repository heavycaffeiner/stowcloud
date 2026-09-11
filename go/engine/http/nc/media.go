//go:build linux && compat_nc

package nc

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"strconv"
	"strings"
	"unicode"

	"github.com/gofiber/fiber/v2"
	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/apierr"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/httpheader"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/preview"
)

// Previews, thumbnails, avatars and the short-lived direct-media link.
//
// Every one of these answers bytes rather than an envelope, and every one of
// them is reached by a client that has already decided the resource exists:
// a fileId from a PROPFIND, a path it just listed, a token it just minted.
// The one exception is the avatar, which a client fetches unconditionally
// for an account it merely knows the name of.

// filePermission is the permission a preview, a thumbnail and a direct
// stream all require: viewing a derivative of the bytes is viewing the file.
const filePermission = acl.Read | acl.Download

// preview answers /core/preview, /core/preview.png and the trash preview
// variant.
//
// The fileId a client sends is this deployment's own numeric identity, the
// same one a PROPFIND handed back as oc:fileid, so it is resolved through
// LocateFile rather than guessed at from a listing.
func (s *Server) preview(c *fiber.Ctx) error {
	p, ok := principalOf(c)
	if !ok {
		return c.SendStatus(fiber.StatusUnauthorized)
	}
	if s.deps.Preview == nil || s.deps.LocateFile == nil {
		// No decoder, or no way back from an id to a path: either way this
		// is the 404 every client already renders as its own placeholder.
		return c.SendStatus(fiber.StatusNotFound)
	}

	id, err := strconv.ParseUint(strings.TrimSpace(c.Query("fileId")), 10, 64)
	if err != nil || id == 0 {
		return c.SendStatus(fiber.StatusNotFound)
	}
	ctx := c.UserContext()
	path, err := s.deps.LocateFile(ctx, user(p), id)
	if err != nil || path == "" {
		return c.SendStatus(fiber.StatusNotFound)
	}

	res, err := s.resolve(ctx, p, path, filePermission)
	if err != nil {
		return c.SendStatus(fiber.StatusNotFound)
	}
	x := clampedQueryInt(c, "x", 1024)
	y := clampedQueryInt(c, "y", 1024)
	return s.serveSizedPreview(c, ctx, res, x, y)
}

// thumbnailByPath answers the Android fallback, which addresses the file by
// its remote path instead of its id.
func (s *Server) thumbnailByPath(c *fiber.Ctx) error {
	p, ok := principalOf(c)
	if !ok {
		return c.SendStatus(fiber.StatusUnauthorized)
	}
	if s.deps.Preview == nil {
		return c.SendStatus(fiber.StatusNotFound)
	}

	x, y, path, ok := parseThumbnailWildcard(c.Params("*"))
	if !ok {
		return c.SendStatus(fiber.StatusNotFound)
	}
	ctx := c.UserContext()
	res, err := s.resolve(ctx, p, path, filePermission)
	if err != nil {
		return c.SendStatus(fiber.StatusNotFound)
	}
	return s.serveSizedPreview(c, ctx, res, clampDim(x), clampDim(y))
}

// parseThumbnailWildcard reads the width, height and path segments the
// legacy thumbnail route packs into one wildcard: {x}/{y}/{path...}.
func parseThumbnailWildcard(wildcard string) (x, y int, path string, ok bool) {
	first, rest, found := strings.Cut(wildcard, "/")
	if !found {
		return 0, 0, "", false
	}
	second, path, found := strings.Cut(rest, "/")
	if !found || path == "" {
		return 0, 0, "", false
	}
	xv, xerr := strconv.Atoi(first)
	yv, yerr := strconv.Atoi(second)
	if xerr != nil || yerr != nil || xv <= 0 || yv <= 0 {
		return 0, 0, "", false
	}
	return xv, yv, path, true
}

// serveSizedPreview generates and answers one thumbnail.
//
// A file this build cannot thumbnail, or one the decoder refuses, answers
// 404 rather than a wire error: every client here renders that as its own
// placeholder icon, which is what forceIcon=0 is asking a server for in the
// first place.
func (s *Server) serveSizedPreview(c *fiber.Ctx, ctx context.Context, res core.Resolved, x, y int) error {
	thumb, err := s.deps.Preview.GetSized(ctx, res, x, y)
	if err != nil {
		return c.SendStatus(fiber.StatusNotFound)
	}
	defer func() {
		if cerr := thumb.Close(); cerr != nil {
			s.log.Warn("a thumbnail was not released", "error", cerr)
		}
	}()

	info, err := thumb.File.Stat()
	if err != nil {
		return c.SendStatus(fiber.StatusNotFound)
	}

	etag := previewETag(res, x, y)
	if inm := c.Get(fiber.HeaderIfNoneMatch); inm != "" && matchesEtag(inm, etag) {
		c.Set(fiber.HeaderETag, etag)
		return c.SendStatus(fiber.StatusNotModified)
	}

	c.Set(fiber.HeaderContentType, "image/png")
	c.Set(fiber.HeaderETag, etag)
	c.Set(fiber.HeaderCacheControl, "private, max-age=86400")
	return c.Status(fiber.StatusOK).SendStream(thumb.File, int(info.Size()))
}

// previewETag keys a thumbnail's validator on the source file's own path and
// the size asked for, so a different box never serves a stale cached copy.
// The cache backing GetSized is itself keyed on the file's mtime, so a
// changed file already answers a different body under the same request.
func previewETag(res core.Resolved, x, y int) string {
	sum := sha256.Sum256([]byte(res.Path().String() + "\x00" +
		strconv.Itoa(x) + "x" + strconv.Itoa(y)))
	return `"` + hex.EncodeToString(sum[:])[:32] + `"`
}

// matchesEtag reports whether a comma-separated If-None-Match header admits
// the given quoted validator, tolerating the weak marker a proxy may add.
func matchesEtag(header, etag string) bool {
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		if part == "*" || part == etag || strings.TrimPrefix(part, "W/") == etag {
			return true
		}
	}
	return false
}

// clampDim holds a dimension inside the preview service's own supported
// range, the same bound GetSized itself enforces.
func clampDim(v int) int {
	return min(max(v, preview.MinSizedDimension), preview.MaxSizedDimension)
}

// clampedQueryInt reads a query parameter as a dimension, clamped into the
// preview service's own bounds. Absent or unparsable answers the fallback,
// clamped the same way.
func clampedQueryInt(c *fiber.Ctx, key string, fallback int) int {
	v, err := strconv.Atoi(c.Query(key))
	if err != nil || v <= 0 {
		v = fallback
	}
	return clampDim(v)
}

// avatar answers /index.php/avatar/{user}/{size}.
//
// This deployment stores no avatar image, and the Android client hard-fails
// unless the response Content-Type starts with "image" regardless of status,
// so a 404 here is read as a broken account rather than as "no avatar set".
// A generated initial answers both truths: no client-visible failure, and no
// stored image pretending to be one.
func (s *Server) avatar(c *fiber.Ctx) error {
	login := c.Params("user")
	if login == "" {
		return c.SendStatus(fiber.StatusNotFound)
	}
	size := queryIntParam(c.Params("size"), 64)

	etag := avatarETag(login, size)
	if inm := c.Get(fiber.HeaderIfNoneMatch); inm != "" && matchesEtag(inm, etag) {
		c.Set(fiber.HeaderETag, etag)
		return c.SendStatus(fiber.StatusNotModified)
	}

	body, err := renderAvatar(login, size)
	if err != nil {
		return c.SendStatus(fiber.StatusNotFound)
	}
	c.Set(fiber.HeaderContentType, "image/png")
	c.Set(fiber.HeaderETag, etag)
	c.Set(fiber.HeaderCacheControl, "public, max-age=86400")
	return c.Status(fiber.StatusOK).Send(body)
}

// queryIntParam parses a route parameter, or the fallback when it is absent
// or not a positive integer.
func queryIntParam(v string, fallback int) int {
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}

// avatarETag keys a generated avatar on the account name and the requested
// size, both of which decide every pixel this handler draws.
func avatarETag(login string, size int) string {
	sum := sha256.Sum256([]byte(login + "\x00" + strconv.Itoa(size)))
	return `"` + hex.EncodeToString(sum[:])[:32] + `"`
}

// avatarPalette is the set a background colour is picked from by account
// name, so the same account always draws the same colour and different
// accounts usually draw different ones.
func avatarPalette() []color.RGBA {
	return []color.RGBA{
		{R: 0x1a, G: 0x73, B: 0xe8, A: 0xff}, {R: 0xd9, G: 0x3d, B: 0x25, A: 0xff},
		{R: 0x18, G: 0x8c, B: 0x5a, A: 0xff}, {R: 0x9a, G: 0x34, B: 0xa8, A: 0xff},
		{R: 0xe6, G: 0x8a, B: 0x00, A: 0xff}, {R: 0x00, G: 0x79, B: 0x7d, A: 0xff},
		{R: 0x5c, G: 0x33, B: 0xa8, A: 0xff}, {R: 0xc2, G: 0x18, B: 0x5b, A: 0xff},
	}
}

// renderAvatar draws a flat-colour square carrying the account's first
// letter, using only the standard library plus the bitmap font already
// vendored for the preview decoder's platform target.
func renderAvatar(login string, size int) ([]byte, error) {
	if size < 16 {
		size = 16
	}
	if size > 512 {
		size = 512
	}

	sum := sha256.Sum256([]byte(login))
	bg := avatarPalette()[int(sum[0])%len(avatarPalette())]

	img := image.NewRGBA(image.Rect(0, 0, size, size))
	draw.Draw(img, img.Bounds(), &image.Uniform{C: bg}, image.Point{}, draw.Src)

	if letter := initialOf(login); letter != 0 {
		drawLetter(img, letter, size)
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// initialOf reads the first letter or digit a person would read as the
// account's initial, upper-cased, or the zero rune for a name with none.
func initialOf(login string) rune {
	for _, r := range login {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToUpper(r)
		}
	}
	return 0
}

// drawLetter centres one glyph from the fixed 7x13 bitmap face onto img,
// nearest-neighbour scaled to a size proportionate to the square.
func drawLetter(img *image.RGBA, letter rune, size int) {
	face := basicfont.Face7x13
	scale := max(size/26, 1)

	advance, _ := face.GlyphAdvance(letter)
	glyphW := max(advance.Ceil(), 1)
	glyphH := face.Ascent + face.Descent

	glyph := image.NewRGBA(image.Rect(0, 0, glyphW, glyphH))
	d := font.Drawer{
		Dst:  glyph,
		Src:  image.NewUniform(color.White),
		Face: face,
		Dot:  fixed.P(0, face.Ascent),
	}
	d.DrawString(string(letter))

	outW, outH := glyphW*scale, glyphH*scale
	offX, offY := (size-outW)/2, (size-outH)/2
	for y := 0; y < outH; y++ {
		for x := 0; x < outW; x++ {
			px := glyph.RGBAAt(x/scale, y/scale)
			if px.A == 0 {
				// Nothing inked here: leave the background showing through
				// instead of painting a transparent hole.
				continue
			}
			img.SetRGBA(offX+x, offY+y, px)
		}
	}
}

// directLink mints a short-lived, unauthenticated URL for one file id.
//
// The claim itself is the deployment's own: it is signed and revocable by a
// key this package never sees, so a nil SealClaim answers 404 rather than
// falling back to a token minted here that nothing else could revoke.
func (s *Server) directLink(c *fiber.Ctx, p Principal) (Val, bool, *Error) {
	if s.deps.SealClaim == nil || s.deps.LocateFile == nil {
		return Val{}, false, NotFound("direct links are not available")
	}
	id, err := strconv.ParseUint(strings.TrimSpace(c.FormValue("fileId")), 10, 64)
	if err != nil || id == 0 {
		return Val{}, false, BadRequest("fileId is required")
	}

	ctx := c.UserContext()
	path, err := s.deps.LocateFile(ctx, user(p), id)
	if err != nil || path == "" {
		return Val{}, false, NotFound("The requested resource could not be found")
	}
	if _, rerr := s.resolve(ctx, p, path, filePermission); rerr != nil {
		return Val{}, false, ocsErrorOf(rerr, apierr.VisibilityHidden)
	}

	token, err := s.deps.SealClaim(user(p), path)
	if err != nil {
		return Val{}, false, Failure("the link could not be minted")
	}

	render := s.deps.ContentOrigin
	if render == nil {
		render = s.deps.Origin
	}
	return Obj(P("url", Str(render(originRequestOf(c))+"/remote.php/direct/"+token))), true, nil
}

// directStream serves the bytes a direct link names.
//
// The permission the minting account held is re-checked for that same
// account rather than trusted from the token, so a claim never outlives the
// access it was minted under, and never spends another account's grant.
func (s *Server) directStream(c *fiber.Ctx) error {
	if s.deps.OpenClaim == nil {
		return c.SendStatus(fiber.StatusNotFound)
	}
	token := c.Params("token")
	if token == "" {
		return c.SendStatus(fiber.StatusNotFound)
	}

	owner, path, err := s.deps.OpenClaim(token)
	if err != nil || path == "" {
		return c.SendStatus(fiber.StatusNotFound)
	}
	ctx := c.UserContext()
	res, err := s.deps.Resolve(owner, path, filePermission)
	if err != nil {
		return c.SendStatus(fiber.StatusNotFound)
	}
	entry, err := s.deps.Core.Stat(ctx, res)
	if err != nil {
		return c.SendStatus(fiber.StatusNotFound)
	}

	rng, rerr := parseSingleRange(c.Get(fiber.HeaderRange), entry.Size)
	if rerr != nil {
		c.Set(fiber.HeaderAcceptRanges, "bytes")
		c.Set(fiber.HeaderContentRange, "bytes */"+strconv.FormatUint(entry.Size, 10))
		return c.SendStatus(fiber.StatusRequestedRangeNotSatisfiable)
	}

	fid, stream, err := s.deps.Core.OpenStream(ctx, res, rng)
	if err != nil {
		return c.SendStatus(fiber.StatusNotFound)
	}
	length, lerr := num.Narrow[int](stream.Remaining())
	if lerr != nil {
		if cerr := stream.Close(); cerr != nil {
			s.log.Warn("a direct stream was not released", "error", cerr)
		}
		return c.SendStatus(fiber.StatusNotFound)
	}

	contentType := ContentTypeOf(false, fid.Name)
	c.Set(fiber.HeaderContentType, contentType)
	c.Set(fiber.HeaderXContentTypeOptions, "nosniff")
	if httpheader.IsExecutableMIME(contentType) {
		c.Set(fiber.HeaderContentDisposition, httpheader.Attachment(fid.Name))
	} else {
		c.Set(fiber.HeaderContentSecurityPolicy, httpheader.SafeInlineCSP)
	}
	c.Set(fiber.HeaderAcceptRanges, "bytes")
	status := fiber.StatusOK
	if rng != nil {
		status = fiber.StatusPartialContent
		c.Set(fiber.HeaderContentRange, contentRangeHeader(rng[0], rng[1], entry.Size))
	}
	c.Status(status)
	// The sized form: the body is read after this handler returns, so a
	// stream closed here would be closed before it was sent, and a stream
	// sent without its size would go out chunked under a Content-Length that
	// no longer holds. The framework closes the stream once it has read it.
	return c.SendStream(stream, length)
}

// parseSingleRange reads a Range header against a known size, refusing a
// multi-range or non-byte request rather than serving the whole file with a
// misleading 206.
func parseSingleRange(header string, size uint64) (*[2]uint64, error) {
	header = strings.TrimSpace(header)
	if header == "" {
		return nil, nil
	}
	spec, ok := strings.CutPrefix(header, "bytes=")
	if !ok || strings.Contains(spec, ",") {
		return nil, errBadRange
	}
	first, last, found := strings.Cut(spec, "-")
	if !found {
		return nil, errBadRange
	}
	first, last = strings.TrimSpace(first), strings.TrimSpace(last)

	switch {
	case first == "" && last == "":
		return nil, errBadRange
	case first == "":
		n, err := strconv.ParseUint(last, 10, 64)
		if err != nil || n == 0 {
			return nil, errBadRange
		}
		if n > size {
			n = size
		}
		return &[2]uint64{size - n, size - 1}, nil
	case last == "":
		start, err := strconv.ParseUint(first, 10, 64)
		if err != nil || start >= size {
			return nil, errBadRange
		}
		return &[2]uint64{start, size - 1}, nil
	default:
		start, serr := strconv.ParseUint(first, 10, 64)
		end, eerr := strconv.ParseUint(last, 10, 64)
		if serr != nil || eerr != nil || start > end || start >= size {
			return nil, errBadRange
		}
		if end >= size {
			end = size - 1
		}
		return &[2]uint64{start, end}, nil
	}
}

// errBadRange is a Range header this handler will not serve.
var errBadRange = errors.New("nc: the requested range is not satisfiable")

// contentRangeHeader renders the Content-Range value for one served range.
func contentRangeHeader(start, end, size uint64) string {
	return "bytes " + strconv.FormatUint(start, 10) + "-" + strconv.FormatUint(end, 10) +
		"/" + strconv.FormatUint(size, 10)
}
