//go:build linux

package publiclinks

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	core "github.com/heavycaffeiner/stowcloud/backend/internal/feature/files"
	"github.com/heavycaffeiner/stowcloud/backend/internal/feature/shares/acl"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/api/handler"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/apierr"
	httpheader "github.com/heavycaffeiner/stowcloud/backend/internal/http/headers"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/middleware"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/route"
	num "github.com/heavycaffeiner/stowcloud/backend/internal/platform/number"
	"github.com/heavycaffeiner/stowcloud/backend/internal/platform/protocol/limits"
	"github.com/heavycaffeiner/stowcloud/backend/internal/store/state"
)

const PublicLinkPrefix = "/s"

type PublicDeps struct {
	Core            *core.Core
	State           *state.DB
	ClaimKey        []byte
	Limiter         interface{ Allow(string) bool }
	Now             func() int64
	ClientAddr      func(*gin.Context) string
	Audit           func(context.Context, string, string, string, string, bool) error
	Logger          Logger
	Frontend        http.Handler
	Fail            func(*gin.Context, error)
	Refuse          func(*gin.Context, apierr.Classified)
	WriteJSON       func(*gin.Context, int, any)
	Decode          func(*gin.Context, any) error
	CloseStream     func(*core.Stream, string)
	SendStreamRange func(c interface {
		Header(string, string)
		Status(int)
	}, writer io.Writer, entry core.FidEntry, stream *core.Stream, ranged bool, rng handler.ByteRange, size int64, attachAs string, logger *slog.Logger)
	AcquireArchive func() (func(), bool)
	WriteArchive   func(context.Context, io.Writer, core.Link, string, string)
}
type Logger interface{ Warn(string, ...any) }
type Public struct{ d PublicDeps }

func NewPublic(d PublicDeps) *Public { return &Public{d: d} }

func (p *Public) Mount(app *gin.Engine) {
	app.GET(PublicLinkPrefix+"/:token", p.Landing)
	app.POST(PublicLinkPrefix+"/:token/auth", p.Unlock)
	app.GET(PublicLinkPrefix+"/:token/download", p.Download)
	app.GET(PublicLinkPrefix+"/:token/zip", p.Zip)
	app.POST(PublicLinkPrefix+"/:token/drop", p.Drop)
}
func (p *Public) Declare(app *gin.Engine, prefix string) {
	app.Use(func(c *gin.Context) {
		if !strings.HasPrefix(c.Request.URL.Path, prefix+"/") {
			c.Next()
			return
		}
		body := route.BodyNone
		if c.Request.Method == http.MethodPost {
			if strings.HasSuffix(c.Request.URL.Path, "/auth") {
				body = route.BodyJSON
			} else if strings.HasSuffix(c.Request.URL.Path, "/drop") {
				body = route.BodyStream
			}
		}
		middleware.SetRequirement(c, route.Requirement{Access: route.AccessPublic}, body, "public link")
		c.Next()
	})
}

func (p *Public) linkFor(c *gin.Context) (core.Link, error) {
	link, _, err := p.d.Core.LinkPublic(c.Request.Context(), c.Param("token"))
	if err != nil {
		p.d.Fail(c, err)
		return core.Link{}, err
	}
	if link.HasPassword && !p.unlocked(c, link) {
		p.d.Refuse(c, apierr.Classified{Class: apierr.Unprocessable, Key: "fs.link_password"})
		return core.Link{}, errors.New("link locked")
	}
	return link, nil
}
func linkCookie(id int64) string { return "sc_link_" + strconv.FormatInt(id, 10) }
func cookiePath(c *gin.Context, token string) string {
	prefix := PublicLinkPrefix
	if strings.HasPrefix(c.Request.URL.Path, "/index.php"+PublicLinkPrefix+"/") {
		prefix = "/index.php" + PublicLinkPrefix
	}
	return prefix + "/" + token
}
func (p *Public) unlocked(c *gin.Context, link core.Link) bool {
	if !link.HasPassword {
		return true
	}
	ticket, err := c.Cookie(linkCookie(link.ID))
	if err != nil || ticket == "" {
		return false
	}
	hash, err := p.d.State.PasswordHash(c.Request.Context(), link.ID)
	if err != nil || hash == nil {
		return false
	}
	return verifyTicket(p.d.ClaimKey, link.ID, *hash, ticket, p.d.Now())
}
func ticket(key []byte, id int64, hash string, exp int64) string {
	mac := hmac.New(sha256.New, key)
	if _, err := fmt.Fprintf(mac, "%d:%s:%d", id, hash, exp); err != nil {
		return ""
	}
	payload := fmt.Sprintf("%d.%s", exp, base64.RawURLEncoding.EncodeToString(mac.Sum(nil)))
	return base64.RawURLEncoding.EncodeToString([]byte(payload))
}
func verifyTicket(key []byte, id int64, hash, raw string, now int64) bool {
	b, e := base64.RawURLEncoding.DecodeString(raw)
	if e != nil {
		return false
	}
	parts := strings.SplitN(string(b), ".", 2)
	if len(parts) != 2 {
		return false
	}
	exp, e := strconv.ParseInt(parts[0], 10, 64)
	if e != nil || exp <= now {
		return false
	}
	sig, e := base64.RawURLEncoding.DecodeString(parts[1])
	if e != nil {
		return false
	}
	mac := hmac.New(sha256.New, key)
	if _, err := fmt.Fprintf(mac, "%d:%s:%d", id, hash, exp); err != nil {
		return false
	}
	return hmac.Equal(sig, mac.Sum(nil))
}

func (p *Public) Landing(c *gin.Context) {
	if strings.Contains(c.GetHeader("Accept"), "text/html") {
		if p.d.Frontend != nil {
			p.d.Frontend.ServeHTTP(c.Writer, c.Request)
			c.Abort()
			return
		}
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	link, root, err := p.d.Core.LinkPublic(c.Request.Context(), c.Param("token"))
	if err != nil {
		p.d.Fail(c, err)
		return
	}
	if link.HasPassword && !p.unlocked(c, link) {
		p.d.WriteJSON(c, http.StatusOK, gin.H{"protected": true})
		return
	}
	sub := strings.Trim(c.Query("path"), "/")
	var listing core.LinkListing
	if sub != "" && !link.Perms.Has(acl.Read) {
		p.d.Fail(c, core.ErrNotFound)
		return
	}
	if sub == "" && !link.Perms.Has(acl.Read) {
		listing = core.LinkListing{IsDir: root.IsDir, Name: root.Name, Size: root.Size}
	} else {
		listing, err = p.d.Core.LinkBrowse(c.Request.Context(), link, sub)
		if err != nil {
			p.d.Fail(c, err)
			return
		}
	}
	out := gin.H{"protected": false, "id": strconv.FormatInt(link.ID, 10), "name": listing.Name, "is_dir": listing.IsDir, "size": listing.Size, "label": link.Label, "note": link.Note, "path": listing.Path, "can_download": link.Perms.Has(acl.Download), "drop": link.Perms.Has(acl.Create) && !link.Perms.Has(acl.Read), "has_password": link.HasPassword, "max_upload_bytes": limits.RequestBody}
	if listing.IsDir && link.Perms.Has(acl.Read) {
		entries := make([]gin.H, 0, len(listing.Entries))
		for _, e := range listing.Entries {
			k := "file"
			if e.IsDir {
				k = "dir"
			}
			entries = append(entries, gin.H{"name": e.Name, "kind": k, "size": e.Size})
		}
		out["entries"] = entries
	}
	p.d.WriteJSON(c, http.StatusOK, out)
}
func (p *Public) Unlock(c *gin.Context) {
	link, _, err := p.d.Core.LinkPublic(c.Request.Context(), c.Param("token"))
	if err != nil {
		p.d.Fail(c, err)
		return
	}
	key := p.d.ClientAddr(c) + "/" + strconv.FormatInt(link.ID, 10)
	if p.d.Limiter != nil && !p.d.Limiter.Allow(key) {
		p.d.Refuse(c, apierr.Classified{Class: apierr.RateLimited, Key: "auth.rate_limited"})
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if err = p.d.Decode(c, &req); err != nil {
		p.d.Refuse(c, apierr.Classified{Class: apierr.Malformed})
		return
	}
	ok, err := p.d.Core.LinkCheckPassword(c.Request.Context(), link, req.Password)
	if err != nil {
		p.d.Fail(c, err)
		return
	}
	if !ok {
		if p.d.Audit != nil {
			if auditErr := p.d.Audit(c.Request.Context(), "link.unlock", fmt.Sprintf("link:%d", link.ID), key, c.Request.UserAgent(), false); auditErr != nil && p.d.Logger != nil {
				p.d.Logger.Warn("recording failed link unlock audit event", "error", auditErr)
			}
		}
		p.d.Refuse(c, apierr.Classified{Class: apierr.Unprocessable, Key: "fs.link_password"})
		return
	}
	if p.d.Audit != nil {
		if auditErr := p.d.Audit(c.Request.Context(), "link.unlock", fmt.Sprintf("link:%d", link.ID), key, c.Request.UserAgent(), true); auditErr != nil && p.d.Logger != nil {
			p.d.Logger.Warn("recording successful link unlock audit event", "error", auditErr)
		}
	}
	hash, err := p.d.State.PasswordHash(c.Request.Context(), link.ID)
	if err != nil || hash == nil {
		p.d.Fail(c, core.ErrNotFound)
		return
	}
	exp := p.d.Now() + int64(24*time.Hour)
	if link.Expires > 0 && link.Expires < exp {
		exp = link.Expires
	}
	http.SetCookie(c.Writer, &http.Cookie{Name: linkCookie(link.ID), Value: ticket(p.d.ClaimKey, link.ID, *hash, exp), Path: cookiePath(c, c.Param("token")), HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
	c.Status(http.StatusNoContent)
}
func (p *Public) Download(c *gin.Context) {
	link, err := p.linkFor(c)
	if err != nil {
		return
	}
	if !link.Perms.Has(acl.Download) {
		p.d.Refuse(c, apierr.Classified{Class: apierr.Denied, Key: "fs.link_no_download"})
		return
	}
	ctx := c.Request.Context()
	path := c.Query("path")
	entry, stream, err := p.d.Core.LinkStreamAt(ctx, link, path, nil)
	if err != nil {
		p.d.Fail(c, err)
		return
	}
	size, nerr := num.Narrow[int64](entry.Size)
	if nerr != nil {
		p.d.CloseStream(stream, entry.Name)
		p.d.Fail(c, core.ErrNotFound)
		return
	}
	rng, ranged, rerr := handler.ParseRange(c.GetHeader("Range"), size)
	if rerr != nil {
		p.d.CloseStream(stream, entry.Name)
		if errors.Is(rerr, handler.ErrRangeUnsatisfiable) {
			c.Header("Accept-Ranges", "bytes")
			c.Header("Content-Range", handler.UnsatisfiedRange(size))
			p.d.Refuse(c, apierr.Classified{Class: apierr.RangeNotSatisfiable})
			return
		}
		p.d.Refuse(c, apierr.Classified{Class: apierr.Unprocessable})
		return
	}
	if ranged {
		p.d.CloseStream(stream, entry.Name)
		start, serr := num.Narrow[uint64](rng.Start)
		last, lerr := num.Narrow[uint64](rng.End - 1)
		if serr != nil || lerr != nil {
			p.d.Fail(c, core.ErrNotFound)
			return
		}
		entry, stream, err = p.d.Core.LinkStreamAt(ctx, link, path, &[2]uint64{start, last})
		if err != nil {
			p.d.Fail(c, err)
			return
		}
	}
	if err = p.d.Core.NoteLinkDownload(ctx, link); err != nil {
		p.d.CloseStream(stream, entry.Name)
		p.d.Fail(c, err)
		return
	}
	// Always an attachment: a stranger's download must never render inline
	// under this origin.
	p.d.SendStreamRange(c, c.Writer, entry, stream, ranged, rng, size, entry.Name, nil)
}
func (p *Public) Zip(c *gin.Context) {
	link, err := p.linkFor(c)
	if err != nil {
		return
	}
	if !link.Perms.Has(acl.Download) {
		p.d.Refuse(c, apierr.Classified{Class: apierr.Denied, Key: "fs.link_no_download"})
		return
	}
	sub := c.Query("path")
	if strings.Trim(sub, "/") != "" && !link.Perms.Has(acl.Read) {
		p.d.Fail(c, core.ErrNotFound)
		return
	}
	listing, err := p.d.Core.LinkBrowse(c.Request.Context(), link, sub)
	if err != nil {
		p.d.Fail(c, err)
		return
	}
	if !listing.IsDir {
		p.d.Refuse(c, apierr.Classified{Class: apierr.Unprocessable, Key: "fs.link_not_a_folder"})
		return
	}
	release, ok := p.d.AcquireArchive()
	if !ok {
		p.d.Refuse(c, apierr.Classified{Class: apierr.ResourceExhausted, Key: "archive.busy"})
		return
	}
	defer release()
	if err = p.d.Core.NoteLinkDownload(c.Request.Context(), link); err != nil {
		p.d.Fail(c, err)
		return
	}
	c.Header("Content-Type", "application/zip")
	c.Header("Content-Disposition", httpheader.Attachment(listing.Name+".zip"))
	c.Status(http.StatusOK)
	p.d.WriteArchive(c.Request.Context(), c.Writer, link, sub, listing.Name)
}
func (p *Public) Drop(c *gin.Context) {
	link, err := p.linkFor(c)
	if err != nil {
		return
	}
	if !link.Perms.Has(acl.Create) {
		p.d.Refuse(c, apierr.Classified{Class: apierr.Denied, Key: "fs.link_no_upload"})
		return
	}
	name := c.Query("name")
	if name == "" {
		p.d.Refuse(c, apierr.Classified{Class: apierr.Unprocessable, Key: "fs.link_no_name"})
		return
	}
	if cl := c.GetHeader("Content-Length"); cl != "" {
		if n, e := strconv.ParseInt(cl, 10, 64); e == nil && n > limits.RequestBody {
			p.d.Refuse(c, apierr.Classified{Class: apierr.BodyTooLarge, Key: "http.body_too_large"})
			return
		}
	}
	body, e := io.ReadAll(io.LimitReader(c.Request.Body, limits.RequestBody+1))
	if e != nil {
		p.d.Fail(c, e)
		return
	}
	if len(body) > limits.RequestBody {
		p.d.Refuse(c, apierr.Classified{Class: apierr.BodyTooLarge, Key: "http.body_too_large"})
		return
	}
	entry, e := p.d.Core.LinkDropFile(c.Request.Context(), link, name, bytes.NewReader(body))
	if e != nil {
		p.d.Fail(c, e)
		return
	}
	p.d.WriteJSON(c, http.StatusCreated, gin.H{"name": entry.Name, "size": strconv.FormatUint(entry.Size, 10)})
}
