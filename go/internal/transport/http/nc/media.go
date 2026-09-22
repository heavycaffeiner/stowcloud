//go:build linux && compat_nc

package nc

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/heavycaffeiner/stowcloud/go/internal/feature/files"
	"github.com/heavycaffeiner/stowcloud/go/internal/feature/preview"
	"github.com/heavycaffeiner/stowcloud/go/internal/feature/shares/acl"
	"github.com/heavycaffeiner/stowcloud/go/internal/kit/httpheader"
	"github.com/heavycaffeiner/stowcloud/go/internal/kit/num"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/apierr"
	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io"
	"net/http"
	"strconv"
	"strings"
	"unicode"
)

const filePermission = acl.Read | acl.Download

func (s *Server) preview(c *gin.Context) error {
	p, ok := principalOf(c)
	if !ok {
		c.Status(http.StatusUnauthorized)
		return nil
	}
	if s.deps.Preview == nil || s.deps.LocateFile == nil {
		c.Status(404)
		return nil
	}
	id, e := strconv.ParseUint(strings.TrimSpace(c.Query("fileId")), 10, 64)
	if e != nil || id == 0 {
		c.Status(404)
		return nil
	}
	ctx := c.Request.Context()
	path, e := s.deps.LocateFile(ctx, user(p), id)
	if e != nil || path == "" {
		c.Status(404)
		return nil
	}
	res, e := s.resolve(ctx, p, path, filePermission)
	if e != nil {
		c.Status(404)
		return nil
	}
	return s.serveSizedPreview(c, ctx, res, clampedQueryInt(c, "x", 1024), clampedQueryInt(c, "y", 1024))
}
func (s *Server) thumbnailByPath(c *gin.Context) error {
	p, ok := principalOf(c)
	if !ok {
		c.Status(401)
		return nil
	}
	x, y, path, ok := parseThumbnailWildcard(c.Param("path"))
	if !ok || s.deps.Preview == nil {
		c.Status(404)
		return nil
	}
	ctx := c.Request.Context()
	res, e := s.resolve(ctx, p, path, filePermission)
	if e != nil {
		c.Status(404)
		return nil
	}
	return s.serveSizedPreview(c, ctx, res, clampDim(x), clampDim(y))
}
func parseThumbnailWildcard(w string) (int, int, string, bool) {
	a, r, f := strings.Cut(w, "/")
	if !f {
		return 0, 0, "", false
	}
	b, p, f := strings.Cut(r, "/")
	if !f || p == "" {
		return 0, 0, "", false
	}
	x, e := strconv.Atoi(a)
	if e != nil {
		return 0, 0, "", false
	}
	y, e := strconv.Atoi(b)
	if e != nil || x <= 0 || y <= 0 {
		return 0, 0, "", false
	}
	return x, y, p, true
}
func (s *Server) serveSizedPreview(c *gin.Context, ctx context.Context, res core.Resolved, x, y int) (err error) {
	t, e := s.deps.Preview.GetSized(ctx, res, x, y)
	if e != nil {
		c.Status(404)
		return nil
	}
	defer func() {
		if cerr := t.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()
	st, e := t.File.Stat()
	if e != nil {
		c.Status(404)
		return nil
	}
	etag := previewETag(res, x, y)
	if matchesEtag(c.GetHeader("If-None-Match"), etag) {
		c.Header("ETag", etag)
		c.Status(304)
		return nil
	}
	c.Header("ETag", etag)
	c.Header("Cache-Control", "private, max-age=86400")
	http.ServeContent(c.Writer, c.Request, "preview.png", st.ModTime(), t.File)
	return nil
}
func previewETag(r core.Resolved, x, y int) string {
	h := sha256.Sum256([]byte(r.Path().String() + "\x00" + strconv.Itoa(x) + "x" + strconv.Itoa(y)))
	return `"` + hex.EncodeToString(h[:])[:32] + `"`
}
func matchesEtag(h, e string) bool {
	for _, p := range strings.Split(h, ",") {
		p = strings.TrimSpace(p)
		if p == "*" || p == e || strings.TrimPrefix(p, "W/") == e {
			return true
		}
	}
	return false
}
func clampDim(v int) int { return min(max(v, preview.MinSizedDimension), preview.MaxSizedDimension) }
func clampedQueryInt(c *gin.Context, k string, d int) int {
	v, e := strconv.Atoi(c.Query(k))
	if e != nil || v <= 0 {
		v = d
	}
	return clampDim(v)
}
func (s *Server) avatar(c *gin.Context) error {
	login := c.Param("user")
	if login == "" {
		c.Status(404)
		return nil
	}
	size := queryIntParam(c.Param("size"), 64)
	etag := avatarETag(login, size)
	if matchesEtag(c.GetHeader("If-None-Match"), etag) {
		c.Header("ETag", etag)
		c.Status(304)
		return nil
	}
	b, e := renderAvatar(login, size)
	if e != nil {
		c.Status(404)
		return nil
	}
	c.Header("Content-Type", "image/png")
	c.Header("ETag", etag)
	c.Header("Cache-Control", "public, max-age=86400")
	c.Data(200, "image/png", b)
	return nil
}
func queryIntParam(v string, d int) int {
	n, e := strconv.Atoi(v)
	if e != nil || n <= 0 {
		return d
	}
	return n
}
func avatarETag(l string, n int) string {
	h := sha256.Sum256([]byte(l + "\x00" + strconv.Itoa(n)))
	return `"` + hex.EncodeToString(h[:])[:32] + `"`
}
func avatarPalette() []color.RGBA {
	return []color.RGBA{{26, 115, 232, 255}, {217, 61, 37, 255}, {24, 140, 90, 255}, {154, 52, 168, 255}, {230, 138, 0, 255}, {0, 121, 125, 255}, {92, 51, 168, 255}, {194, 24, 91, 255}}
}
func renderAvatar(login string, size int) ([]byte, error) {
	if size < 16 {
		size = 16
	}
	if size > 512 {
		size = 512
	}
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	p := avatarPalette()[int(sha256.Sum256([]byte(login))[0])%len(avatarPalette())]
	draw.Draw(img, img.Bounds(), image.NewUniform(p), image.Point{}, draw.Src)
	if r := initialOf(login); r != 0 {
		drawLetter(img, r, size)
	}
	var b bytes.Buffer
	e := png.Encode(&b, img)
	return b.Bytes(), e
}
func initialOf(s string) rune {
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToUpper(r)
		}
	}
	return 0
}
func drawLetter(img *image.RGBA, r rune, size int) {
	face := basicfont.Face7x13
	sc := max(size/26, 1)
	a, _ := face.GlyphAdvance(r)
	gw := max(a.Ceil(), 1)
	gh := face.Ascent + face.Descent
	g := image.NewRGBA(image.Rect(0, 0, gw, gh))
	drawer := font.Drawer{Dst: g, Src: image.NewUniform(color.White), Face: face, Dot: fixed.P(0, face.Ascent)}
	drawer.DrawString(string(r))
	for y := 0; y < gh*sc; y++ {
		for x := 0; x < gw*sc; x++ {
			if g.RGBAAt(x/sc, y/sc).A > 0 {
				img.Set(x+(size-gw*sc)/2, y+(size-gh*sc)/2, color.White)
			}
		}
	}
}
func (s *Server) directLink(c *gin.Context, p Principal) (Val, bool, *Error) {
	if s.deps.SealClaim == nil || s.deps.LocateFile == nil {
		return Val{}, false, NotFound("direct links are not available")
	}
	id, e := strconv.ParseUint(strings.TrimSpace(c.Request.FormValue("fileId")), 10, 64)
	if e != nil || id == 0 {
		return Val{}, false, BadRequest("fileId is required")
	}
	ctx := c.Request.Context()
	path, e := s.deps.LocateFile(ctx, user(p), id)
	if e != nil || path == "" {
		return Val{}, false, NotFound("The requested resource could not be found")
	}
	if _, e = s.resolve(ctx, p, path, filePermission); e != nil {
		return Val{}, false, ocsErrorOf(e, apierr.VisibilityHidden)
	}
	tok, e := s.deps.SealClaim(user(p), path)
	if e != nil {
		return Val{}, false, Failure("the link could not be minted")
	}
	f := s.deps.ContentOrigin
	if f == nil {
		f = s.deps.Origin
	}
	return Obj(P("url", Str(f(originRequestOf(c))+"/remote.php/direct/"+tok))), true, nil
}
func (s *Server) directStream(c *gin.Context) (err error) {
	if s.deps.OpenClaim == nil {
		c.Status(404)
		return nil
	}
	owner, path, e := s.deps.OpenClaim(c.Param("token"))
	if e != nil || path == "" {
		c.Status(404)
		return nil
	}
	ctx := c.Request.Context()
	res, e := s.deps.Resolve(owner, path, filePermission)
	if e != nil {
		c.Status(404)
		return nil
	}
	entry, e := s.deps.Core.Stat(ctx, res)
	if e != nil {
		c.Status(404)
		return nil
	}
	rng, e := parseSingleRange(c.GetHeader("Range"), entry.Size)
	if e != nil {
		c.Header("Accept-Ranges", "bytes")
		c.Header("Content-Range", "bytes */"+strconv.FormatUint(entry.Size, 10))
		c.Status(416)
		return nil
	}
	fid, st, e := s.deps.Core.OpenStream(ctx, res, rng)
	if e != nil {
		c.Status(404)
		return nil
	}
	defer func() {
		if cerr := st.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()
	n, e := num.Narrow[int](st.Remaining())
	if e != nil {
		c.Status(404)
		return nil
	}
	ct := ContentTypeOf(false, fid.Name)
	c.Header("Content-Type", ct)
	c.Header("Accept-Ranges", "bytes")
	if httpheader.IsExecutableMIME(ct) {
		c.Header("Content-Disposition", httpheader.Attachment(fid.Name))
	} else {
		c.Header("Content-Security-Policy", httpheader.SafeInlineCSP)
	}
	status := 200
	if rng != nil {
		status = 206
		c.Header("Content-Range", contentRangeHeader(rng[0], rng[1], entry.Size))
	}
	c.Status(status)
	c.Header("Content-Length", strconv.Itoa(n))
	if _, e = io.CopyN(c.Writer, st, int64(n)); e != nil {
		return e
	}
	return nil
}
func parseSingleRange(h string, size uint64) (*[2]uint64, error) {
	h = strings.TrimSpace(h)
	if h == "" {
		return nil, nil
	}
	s, ok := strings.CutPrefix(h, "bytes=")
	if !ok || strings.Contains(s, ",") {
		return nil, errors.New("bad range")
	}
	a, b, ok := strings.Cut(s, "-")
	if !ok {
		return nil, errors.New("bad range")
	}
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	if a == "" {
		n, e := strconv.ParseUint(b, 10, 64)
		if e != nil || n == 0 {
			return nil, errors.New("bad range")
		}
		if n > size {
			n = size
		}
		return &[2]uint64{size - n, size - 1}, nil
	}
	start, e := strconv.ParseUint(a, 10, 64)
	if e != nil || start >= size {
		return nil, errors.New("bad range")
	}
	if b == "" {
		return &[2]uint64{start, size - 1}, nil
	}
	end, e := strconv.ParseUint(b, 10, 64)
	if e != nil || start > end {
		return nil, errors.New("bad range")
	}
	if end >= size {
		end = size - 1
	}
	return &[2]uint64{start, end}, nil
}
func contentRangeHeader(a, b, s uint64) string {
	return "bytes " + strconv.FormatUint(a, 10) + "-" + strconv.FormatUint(b, 10) + "/" + strconv.FormatUint(s, 10)
}
