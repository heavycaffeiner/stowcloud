//go:build linux

package core

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/secret"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/state"
)

// A link stores both a hash and an encrypted copy of its token. The sha256
// authenticates public requests, so a bearer is verified without the master
// key being loaded; the ciphertext lets the owner list the full URL again.
// Legacy rows without ciphertext stay unrecoverable: the owner sees the link
// exists and nothing invents a URL for it. Revocation is permanent.

// Link presents a public link as callers above the core see it. No password
// material appears, only whether one exists.
type Link struct {
	ID int64
	// Token is the plaintext, present only when it could be recovered. A
	// pointer because nil is the unrecoverable-legacy case, which a value
	// type cannot carry.
	Token   *secret.Secret
	Share   ShareID
	Path    vfs.SharePath
	Owner   UserID
	Perms   acl.Perms
	Expires int64 // ns epoch, zero when never
	MaxDown int32 // -1 when unlimited
	Downs   int32
	Label   string
	Note    string
	// HasPassword is the sole password-related fact this package exposes.
	HasPassword bool
	CreatedNs   int64
	// TokenHash holds the sha256 authenticating public requests.
	TokenHash []byte

	// The identity pinned at creation, when one could be allocated. A nil
	// dev means the link is path-only.
	dev   *int64
	ino   *int64
	btime *int64
}

// IsDrop reports a file-drop link: Create with neither Read nor Download, so
// the holder can write and can never list.
func (l Link) IsDrop() bool {
	return l.Perms.Has(acl.Create) && !l.Perms.Intersects(acl.Read|acl.Download)
}

// IsExpired answers against the wall clock.
func (l Link) IsExpired(now int64) bool { return l.Expires != 0 && l.Expires <= now }

// IsExhausted reports the download cap reached.
func (l Link) IsExhausted() bool { return l.MaxDown >= 0 && l.Downs >= l.MaxDown }

// Dev reports the pinned device, non-nil when this link carries an identity.
func (l Link) Dev() *int64 { return l.dev }

// LinkSpec describes what CreateLink is asked to produce.
type LinkSpec struct {
	Perms acl.Perms
	// Password, when set, is hashed before it touches the row.
	Password *string
	Expires  int64 // ns epoch, zero when never
	MaxDown  int32 // -1 when unlimited
	Label    string
	Note     string
}

// LinkPatch is what an update may change.
//
// The double pointer is the tri-state a patch needs. An outer nil leaves the
// field alone, which lets a screen edit one thing without resetting the rest.
// An outer non-nil with an inner nil clears the field. "Leave it" and "remove
// it" are different requests and both need a spelling.
type LinkPatch struct {
	Perms    *acl.Perms
	Password **string
	Expires  **int64
	MaxDown  **int32
	Label    *string
	Note     *string
}

// LinkRow and LinkRowPatch are the row shapes crossing the store boundary.
// They alias the store's own types rather than restating them: the schema's
// owner owns its row shape, and the alias is what lets the store satisfy the
// interface below with no conversion on either side.
type (
	LinkRow      = state.LinkRow
	LinkRowPatch = state.LinkRowPatch
)

// LinkStore is the persistence seam. Every share_link statement and row scan
// lives behind it, in the store layer that owns the schema.
type LinkStore interface {
	Insert(ctx context.Context, row LinkRow) (int64, error)
	ByID(ctx context.Context, id int64) (LinkRow, bool, error)
	ByHash(ctx context.Context, tokenHash []byte) (LinkRow, bool, error)
	ListByOwner(ctx context.Context, owner int64) ([]LinkRow, error)
	Delete(ctx context.Context, id, owner int64) error
	ConsumeDownload(ctx context.Context, id int64) (bool, error)
	PasswordHash(ctx context.Context, id int64) (*string, error)
	Update(ctx context.Context, id int64, patch LinkRowPatch) error
	KeyVersion(ctx context.Context) (uint32, error)
}

// LinkCipher is the at-rest cryptography for tokens. An interface so a Core
// can hold a zero value that refuses to seal until the server wires the
// master key. The ciphertext format is the auth package's, defined in one
// place, so a key rotation keeps opening what it re-seals.
type LinkCipher interface {
	Seal(token, tokenHash []byte, keyVer uint32) ([]byte, error)
	Open(blob, tokenHash []byte, keyVer uint32) ([]byte, error)
}

// passwordHasher owns the parameters, the concurrency gate and the format the
// verifier reads, so it lives with the auth service rather than here.
type passwordHasher func(ctx context.Context, plain string) (string, error)

// passwordVerifier is the other half.
type passwordVerifier func(ctx context.Context, enc, candidate string) (bool, error)

// AttachLinkCrypto wires the token cipher and the password path. One call
// because they are one feature: links that cannot open their own tokens, hash
// their own passwords or verify them are not a partial feature.
func (c *Core) AttachLinkCrypto(cipher LinkCipher, hash passwordHasher, verify passwordVerifier) {
	c.linkCipher = cipher
	c.hashLinkPw = hash
	c.verifyLinkPw = verify
}

// Each seam fails closed with a plain error rather than a panic or a silent
// fallback. The checks live here rather than at the call sites because the
// failure without them is a crash in the middle of minting, and because the
// fallback nobody notices on the password path is writing the plaintext down.

func (c *Core) sealLinkToken(token, hash []byte, ver uint32) ([]byte, error) {
	if c.linkCipher == nil {
		return nil, errors.New("no link cipher is attached")
	}
	return c.linkCipher.Seal(token, hash, ver)
}

func (c *Core) openLinkToken(sealed, hash []byte, ver uint32) ([]byte, error) {
	if c.linkCipher == nil {
		return nil, errors.New("no link cipher is attached")
	}
	return c.linkCipher.Open(sealed, hash, ver)
}

func (c *Core) hashLinkPassword(ctx context.Context, plain string) (string, error) {
	if c.hashLinkPw == nil {
		return "", errors.New("no password hasher is attached")
	}
	return c.hashLinkPw(ctx, plain)
}

func (c *Core) verifyLinkPassword(ctx context.Context, enc, candidate string) (bool, error) {
	if c.verifyLinkPw == nil {
		return false, errors.New("no password verifier is attached")
	}
	return c.verifyLinkPw(ctx, enc, candidate)
}

// links is the attached store, or an error naming the wiring mistake. A Core
// built without one fails every link operation rather than panicking.
func (c *Core) links() (LinkStore, error) {
	if c.linkStore == nil {
		return nil, errors.New("no link store is attached")
	}
	return c.linkStore, nil
}

// tokenLen counts the CSPRNG bytes behind a token, rendering as 22 base64url
// characters.
const tokenLen = 16

// mintToken is 16 bytes of CSPRNG, base64url without padding.
func mintToken() (string, error) {
	var b [tokenLen]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("minting a share-link token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// linkTokenHash produces the sole token representation ever persisted.
func linkTokenHash(token []byte) []byte {
	sum := sha256.Sum256(token)
	return sum[:]
}

// CreateLink mints a link and returns its bearer secret, the one moment the
// plaintext token exists outside the hash.
func (c *Core) CreateLink(ctx context.Context, r Resolved, spec LinkSpec) (Link, secret.Secret, error) {
	store, err := c.links()
	if err != nil {
		return Link{}, secret.Secret{}, err
	}
	if err = r.Require(acl.Share); err != nil {
		return Link{}, secret.Secret{}, err
	}
	if spec.Perms.IsEmpty() {
		return Link{}, secret.Secret{}, errf(ErrDenied, "a share link must grant at least one permission")
	}
	// Escalation guard: a link is a delegation of the creator's own access,
	// so it can never carry a permission the creator lacks.
	if !r.perms.Has(spec.Perms) {
		return Link{}, secret.Secret{}, ErrDenied
	}
	if spec.Expires != 0 && spec.Expires <= c.clk.Nanos() {
		return Link{}, secret.Secret{}, errf(ErrDenied, "expiry is in the past")
	}

	st, serr := r.root.Stat(r.path)
	if serr != nil {
		return Link{}, secret.Secret{}, mapVFSErr(serr)
	}
	if spec.Perms.Has(acl.Create) && !spec.Perms.Intersects(acl.Read|acl.Download) && !st.Kind.IsDir() {
		return Link{}, secret.Secret{}, errf(ErrDenied, "a file-drop link must target a directory")
	}

	// The password is hashed before it reaches the row. An earlier version
	// stored it as it arrived and the verifier then compared candidates
	// against the plaintext as though it were a hash, so every protected link
	// refused its own password while the plaintext sat in the database.
	var pwHash *string
	if spec.Password != nil {
		h, herr := c.hashLinkPassword(ctx, *spec.Password)
		if herr != nil {
			return Link{}, secret.Secret{}, herr
		}
		pwHash = &h
	}

	token, err := mintToken()
	if err != nil {
		return Link{}, secret.Secret{}, err
	}
	hash := linkTokenHash([]byte(token))
	ver, err := store.KeyVersion(ctx)
	if err != nil {
		return Link{}, secret.Secret{}, err
	}
	sealed, err := c.sealLinkToken([]byte(token), hash, ver)
	if err != nil {
		return Link{}, secret.Secret{}, err
	}

	row := LinkRow{
		TokenHash: hash, TokenEnc: sealed, TokenKeyVer: &ver,
		Share: int64(r.share), Path: r.path.String(),
		Owner: int64(r.user), Perms: uint16(spec.Perms),
		PasswordHash: pwHash, Downloads: 0,
		CreatedNs: c.clk.Nanos(),
	}
	pinIdentity(&row, r.path, st)
	if spec.Expires != 0 {
		expires := spec.Expires
		row.ExpiresNs = &expires
	}
	if spec.MaxDown >= 0 {
		cap64 := int64(spec.MaxDown)
		row.MaxDown = &cap64
	}
	if spec.Label != "" {
		row.Label = &spec.Label
	}
	if spec.Note != "" {
		row.Note = &spec.Note
	}

	id, err := store.Insert(ctx, row)
	if err != nil {
		return Link{}, secret.Secret{}, err
	}

	tok := secret.New([]byte(token))
	return Link{
		ID: id, Token: &tok,
		Share: r.share, Path: r.path.Share(), Owner: r.user,
		Perms: spec.Perms, Expires: spec.Expires, MaxDown: spec.MaxDown,
		Label: spec.Label, Note: spec.Note,
		HasPassword: spec.Password != nil,
		CreatedNs:   row.CreatedNs, TokenHash: hash,
		dev: row.Dev, ino: row.Ino, btime: row.Btime,
	}, tok, nil
}

// pinIdentity captures the target's identity when one can be allocated. A
// root link, or a filesystem without birth times, stays path-only: the cross
// check needs a birth time to tell the original inode from a reused one.
func pinIdentity(row *LinkRow, p vfs.SafePath, st vfs.Stat) {
	if p.IsRoot() || st.BtimeNs == nil {
		return
	}
	dev, err := num.Narrow[int64](st.Dev)
	if err != nil {
		return
	}
	ino, err := num.Narrow[int64](st.Ino)
	if err != nil {
		return
	}
	btime := *st.BtimeNs
	row.Dev, row.Ino, row.Btime = &dev, &ino, &btime
}

// sameIdent checks a stat against the identity a link pinned when created. A pin
// that cannot be verified returns false, since the safe interpretation of
// "unknown" is "dead link".
func sameIdent(st vfs.Stat, l Link) bool {
	if l.dev == nil || l.ino == nil || l.btime == nil || st.BtimeNs == nil {
		return false
	}
	dev, err := num.Narrow[int64](st.Dev)
	if err != nil {
		return false
	}
	ino, err := num.Narrow[int64](st.Ino)
	if err != nil {
		return false
	}
	return dev == *l.dev && ino == *l.ino && *st.BtimeNs == *l.btime
}

// linkOf converts one stored row into the domain value, including the
// opportunistic token decryption. One function, so the trust-boundary
// validation of a row has a single home.
//
// A row whose ciphertext cannot be opened yields a nil Token rather than an
// error: the link still works, its URL is simply not recoverable.
func (c *Core) linkOf(row LinkRow) (Link, error) {
	share, err := num.Narrow[uint32](row.Share)
	if err != nil {
		return Link{}, fmt.Errorf("share link %d names share %d: %w", row.ID, row.Share, err)
	}
	path, err := vfs.ParseSharePath(row.Path)
	if err != nil {
		return Link{}, fmt.Errorf("share link %d holds an unusable path: %w", row.ID, err)
	}
	downs, err := num.Narrow[int32](row.Downloads)
	if err != nil {
		return Link{}, fmt.Errorf("share link %d counts %d downloads: %w", row.ID, row.Downloads, err)
	}

	l := Link{
		ID: row.ID, Share: ShareID(share), Path: path,
		Owner: UserID(row.Owner), Perms: acl.Perms(row.Perms),
		Downs: downs, HasPassword: row.PasswordHash != nil,
		CreatedNs: row.CreatedNs, TokenHash: row.TokenHash,
		dev: row.Dev, ino: row.Ino, btime: row.Btime,
	}
	if row.ExpiresNs != nil {
		l.Expires = *row.ExpiresNs
	}
	// A missing cap is unlimited, which the domain spells as -1.
	l.MaxDown = -1
	if row.MaxDown != nil {
		capped, cerr := num.Narrow[int32](*row.MaxDown)
		if cerr != nil {
			return Link{}, fmt.Errorf("share link %d caps at %d: %w", row.ID, *row.MaxDown, cerr)
		}
		l.MaxDown = capped
	}
	if row.Label != nil {
		l.Label = *row.Label
	}
	if row.Note != nil {
		l.Note = *row.Note
	}

	if row.TokenEnc != nil && row.TokenKeyVer != nil && c.linkCipher != nil {
		if plain, oerr := c.openLinkToken(row.TokenEnc, row.TokenHash, *row.TokenKeyVer); oerr == nil {
			tok := secret.New(plain)
			l.Token = &tok
		}
	}
	return l, nil
}

// ListLinks returns every link the owner holds, ordered by id, optionally
// narrowed to links against one resolved target.
func (c *Core) ListLinks(ctx context.Context, owner UserID, at *Resolved) ([]Link, error) {
	store, err := c.links()
	if err != nil {
		return nil, err
	}
	rows, err := store.ListByOwner(ctx, int64(owner))
	if err != nil {
		return nil, err
	}
	out := make([]Link, 0, len(rows))
	for _, row := range rows {
		l, cerr := c.linkOf(row)
		if cerr != nil {
			return nil, cerr
		}
		if at != nil && (l.Share != at.share || l.Path.String() != at.path.String()) {
			continue
		}
		out = append(out, l)
	}
	return out, nil
}

// GetLink returns one link by id, scoped to its owner.
//
// A missing row and another owner's row are one answer, so an id-probing
// client learns nothing about which ids exist.
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

// linkByID is the ownership-less lookup the bearer surfaces use, where
// possession of the token is the authorization.
func (c *Core) linkByID(ctx context.Context, id int64) (Link, bool, error) {
	store, err := c.links()
	if err != nil {
		return Link{}, false, err
	}
	row, ok, err := store.ByID(ctx, id)
	if err != nil || !ok {
		return Link{}, false, err
	}
	l, err := c.linkOf(row)
	if err != nil {
		return Link{}, false, err
	}
	return l, true, nil
}

// resolveLink locates a link from its public token. Hashing happens before the
// token reaches any query, and this is the only place a plaintext one is
// accepted.
func (c *Core) resolveLink(ctx context.Context, token string) (Link, bool, error) {
	store, err := c.links()
	if err != nil {
		return Link{}, false, err
	}
	row, ok, err := store.ByHash(ctx, linkTokenHash([]byte(token)))
	if err != nil || !ok {
		return Link{}, false, err
	}
	l, err := c.linkOf(row)
	if err != nil {
		return Link{}, false, err
	}
	return l, true, nil
}

// UpdateLink modifies a live link.
//
// Broadening permissions revalidates against the creator's present access rather
// than their access when the link was created. A grant revoked in the interim
// must not be restored through an update; the update is exactly when to ask
// again.
func (c *Core) UpdateLink(ctx context.Context, owner UserID, id int64, patch LinkPatch) (Link, error) {
	store, err := c.links()
	if err != nil {
		return Link{}, err
	}
	link, err := c.GetLink(ctx, owner, id)
	if err != nil {
		return Link{}, err
	}

	var rowPatch LinkRowPatch
	if patch.Perms != nil {
		if patch.Perms.IsEmpty() {
			return Link{}, errf(ErrDenied, "a share link must grant at least one permission")
		}
		vp, verr := c.VpathFor(owner, link.Share, link.Path)
		if verr != nil {
			return Link{}, verr
		}
		// Resolving asks the evaluator what this account may do there right
		// now, which is the whole point of re-checking.
		r, rerr := c.Resolve(owner, vp, acl.Share)
		if rerr != nil {
			return Link{}, rerr
		}
		if !r.perms.Has(*patch.Perms) {
			return Link{}, ErrDenied
		}
		perms := uint16(*patch.Perms)
		rowPatch.Perms = &perms
	}
	if patch.Expires != nil && *patch.Expires != nil && **patch.Expires <= c.clk.Nanos() {
		return Link{}, errf(ErrDenied, "expiry is in the past")
	}

	if patch.Password != nil {
		var hashed *string
		if *patch.Password != nil {
			// Hashed here for the same reason as at creation: what reaches
			// the row is never the plaintext.
			h, herr := c.hashLinkPassword(ctx, **patch.Password)
			if herr != nil {
				return Link{}, herr
			}
			hashed = &h
		}
		rowPatch.PasswordHash = &hashed
	}
	if patch.Expires != nil {
		rowPatch.ExpiresNs = patch.Expires
	}
	if patch.MaxDown != nil {
		var capped *int64
		if *patch.MaxDown != nil {
			v := int64(**patch.MaxDown)
			capped = &v
		}
		rowPatch.MaxDown = &capped
	}
	rowPatch.Label, rowPatch.Note = patch.Label, patch.Note

	if err := store.Update(ctx, id, rowPatch); err != nil {
		return Link{}, err
	}
	return c.GetLink(ctx, owner, id)
}

// DeleteLink revokes a link permanently.
//
// Ownership is checked first, then the owner rides in the delete predicate as
// well, so the check and the delete cannot disagree. Nothing resurrects a
// token: a later link on the same target is a new one.
func (c *Core) DeleteLink(ctx context.Context, owner UserID, id int64) error {
	store, err := c.links()
	if err != nil {
		return err
	}
	if _, err := c.GetLink(ctx, owner, id); err != nil {
		return err
	}
	return store.Delete(ctx, id, int64(owner))
}

// NoteLinkDownload consumes one download against the cap, atomically.
//
// The conditional UPDATE in the store is the whole mechanism: a read then
// write lets N concurrent requests all observe room under the cap and all
// proceed. A transfer that dies after the consume still counts, because the
// cap bounds attempts rather than completions.
func (c *Core) NoteLinkDownload(ctx context.Context, link Link) error {
	store, err := c.links()
	if err != nil {
		return err
	}
	consumed, err := store.ConsumeDownload(ctx, link.ID)
	if err != nil {
		return err
	}
	if consumed {
		return nil
	}
	// Zero affected rows is ambiguous by design, so it is disambiguated here:
	// a vanished row is absent, a present one has reached its cap.
	_, ok, err := c.linkByID(ctx, link.ID)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotFound
	}
	return ErrLinkExpired
}
