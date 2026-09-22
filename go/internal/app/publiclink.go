//go:build linux

// The surface a stranger reaches with nothing but a URL.
//
// No account is involved anywhere below. The token names the link, the link
// carries its own permission set, and that set is the entire answer to what
// the caller may do. Where a link is locked, the password is answered once and
// the proof rides in a cookie that goes nowhere else.
//
// Unversioned, unlike the rest of the API: these addresses get pasted into
// messages and typed by hand, and a version segment in one is noise to
// everybody who ever reads it aloud.
package app

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/heavycaffeiner/stowcloud/go/internal/feature/files"
	"github.com/heavycaffeiner/stowcloud/go/internal/feature/shares/acl"
	"github.com/heavycaffeiner/stowcloud/go/internal/kit/httpheader"
	"github.com/heavycaffeiner/stowcloud/go/internal/kit/limits"
	"github.com/heavycaffeiner/stowcloud/go/internal/kit/num"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/apierr"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/archive"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/middleware"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/route"
)

// PublicLinkPrefix is where the public link surface is mounted.
const PublicLinkPrefix = "/s"

// errArchiveBounded stops the walk once the content ceiling is reached. The
// caller writes an explicit marker before closing the archive, then returns
// this sentinel so the committed stream is also recorded as incomplete.
var errArchiveBounded = errors.New("archive bounds reached")

// mountPublicLinks binds the five routes a link's holder reaches.
//
// Registered directly rather than through the route table, because the table
// carries the API's version prefix and these paths are the product's own.
// What they require is declared in `declarePublicLinks`, which runs ahead of
// the chain: a handler wrapper here would run after every step that reads it.
func (e *Engine) mountPublicLinks(app *gin.Engine) {
	app.GET(PublicLinkPrefix+"/:token", e.linkLanding)
	app.POST(PublicLinkPrefix+"/:token/auth", e.linkUnlock)
	app.GET(PublicLinkPrefix+"/:token/download", e.linkDownload)
	app.GET(PublicLinkPrefix+"/:token/zip", e.linkZip)
	app.POST(PublicLinkPrefix+"/:token/drop", e.linkDrop)
}

// declarePublicLinks tells the chain what the link routes require, before the
// chain runs.
//
// The link's own token is the authority on these paths. A visitor who also
// holds a session cookie for this deployment is still a visitor following a
// link, and without this declaration the CSRF step sees an ambient cookie on
// a mutating request and refuses the unlock and the drop for every signed-in
// browser, including the owner testing their own link.
//
// A pass-through app.Use per spelling rather than a registration per path, for
// the reason Announce gives: app.Use matches without making the path "found",
// so an address nothing serves still answers 404 rather than reaching a
// handler with nothing after it. It matches the prefix, so the body class is
// chosen here rather than by registration order.
func (e *Engine) declarePublicLinks(app *gin.Engine) {
	e.declarePublicLinkPrefix(app, PublicLinkPrefix)
	e.declarePublicLinkAliases(app)
}

// declarePublicLinkPrefix attaches one public-link spelling's requirement
// metadata before the middleware chain, including its mutation body class.
func (e *Engine) declarePublicLinkPrefix(app *gin.Engine, prefix string) {
	app.Use(func(c *gin.Context) {
		if !strings.HasPrefix(c.Request.URL.Path, prefix+"/") {
			c.Next()
			return
		}
		body := route.BodyNone
		if c.Request.Method == http.MethodPost {
			switch {
			case strings.HasSuffix(c.Request.URL.Path, "/auth"):
				body = route.BodyJSON
			case strings.HasSuffix(c.Request.URL.Path, "/drop"):
				body = route.BodyStream
			}
		}
		middleware.SetRequirement(c, route.Requirement{Access: route.AccessPublic}, body, "public link")
		c.Next()
	})
}

// linkFor turns a token into a link, or writes the refusal and reports false.
//
// Every route that touches content goes through here so the lock is checked in
// exactly one place. Four separate copies of the check is three chances to
// leave one out, and the one left out serves a locked link to anybody.
func (e *Engine) linkFor(c *gin.Context) (core.Link, error) {
	link, _, err := e.Core.LinkPublic(c.Request.Context(), c.Param("token"))
	if err != nil {
		fail(c, err)
		return core.Link{}, err
	}
	if link.HasPassword && !e.linkUnlocked(c, link) {
		refuse(c, linkPasswordRefusal())
		return core.Link{}, errors.New("link locked")
	}
	return link, nil
}

// linkPasswordRefusal is the single answer a locked link gives, whether the
// cookie was missing, stale, or simply wrong. One shape, so the response says
// only that a password is wanted.
func linkPasswordRefusal() apierr.Classified {
	return apierr.Classified{Class: apierr.Unprocessable, Key: "fs.link_password"}
}

// linkCookie names the cookie that remembers one link's password.
//
// Per link, and scoped to that link's own path. Two links shared with two
// different people are two separate secrets, and a cookie broad enough to
// cover both would make answering one password enough to open the other.
func linkCookie(id int64) string {
	return "sc_link_" + strconv.FormatInt(id, 10)
}

// publicLinkCookiePath scopes an unlock proof to the spelling that accepted
// it. The compatibility front-controller alias has a different browser path
// from the canonical route, so sharing one cookie path would make one flow
// unusable while broadening it to the whole site would leak proof elsewhere.
func publicLinkCookiePath(c *gin.Context, token string) string {
	const aliasPrefix = "/index.php" + PublicLinkPrefix
	prefix := PublicLinkPrefix
	if strings.HasPrefix(c.Request.URL.Path, aliasPrefix+"/") {
		prefix = aliasPrefix
	}
	return prefix + "/" + token
}

// linkUnlocked verifies the HMAC unlock ticket against the stored password hash.
//
// Storing an HMAC ticket bound to the stored password hash avoids storing plaintext
// passwords in cookies and prevents Argon2id permit exhaustion on subsequent requests.
// Changing a link's password changes its stored hash, immediately invalidating all
// outstanding tickets.
func (e *Engine) linkUnlocked(c *gin.Context, link core.Link) bool {
	if !link.HasPassword {
		return true
	}
	ticket, err := c.Cookie(linkCookie(link.ID))
	if err != nil || ticket == "" {
		return false
	}
	hash, err := e.State.PasswordHash(c.Request.Context(), link.ID)
	if err != nil || hash == nil {
		return false
	}
	return verifyLinkTicket(e.claimKey.Key, link.ID, *hash, ticket, e.clk().Nanos())
}

func linkTicket(key []byte, linkID int64, storedHash string, expiresNs int64) string {
	mac := hmac.New(sha256.New, key)
	if _, err := fmt.Fprintf(mac, "%d:%s:%d", linkID, storedHash, expiresNs); err != nil {
		return ""
	}
	sig := mac.Sum(nil)
	payload := fmt.Sprintf("%d.%s", expiresNs, base64.RawURLEncoding.EncodeToString(sig))
	return base64.RawURLEncoding.EncodeToString([]byte(payload))
}

func verifyLinkTicket(key []byte, linkID int64, storedHash string, rawTicket string, nowNs int64) bool {
	payloadBytes, err := base64.RawURLEncoding.DecodeString(rawTicket)
	if err != nil {
		return false
	}
	parts := strings.SplitN(string(payloadBytes), ".", 2)
	if len(parts) != 2 {
		return false
	}
	expiresNs, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || expiresNs <= nowNs {
		return false
	}
	gotSig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, key)
	if _, err := fmt.Fprintf(mac, "%d:%s:%d", linkID, storedHash, expiresNs); err != nil {
		return false
	}
	expectedSig := mac.Sum(nil)
	return hmac.Equal(gotSig, expectedSig)
}

// linkLimiter enforces a sliding-window attempt budget on link unlocks.
type linkLimiter struct {
	mu     sync.Mutex
	window time.Duration
	max    int
	now    func() int64
	k      map[string]*linkLimitBucket
	ord    []string
}

type linkLimitBucket struct {
	count int
	reset int64
}

const linkLimiterKeys = 65536

func newLinkLimiter(window time.Duration, maxAttempts int, now func() int64) *linkLimiter {
	return &linkLimiter{
		window: window,
		max:    maxAttempts,
		now:    now,
		k:      make(map[string]*linkLimitBucket),
	}
}

func (l *linkLimiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	b, ok := l.k[key]
	if !ok || now >= b.reset {
		if !ok {
			if len(l.ord) >= linkLimiterKeys {
				delete(l.k, l.ord[0])
				l.ord = l.ord[1:]
			}
			l.ord = append(l.ord, key)
		}
		l.k[key] = &linkLimitBucket{count: 1, reset: now + l.window.Nanoseconds()}
		return true
	}
	if b.count >= l.max {
		return false
	}
	b.count++
	return true
}

// linkLanding answers GET /s/{token}.
//
// The same address is both the page a visitor opens and the endpoint the
// page's own fetch reads, and the Accept header is what tells the two apart:
// a navigation asking for text/html gets the interface document, whose
// client router then fetches the data with an explicit JSON accept. A
// request that already asked for JSON is a fetch, and gets the data.

func (e *Engine) linkLanding(c *gin.Context) {
	// A browser navigation gets the document; the page's own script then
	// fetches the data with an explicit JSON accept. Serving the document
	// here is what makes a pasted link open at all.
	if strings.Contains(c.GetHeader("Accept"), "text/html") {
		e.serveFrontendDocument(c)
		return
	}

	link, root, err := e.Core.LinkPublic(c.Request.Context(), c.Param("token"))
	if err != nil {
		fail(c, err)
		return
	}

	// A locked link answers with nothing but the fact that it is locked. The
	// name and the listing are behind the password too: a link whose contents
	// are readable without it is one where the password only guards the bytes.
	if link.HasPassword && !e.linkUnlocked(c, link) {
		writeJSON(c, http.StatusOK, gin.H{"protected": true})
		return
	}

	sub := strings.Trim(c.Query("path"), "/")
	var listing core.LinkListing
	if sub != "" && !link.Perms.Has(acl.Read) {
		// A create-only link has no authority to inspect descendants. Refuse
		// before LinkBrowse can stat a guessed path, and use the same not-found
		// answer for existing and absent descendants.
		fail(c, core.ErrNotFound)
	}
	if sub == "" && !link.Perms.Has(acl.Read) {
		listing = core.LinkListing{
			Path: "", IsDir: root.IsDir, Name: root.Name, Size: root.Size,
		}
	} else {
		var lerr error
		listing, lerr = e.Core.LinkBrowse(c.Request.Context(), link, sub)
		if lerr != nil {
			fail(c, lerr)
		}
	}

	out := gin.H{
		"protected": false,
		"id":        strconv.FormatInt(link.ID, 10),
		"name":      listing.Name,
		"is_dir":    listing.IsDir,
		"size":      listing.Size,
		"label":     link.Label,
		"note":      link.Note,
		"path":      listing.Path,
		// The page decides which controls to draw from these two, so they are
		// answered even where they are false.
		"can_download": link.Perms.Has(acl.Download),
		"drop":         link.Perms.Has(acl.Create) && !link.Perms.Has(acl.Read),
		"has_password": link.HasPassword,
		// The ceiling a drop hits, so the page can refuse an oversized file
		// before it streams one and learns at the end.
		"max_upload_bytes": limits.RequestBody,
	}

	// Withheld unless the link grants reading. A collection box is the point
	// of a drop link, and a box whose contents are visible to everyone who can
	// post into it is not one.
	if listing.IsDir && link.Perms.Has(acl.Read) {
		entries := make([]gin.H, 0, len(listing.Entries))
		for _, entry := range listing.Entries {
			kind := "file"
			if entry.IsDir {
				kind = "dir"
			}
			entries = append(entries, gin.H{
				"name": entry.Name, "kind": kind, "size": entry.Size,
			})
		}
		out["entries"] = entries
	}
	writeJSON(c, http.StatusOK, out)
}

// linkUnlock answers POST /s/{token}/auth, which is how a visitor answers the
// password.
//
// A wrong password answers the same way whether the link is locked or not, so
// the endpoint does not report which links have passwords.
func (e *Engine) linkUnlock(c *gin.Context) {
	link, _, err := e.Core.LinkPublic(c.Request.Context(), c.Param("token"))
	if err != nil {
		fail(c, err)
	}

	ip := clientAddr(c)
	limiterKey := ip + "/" + strconv.FormatInt(link.ID, 10)
	if e.linkLimiter != nil && !e.linkLimiter.Allow(limiterKey) {
		refuse(c, apierr.Classified{Class: apierr.RateLimited, Key: "auth.rate_limited"})
	}

	var req struct {
		Password string `json:"password"`
	}
	if derr := decodeBody(c, &req); derr != nil {
		refuse(c, apierr.Classified{Class: apierr.Malformed})
	}

	ok, cerr := e.Core.LinkCheckPassword(c.Request.Context(), link, req.Password)
	if cerr != nil {
		fail(c, cerr)
	}
	if !ok {
		if e.Auth != nil {
			if aerr := e.Auth.Audit(c.Request.Context(), nil, "link.unlock", fmt.Sprintf("link:%d", link.ID), ip, c.GetHeader("User-Agent"), false); aerr != nil {
				e.logger.Warn("recording link unlock failure to audit log failed", "error", aerr)
			}
		}
		refuse(c, linkPasswordRefusal())
	}

	if e.Auth != nil {
		if aerr := e.Auth.Audit(c.Request.Context(), nil, "link.unlock", fmt.Sprintf("link:%d", link.ID), ip, c.GetHeader("User-Agent"), true); aerr != nil {
			e.logger.Warn("recording link unlock success to audit log failed", "error", aerr)
		}
	}
	hash, herr := e.State.PasswordHash(c.Request.Context(), link.ID)
	if herr != nil || hash == nil {
		fail(c, core.ErrNotFound)
	}

	now := e.clk().Nanos()
	expiresNs := now + int64(24*time.Hour)
	if link.Expires > 0 && link.Expires < expiresNs {
		expiresNs = link.Expires
	}

	ticket := linkTicket(e.claimKey.Key, link.ID, *hash, expiresNs)

	http.SetCookie(c.Writer, &http.Cookie{
		Name:     linkCookie(link.ID),
		Value:    ticket,
		Path:     publicLinkCookiePath(c, c.Param("token")),
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
	c.Status(http.StatusNoContent)
}

// linkDownload answers GET /s/{token}/download, serving the bytes.
//
// Reached by navigation, which is why the password is a cookie and not a
// header: a browser following a download URL sends neither a body nor a
// header anybody here chose.
func (e *Engine) linkDownload(c *gin.Context) {
	link, lerr := e.linkFor(c)
	if lerr != nil {
		return
	}
	if !link.Perms.Has(acl.Download) {
		refuse(c, apierr.Classified{Class: apierr.Denied, Key: "fs.link_no_download"})
		return
	}
	entry, stream, serr := e.Core.LinkStreamAt(c.Request.Context(), link, c.Query("path"), nil)
	if serr != nil {
		fail(c, serr)
		return
	}
	if nerr := e.Core.NoteLinkDownload(c.Request.Context(), link); nerr != nil {
		e.closeStream(stream, entry.Name)
		fail(c, nerr)
		return
	}
	length, lerr := num.Narrow[int64](stream.Remaining())
	if lerr != nil {
		e.closeStream(stream, entry.Name)
		fail(c, core.ErrNotFound)
		return
	}
	c.Header("Content-Type", "application/octet-stream")
	c.Header("Content-Length", strconv.FormatInt(length, 10))
	c.Header("Content-Disposition", httpheader.Attachment(entry.Name))
	c.Status(http.StatusOK)
	if _, err := io.CopyN(c.Writer, &loggedStream{inner: stream, name: entry.Name, logger: e.logger}, length); err != nil && !errors.Is(err, io.EOF) {
		e.logger.Warn("copying a public link download ended early", "name", entry.Name, "error", err)
	}
}

// linkZip answers GET /s/{token}/zip, packing a shared folder.
//
// The response commits before the walk finishes, so hitting a ceiling halfway
// cannot turn into an error status. The archive is finished off as-is instead:
// a short zip that opens beats a truncated stream that does not.
func (e *Engine) linkZip(c *gin.Context) {
	link, lerr := e.linkFor(c)
	if lerr != nil {
		return
	}
	if !link.Perms.Has(acl.Download) {
		refuse(c, apierr.Classified{Class: apierr.Denied, Key: "fs.link_no_download"})
		return
	}
	sub := c.Query("path")
	if strings.Trim(sub, "/") != "" && !link.Perms.Has(acl.Read) {
		fail(c, core.ErrNotFound)
		return
	}
	listing, lerr := e.Core.LinkBrowse(c.Request.Context(), link, sub)
	if lerr != nil {
		fail(c, lerr)
		return
	}
	if !listing.IsDir {
		refuse(c, apierr.Classified{Class: apierr.Unprocessable, Key: "fs.link_not_a_folder"})
		return
	}
	release, ok := e.tryAcquireArchive()
	if !ok {
		refuse(c, archiveBusy())
		return
	}
	defer release()
	if nerr := e.Core.NoteLinkDownload(c.Request.Context(), link); nerr != nil {
		fail(c, nerr)
		return
	}
	c.Header("Content-Type", "application/zip")
	c.Header("Content-Disposition", httpheader.Attachment(listing.Name+".zip"))
	c.Status(http.StatusOK)
	e.writeLinkArchive(context.WithoutCancel(c.Request.Context()), c.Writer, link, sub, listing.Name)
}

// writeLinkArchive builds a link's zip into a committed response.
//
// Bounded by the same ceilings as authenticated archive downloads: a link is
// reachable by anyone holding the address, and a visible marker records when
// a walk cannot include every requested entry.
func (e *Engine) writeLinkArchive(
	ctx context.Context, w io.Writer, link core.Link, sub, name string,
) {
	z := archive.NewWriter(w)
	builder := archiveBuilder{z: z}
	walkErr := e.Core.LinkArchiveWalk(ctx, link, sub,
		func(entry core.WalkEntry, stream *core.Stream) error {
			return builder.add(entry, stream)
		})
	if walkErr != nil {
		builder.incomplete = true
		e.logger.Warn("a link archive ended early", "name", name, "error", walkErr)
	}
	if merr := builder.addMarker(); merr != nil {
		e.logger.Warn("adding the incomplete archive marker failed", "name", name, "error", merr)
	}

	// Closed regardless, because a zip without its central directory is not a
	// zip: the bytes already sent are unreadable without it.
	if cerr := z.Close(); cerr != nil {
		e.logger.Warn("closing a link archive", "name", name, "error", cerr)
	}
	// The native response writer has no buffered Flush requirement here.
}

// linkDrop answers POST /s/{token}/drop, the upload half of a link.
//
// Create without Read is what makes a link a collection box. The check below
// is not defending the route from misuse; it is the thing that distinguishes
// this kind of link from the ordinary kind.
func (e *Engine) linkDrop(c *gin.Context) {
	link, lerr := e.linkFor(c)
	if lerr != nil {
		return
	}
	if !link.Perms.Has(acl.Create) {
		refuse(c, apierr.Classified{Class: apierr.Denied, Key: "fs.link_no_upload"})
		return
	}
	name := c.Query("name")
	if name == "" {
		refuse(c, apierr.Classified{Class: apierr.Unprocessable, Key: "fs.link_no_name"})
		return
	}
	if cl := c.GetHeader("Content-Length"); cl != "" {
		if n, err := strconv.ParseInt(cl, 10, 64); err == nil && n > limits.RequestBody {
			refuse(c, apierr.Classified{Class: apierr.BodyTooLarge, Key: "http.body_too_large"})
			return
		}
	}
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, limits.RequestBody+1))
	if err != nil {
		fail(c, err)
		return
	}
	if len(body) > limits.RequestBody {
		refuse(c, apierr.Classified{Class: apierr.BodyTooLarge, Key: "http.body_too_large"})
		return
	}
	entry, werr := e.Core.LinkDropFile(c.Request.Context(), link, name, bytesReader(body))
	if werr != nil {
		fail(c, werr)
		return
	}
	writeJSON(c, http.StatusCreated, gin.H{"name": entry.Name, "size": strconv.FormatUint(entry.Size, 10)})
}

// bytesReader adapts a request body for the core's streaming writer.
func bytesReader(b []byte) io.Reader { return &byteSliceReader{b: b} }

type byteSliceReader struct {
	b   []byte
	off int
}

func (r *byteSliceReader) Read(p []byte) (int, error) {
	if r.off >= len(r.b) {
		return 0, io.EOF
	}
	n := copy(p, r.b[r.off:])
	r.off += n
	return n, nil
}
