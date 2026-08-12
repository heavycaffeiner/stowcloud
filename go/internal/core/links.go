//go:build linux

package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strconv"

	"github.com/heavycaffeiner/stowcloud/go/internal/acl"
	"github.com/heavycaffeiner/stowcloud/go/internal/secret"
	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
)

// Share links. A link stores both a hash and an encrypted token: the hash
// verifies public access without decryption, and the ciphertext lets the owner
// list the URL again. Legacy rows without ciphertext remain unrecoverable.
// Revocation is permanent and the same link is never recreated.

// Link is one public link, as every caller above the core sees it. It carries
// no password material, only whether one is set.
type Link struct {
	ID      int64
	Token   *secret.Secret // the plaintext when recoverable
	Share   ShareID
	Path    vfs.SharePath
	Owner   UserID
	Perms   acl.Perms
	Expires int64 // ns epoch, zero when never
	MaxDown int32 // -1 when unlimited
	Downs   int32
	Label   string
	Note    string
	// HasPassword is the one fact about passwords that leaves this package.
	HasPassword bool
	CreatedNs   int64
	// TokenHash is the sha256 that authenticates public requests.
	TokenHash []byte

	// The identity pinned at creation, when one could be allocated. New
	// non-root links always carry a birth time; a link without one predates
	// the rule and keeps a path-only check. nil dev means path-only.
	dev   *int64
	ino   *int64
	btime *int64
}

// IsDrop reports a file-drop link: CREATE with no READ and no DOWNLOAD, so the
// holder can write and cannot list.
func (l Link) IsDrop() bool {
	return l.Perms.Has(acl.Create) && !l.Perms.Intersects(acl.Read|acl.Download)
}

// IsExpired reports the wall-clock answer.
func (l Link) IsExpired(now int64) bool {
	return l.Expires != 0 && l.Expires <= now
}

// IsExhausted reports the cap reached.
func (l Link) IsExhausted() bool {
	return l.MaxDown >= 0 && l.Downs >= l.MaxDown
}

// LinkSpec is what CreateLink is asked to mint.
type LinkSpec struct {
	Perms acl.Perms
	// Password, when set, is hashed with Argon2id before it touches the row.
	Password *string
	Expires  int64 // ns epoch, zero when never
	MaxDown  int32 // -1 when unlimited
	Label    string
	Note     string
}

// tokenLen is the CSPRNG bytes behind a token, which is 22 base64url chars.
const tokenLen = 16

// CreateLink mints a link and returns its bearer secret. The store keeps a
// verification hash and an encrypted copy, so the owner can list the URL again
// without making public access depend on decryption.
//
// requires SHARE at the target, and the link can never carry permissions the
// creator does not hold there.
func (c *Core) CreateLink(ctx context.Context, r Resolved, spec LinkSpec) (Link, secret.Secret, error) {
	if err := r.Require(acl.Share); err != nil {
		return Link{}, secret.Secret{}, err
	}
	if spec.Perms.IsEmpty() {
		return Link{}, secret.Secret{}, errf(ErrDenied, "a share link must grant at least one permission")
	}
	// Escalation guard: a link is a delegation of the creator's own access.
	if !r.perms.Has(spec.Perms) {
		return Link{}, secret.Secret{}, ErrDenied
	}
	if spec.Expires != 0 && spec.Expires <= c.clk.Nanos() {
		return Link{}, secret.Secret{}, errf(ErrDenied, "expiry is in the past")
	}

	st, err := r.root.Stat(r.path)
	if err != nil {
		return Link{}, secret.Secret{}, mapVFSErr(err)
	}
	if spec.Perms.Has(acl.Create) && !spec.Perms.Intersects(acl.Read|acl.Download) && !st.Kind.IsDir() {
		return Link{}, secret.Secret{}, errf(ErrDenied, "a file-drop link must target a directory")
	}

	// The target identity, when one can be allocated at creation. A root link
	// (or one the filesystem cannot give a birth time for) stays path-only,
	// exactly as the reference does: the cross-check needs a birth time to
	// distinguish the original inode from one reused after a delete.
	var dev, ino, present, btime any
	if !r.path.IsRoot() && st.BtimeNs != nil {
		dev = int64(st.Dev)
		ino = int64(st.Ino)
		present, btime = int64(1), *st.BtimeNs
	}

	token, err := mintToken()
	if err != nil {
		return Link{}, secret.Secret{}, err
	}
	th := linkTokenHash([]byte(token))
	ver, err := c.linkKeyVer(ctx)
	if err != nil {
		return Link{}, secret.Secret{}, err
	}
	sealed, err := c.linkCipher.Seal([]byte(token), th, ver)
	if err != nil {
		return Link{}, secret.Secret{}, err
	}

	created := c.clk.Nanos()
	var id int64
	err = c.state.Write(ctx, func(tx *sql.Tx) error {
		res, ierr := tx.ExecContext(ctx, sqlInsertShareLink,
			th, sealed, ver, int64(r.share), r.path.String(),
			dev, ino, present, btime, int64(r.user), int64(spec.Perms),
			passwordArg(spec.Password), expiryArg(spec.Expires), maxDownArg(spec.MaxDown),
			stringArg(spec.Label), stringArg(spec.Note), created)
		if ierr != nil {
			return ierr
		}
		var rerr error
		id, rerr = res.LastInsertId()
		return rerr
	})
	if err != nil {
		return Link{}, secret.Secret{}, fmt.Errorf("storing a share link: %w", err)
	}

	tok := newSecret([]byte(token))
	return Link{
		ID:          id,
		Token:       tok,
		Share:       r.share,
		Path:        r.path.Share(),
		Owner:       r.user,
		Perms:       spec.Perms,
		Expires:     spec.Expires,
		MaxDown:     spec.MaxDown,
		Label:       spec.Label,
		Note:        spec.Note,
		HasPassword: spec.Password != nil,
		CreatedNs:   created,
		TokenHash:   th,
	}, *tok, nil
}

// ListLinks returns every link the owner holds, optionally narrowed to one
// resolved path.
func (c *Core) ListLinks(ctx context.Context, owner UserID, at *Resolved) ([]Link, error) {
	cmp := func(l Link) bool { return true }
	if at != nil {
		cmp = func(l Link) bool {
			return l.Share == at.share && l.Path.String() == at.path.String()
		}
	}
	rows, err := c.state.SQL().QueryContext(ctx, sqlListLinksOwner, int64(owner))
	if err != nil {
		return nil, fmt.Errorf("listing share links: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Link
	for rows.Next() {
		l, serr := c.scanLink(rows)
		if serr != nil {
			return nil, serr
		}
		if cmp(l) {
			out = append(out, l)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// GetLink returns one link by id, scoped to its owner. Another owner's link is
// NotFound, so an id-probing client learns nothing.
func (c *Core) GetLink(ctx context.Context, owner UserID, id int64) (Link, error) {
	l, ok, err := c.linkByID(ctx, id)
	if err != nil {
		return Link{}, err
	}
	if !ok || l.Owner != owner {
		return Link{}, ErrNotFound
	}
	return l, nil
}

// linkByID is the ownership-less lookup the public surface uses; possession of
// the token is the authorization.
func (c *Core) linkByID(ctx context.Context, id int64) (Link, bool, error) {
	row := c.state.SQL().QueryRowContext(ctx, sqlLinkByID, id)
	l, err := c.scanLink(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Link{}, false, nil
	}
	if err != nil {
		return Link{}, false, err
	}
	return l, true, nil
}

// resolveLink finds a link by its public token. The token is hashed before it
// touches the query, which is the only place a plaintext is ever accepted.
func (c *Core) resolveLink(ctx context.Context, token string) (Link, bool, error) {
	row := c.state.SQL().QueryRowContext(ctx, sqlLinkByHash, linkTokenHash([]byte(token)))
	l, err := c.scanLink(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Link{}, false, nil
	}
	if err != nil {
		return Link{}, false, err
	}
	return l, true, nil
}

// DeleteLink revokes a link permanently. Ownership is checked first, so
// deleting somebody else's link is NotFound. A revoked link is never recreated:
// the token stays dead even if a later link reuses the target.
func (c *Core) DeleteLink(ctx context.Context, owner UserID, id int64) error {
	if _, err := c.GetLink(ctx, owner, id); err != nil {
		return err
	}
	return c.state.Write(ctx, func(tx *sql.Tx) error {
		_, ierr := tx.ExecContext(ctx, sqlDeleteLink, id, int64(owner))
		return ierr
	})
}

// NoteLinkDownload consumes one download against a link's cap, atomically. The
// conditional UPDATE is the whole mechanism: read-then-write lets N concurrent
// requests all observe room and all proceed, and a dead transfer still counts.
func (c *Core) NoteLinkDownload(ctx context.Context, link Link) error {
	var n int64
	err := c.state.Write(ctx, func(tx *sql.Tx) error {
		res, ierr := tx.ExecContext(ctx, sqlNoteLinkDownload, link.ID)
		if ierr != nil {
			return ierr
		}
		var rerr error
		n, rerr = res.RowsAffected()
		return rerr
	})
	if err != nil {
		return err
	}
	if n == 1 {
		return nil
	}
	if _, ok, e := c.linkByID(ctx, link.ID); e != nil {
		return e
	} else if !ok {
		return ErrNotFound
	}
	return ErrLinkExpired
}

// LinkPublic resolves a link's target for a bearer, enforcing every liveness
// rule: expiry, cap, and the path-plus-identity cross-check. Any failure is
// ErrLinkExpired: the link is dead, and the answer does not tell a stranger
// why.
func (c *Core) LinkPublic(ctx context.Context, token string) (Link, Entry, error) {
	l, ok, err := c.resolveLink(ctx, token)
	if err != nil {
		return Link{}, Entry{}, err
	}
	if !ok {
		return Link{}, Entry{}, ErrLinkExpired
	}
	now := c.clk.Nanos()
	if l.IsExpired(now) || l.IsExhausted() {
		return Link{}, Entry{}, ErrLinkExpired
	}
	root, ok := c.ShareRoot(l.Share)
	if !ok {
		return Link{}, Entry{}, ErrLinkExpired
	}
	p, perr := l.Path.Safe()
	if perr != nil {
		return Link{}, Entry{}, ErrLinkExpired
	}
	st, serr := root.Stat(p)
	if serr != nil {
		return Link{}, Entry{}, ErrLinkExpired
	}
	// Path-plus-identity cross-check. A rename therefore makes the link gone
	// instead of moving it; a replacement at the same path also makes it gone.
	// An unverifiable identity (row evicted, or never allocated) reads as dead,
	// because the safe reading of "cannot tell" is "dead link".
	if l.Dev() != nil && !sameIdent(st, l) {
		return Link{}, Entry{}, ErrLinkExpired
	}
	entry := c.buildEntry(Resolved{share: l.Share, root: root, path: p, perms: l.Perms},
		p.Name(), p)
	entry.Perms = l.Perms
	return l, entry, nil
}

// Dev reports a non-nil identity the link pins, for the cross-check.
func (l Link) Dev() *int64 { return l.dev }

// LinkCheckPassword verifies a password against a link. A link with no
// password accepts anything. A link that does not exist cannot be reached
// here, because a bearer only ever carries a token that resolved.
func (c *Core) LinkCheckPassword(ctx context.Context, link Link, candidate string) (bool, error) {
	var h sql.NullString
	err := c.state.SQL().QueryRowContext(ctx, sqlLinkPassword, link.ID).Scan(&h)
	if err != nil {
		return false, err
	}
	if !h.Valid {
		return true, nil
	}
	return c.verifyLinkPassword(ctx, h.String, candidate)
}

// LinkDrop accepts an upload through a link. It is the upload-only surface:
// a bearer with CREATE but no READ or DOWNLOAD can write into the link's
// directory and can never list it.
//
// Never overwrites: a name already taken gets the same "name (2).ext"
// treatment as a rename-on-conflict, so an anonymous uploader cannot destroy,
// or probe for, what somebody else already put there.
func (c *Core) LinkDrop(ctx context.Context, link Link, name string, body []byte) (Entry, error) {
	if !link.Perms.Has(acl.Create) {
		return Entry{}, ErrDenied
	}
	if link.IsExpired(c.clk.Nanos()) {
		return Entry{}, ErrLinkExpired
	}
	root, ok := c.ShareRoot(link.Share)
	if !ok {
		return Entry{}, ErrLinkExpired
	}
	dir, perr := link.Path.Safe()
	if perr != nil {
		return Entry{}, ErrLinkExpired
	}
	st, serr := root.Stat(dir)
	if serr != nil || !st.Kind.IsDir() {
		return Entry{}, ErrLinkExpired
	}

	dest := dir
	dest, jerr := dest.Join(name)
	if jerr != nil {
		return Entry{}, jerr
	}
	if p, err := pathExists(root, dest); err != nil {
		return Entry{}, err
	} else if p {
		// A name already taken gets a counting suffix rather than an
		// overwrite, which is the drop-box contract.
		uniq, uerr := c.uniqueDropName(root, dir, name)
		if uerr != nil {
			return Entry{}, uerr
		}
		dest = uniq
	}

	mode := root.Policy().ModeFile
	if _, werr := root.WriteDurable(dest, vfs.DurableOpts{Mode: mode, NoClobber: true}, func(f *vfs.File) error {
		_, cerr := f.WriteAt(body, 0)
		return cerr
	}); werr != nil {
		return Entry{}, mapVFSErr(werr)
	}
	c.markDirty(ctx, link.Share, dest)
	entry := c.buildEntry(Resolved{share: link.Share, root: root, path: dest, perms: link.Perms},
		dest.Name(), dest)
	entry.Perms = link.Perms
	return entry, nil
}

// uniqueDropName picks the next free "name (2).ext" in a directory.
func (c *Core) uniqueDropName(root *vfs.ShareRoot, dir vfs.SafePath, name string) (vfs.SafePath, error) {
	stem, ext := name, ""
	if i := lastDot(name); i >= 0 {
		stem, ext = name[:i], name[i:]
	}
	for n := 2; n < 10_000; n++ {
		candidate := stem + " (" + strconv.Itoa(n) + ")" + ext
		p, jerr := dir.Join(candidate)
		if jerr != nil {
			continue
		}
		if exists, err := pathExists(root, p); err != nil {
			return vfs.SafePath{}, err
		} else if !exists {
			return p, nil
		}
	}
	return vfs.SafePath{}, ErrConflict
}

// lastDot finds the last '.' in a name, which is where the extension starts.
func lastDot(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '.' {
			return i
		}
	}
	return -1
}

// sameIdent compares a stat against the identity a link pinned at creation.
func sameIdent(st vfs.Stat, l Link) bool {
	if l.dev == nil || l.ino == nil || l.btime == nil {
		return false
	}
	return st.Dev == uint64(*l.dev) && st.Ino == uint64(*l.ino) &&
		st.BtimeNs != nil && *st.BtimeNs == *l.btime
}
