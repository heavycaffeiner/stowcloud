//go:build linux

package shares

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

	"github.com/danielgtaylor/huma/v2"
	"github.com/gin-gonic/gin"

	"github.com/heavycaffeiner/stowcloud/backend/internal/db/state"
	"github.com/heavycaffeiner/stowcloud/backend/internal/files"
	num "github.com/heavycaffeiner/stowcloud/backend/internal/platform/number"
	"github.com/heavycaffeiner/stowcloud/backend/internal/platform/protocol/limits"
	"github.com/heavycaffeiner/stowcloud/backend/internal/server/apierr"
	"github.com/heavycaffeiner/stowcloud/backend/internal/server/httpx"
	"github.com/heavycaffeiner/stowcloud/backend/internal/server/middleware"
	"github.com/heavycaffeiner/stowcloud/backend/internal/shares/acl"
)

const PublicLinkPrefix = "/s"

type PublicDeps struct {
	Core            *files.Core
	State           *state.DB
	ClaimKey        []byte
	Limiter         interface{ Allow(string) bool }
	Now             func() int64
	Audit           func(context.Context, string, string, string, string, bool) error
	Logger          *slog.Logger
	Frontend        http.Handler
	CloseStream     func(*files.Stream, string)
	SendStreamRange func(c interface {
		Header(string, string)
		Status(int)
	}, writer io.Writer, entry files.FidEntry, stream *files.Stream, ranged bool, rng httpx.ByteRange, size int64, attachAs string, logger *slog.Logger)
	AcquireArchive func() (func(), bool)
	WriteArchive   func(context.Context, io.Writer, files.Link, string, string)
}
type Public struct{ d PublicDeps }

func NewPublic(d PublicDeps) *Public { return &Public{d: d} }

func (p *Public) linkFor(c *gin.Context) (files.Link, error) {
	link, _, err := p.d.Core.LinkPublic(c.Request.Context(), c.Param("token"))
	if err != nil {
		httpx.Fail(c, err)
		return files.Link{}, err
	}
	proof, cerr := c.Cookie(linkCookie(link.ID))
	if cerr != nil {
		proof = ""
	}
	if !p.unlocked(c.Request.Context(), link, proof) {
		httpx.Refuse(c, apierr.Classified{Class: apierr.Unprocessable, Key: "fs.link_password"})
		return files.Link{}, errors.New("link locked")
	}
	return link, nil
}
func linkCookie(id int64) string { return "sc_link_" + strconv.FormatInt(id, 10) }

// unlocked reports whether proof is a valid unlock ticket for link.
func (p *Public) unlocked(ctx context.Context, link files.Link, proof string) bool {
	if !link.HasPassword {
		return true
	}
	if proof == "" {
		return false
	}
	hash, err := p.d.State.PasswordHash(ctx, link.ID)
	if err != nil || hash == nil {
		return false
	}
	return verifyTicket(p.d.ClaimKey, link.ID, *hash, proof, p.d.Now())
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

// LandingPage hands a browser navigating to a link the web client, so the
// typed route behind it only answers the client's own JSON request.
func (p *Public) LandingPage(c *gin.Context) {
	if !strings.Contains(c.GetHeader("Accept"), "text/html") {
		return
	}
	if p.d.Frontend != nil {
		p.d.Frontend.ServeHTTP(c.Writer, c.Request)
		c.Abort()
		return
	}
	c.AbortWithStatus(http.StatusNotFound)
}

type landingInput struct {
	Token   string `path:"token"`
	Path    string `query:"path"`
	cookies []*http.Cookie
}

// Resolve keeps the request cookies: the unlock ticket's name depends on the
// link, so it cannot be declared as a fixed cookie parameter.
func (in *landingInput) Resolve(ctx huma.Context) []error {
	in.cookies = huma.ReadCookies(ctx)
	return nil
}

func (in *landingInput) cookie(name string) string {
	for _, c := range in.cookies {
		if c.Name == name {
			return c.Value
		}
	}
	return ""
}

// PublicLinkView is a link as its visitor sees it. A locked link answers
// only Protected, so nothing about it is visible before the password.
type PublicLinkView struct {
	Protected bool `json:"protected"`
	*PublicLinkDetailView
}

type PublicLinkDetailView struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	IsDir          bool   `json:"is_dir"`
	Size           uint64 `json:"size"`
	Label          string `json:"label"`
	Note           string `json:"note"`
	Path           string `json:"path"`
	CanDownload    bool   `json:"can_download"`
	Drop           bool   `json:"drop"`
	HasPassword    bool   `json:"has_password"`
	MaxUploadBytes int64  `json:"max_upload_bytes"`
	// Entries is present only for a readable folder, and then even when empty.
	Entries *[]PublicLinkEntryView `json:"entries,omitempty"`
}

type PublicLinkEntryView struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	Size uint64 `json:"size"`
}

type landingOutput struct{ Body PublicLinkView }

// Landing describes a link, or one folder inside it, to a visitor.
func (p *Public) Landing(ctx context.Context, in *landingInput) (*landingOutput, error) {
	link, root, err := p.d.Core.LinkPublic(ctx, in.Token)
	if err != nil {
		return nil, err
	}
	if !p.unlocked(ctx, link, in.cookie(linkCookie(link.ID))) {
		return &landingOutput{Body: PublicLinkView{Protected: true}}, nil
	}
	sub := strings.Trim(in.Path, "/")
	canRead := link.Perms.Has(acl.Read)
	var listing files.LinkListing
	switch {
	case sub != "" && !canRead:
		return nil, files.ErrNotFound
	case sub == "" && !canRead:
		listing = files.LinkListing{IsDir: root.IsDir, Name: root.Name, Size: root.Size}
	default:
		listing, err = p.d.Core.LinkBrowse(ctx, link, sub)
		if err != nil {
			return nil, err
		}
	}
	detail := &PublicLinkDetailView{
		ID: strconv.FormatInt(link.ID, 10), Name: listing.Name, IsDir: listing.IsDir, Size: listing.Size,
		Label: link.Label, Note: link.Note, Path: listing.Path,
		CanDownload: link.Perms.Has(acl.Download), Drop: link.Perms.Has(acl.Create) && !canRead,
		HasPassword: link.HasPassword, MaxUploadBytes: limits.RequestBody,
	}
	if listing.IsDir && canRead {
		entries := make([]PublicLinkEntryView, 0, len(listing.Entries))
		for _, e := range listing.Entries {
			kind := "file"
			if e.IsDir {
				kind = "dir"
			}
			entries = append(entries, PublicLinkEntryView{Name: e.Name, Kind: kind, Size: e.Size})
		}
		detail.Entries = &entries
	}
	return &landingOutput{Body: PublicLinkView{PublicLinkDetailView: detail}}, nil
}

type unlockRequest struct {
	Password string `json:"password"`
}
type unlockInput struct {
	Token     string `path:"token"`
	UserAgent string `header:"User-Agent"`
	Body      unlockRequest
}
type unlockOutput struct {
	Status    int
	SetCookie string `header:"Set-Cookie"`
}

// Unlock checks a link password and sets a ticket cookie scoped to prefix
// plus the token, so it is only sent back to that link.
func (p *Public) Unlock(prefix string) func(context.Context, *unlockInput) (*unlockOutput, error) {
	return func(ctx context.Context, in *unlockInput) (*unlockOutput, error) {
		link, _, err := p.d.Core.LinkPublic(ctx, in.Token)
		if err != nil {
			return nil, err
		}
		key := middleware.ClientFrom(ctx).String() + "/" + strconv.FormatInt(link.ID, 10)
		if p.d.Limiter != nil && !p.d.Limiter.Allow(key) {
			return nil, apierr.AsClassified(apierr.RateLimited, "auth.rate_limited")
		}
		ok, err := p.d.Core.LinkCheckPassword(ctx, link, in.Body.Password)
		if err != nil {
			return nil, err
		}
		if p.d.Audit != nil {
			if auditErr := p.d.Audit(ctx, "link.unlock", fmt.Sprintf("link:%d", link.ID), key, in.UserAgent, ok); auditErr != nil && p.d.Logger != nil {
				p.d.Logger.Warn("recording link unlock audit event", "success", ok, "error", auditErr)
			}
		}
		if !ok {
			return nil, apierr.AsClassified(apierr.Unprocessable, "fs.link_password")
		}
		hash, err := p.d.State.PasswordHash(ctx, link.ID)
		if err != nil || hash == nil {
			return nil, files.ErrNotFound
		}
		exp := p.d.Now() + int64(24*time.Hour)
		if link.Expires > 0 && link.Expires < exp {
			exp = link.Expires
		}
		cookie := &http.Cookie{Name: linkCookie(link.ID), Value: ticket(p.d.ClaimKey, link.ID, *hash, exp), Path: prefix + "/" + in.Token, HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode}
		return &unlockOutput{Status: http.StatusNoContent, SetCookie: cookie.String()}, nil
	}
}
func (p *Public) Download(c *gin.Context) {
	link, err := p.linkFor(c)
	if err != nil {
		return
	}
	if !link.Perms.Has(acl.Download) {
		httpx.Refuse(c, apierr.Classified{Class: apierr.Denied, Key: "fs.link_no_download"})
		return
	}
	ctx := c.Request.Context()
	path := c.Query("path")
	entry, stream, err := p.d.Core.LinkStreamAt(ctx, link, path, nil)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	size, nerr := num.Narrow[int64](entry.Size)
	if nerr != nil {
		p.d.CloseStream(stream, entry.Name)
		httpx.Fail(c, files.ErrNotFound)
		return
	}
	rng, ranged, rerr := httpx.ParseRange(c.GetHeader("Range"), size)
	if rerr != nil {
		p.d.CloseStream(stream, entry.Name)
		if errors.Is(rerr, httpx.ErrRangeUnsatisfiable) {
			c.Header("Accept-Ranges", "bytes")
			c.Header("Content-Range", httpx.UnsatisfiedRange(size))
			httpx.Refuse(c, apierr.Classified{Class: apierr.RangeNotSatisfiable})
			return
		}
		httpx.Refuse(c, apierr.Classified{Class: apierr.Unprocessable})
		return
	}
	if ranged {
		p.d.CloseStream(stream, entry.Name)
		start, serr := num.Narrow[uint64](rng.Start)
		last, lerr := num.Narrow[uint64](rng.End - 1)
		if serr != nil || lerr != nil {
			httpx.Fail(c, files.ErrNotFound)
			return
		}
		entry, stream, err = p.d.Core.LinkStreamAt(ctx, link, path, &[2]uint64{start, last})
		if err != nil {
			httpx.Fail(c, err)
			return
		}
	}
	if err = p.d.Core.NoteLinkDownload(ctx, link); err != nil {
		p.d.CloseStream(stream, entry.Name)
		httpx.Fail(c, err)
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
		httpx.Refuse(c, apierr.Classified{Class: apierr.Denied, Key: "fs.link_no_download"})
		return
	}
	sub := c.Query("path")
	if strings.Trim(sub, "/") != "" && !link.Perms.Has(acl.Read) {
		httpx.Fail(c, files.ErrNotFound)
		return
	}
	listing, err := p.d.Core.LinkBrowse(c.Request.Context(), link, sub)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	if !listing.IsDir {
		httpx.Refuse(c, apierr.Classified{Class: apierr.Unprocessable, Key: "fs.link_not_a_folder"})
		return
	}
	release, ok := p.d.AcquireArchive()
	if !ok {
		httpx.Refuse(c, apierr.Classified{Class: apierr.ResourceExhausted, Key: "archive.busy"})
		return
	}
	defer release()
	if err = p.d.Core.NoteLinkDownload(c.Request.Context(), link); err != nil {
		httpx.Fail(c, err)
		return
	}
	c.Header("Content-Type", "application/zip")
	c.Header("Content-Disposition", httpx.Attachment(listing.Name+".zip"))
	c.Status(http.StatusOK)
	p.d.WriteArchive(c.Request.Context(), c.Writer, link, sub, listing.Name)
}
func (p *Public) Drop(c *gin.Context) {
	link, err := p.linkFor(c)
	if err != nil {
		return
	}
	if !link.Perms.Has(acl.Create) {
		httpx.Refuse(c, apierr.Classified{Class: apierr.Denied, Key: "fs.link_no_upload"})
		return
	}
	name := c.Query("name")
	if name == "" {
		httpx.Refuse(c, apierr.Classified{Class: apierr.Unprocessable, Key: "fs.link_no_name"})
		return
	}
	if c.Request.ContentLength > limits.RequestBody {
		middleware.DrainRejectedBody(c)
		httpx.Refuse(c, apierr.Classified{Class: apierr.BodyTooLarge, Key: "http.body_too_large"})
		return
	}
	body, e := io.ReadAll(io.LimitReader(c.Request.Body, limits.RequestBody+1))
	if e != nil {
		httpx.Fail(c, e)
		return
	}
	if len(body) > limits.RequestBody {
		middleware.DrainRejectedBody(c)
		httpx.Refuse(c, apierr.Classified{Class: apierr.BodyTooLarge, Key: "http.body_too_large"})
		return
	}
	entry, e := p.d.Core.LinkDropFile(c.Request.Context(), link, name, bytes.NewReader(body))
	if e != nil {
		httpx.Fail(c, e)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"name": entry.Name, "size": strconv.FormatUint(entry.Size, 10)})
}
