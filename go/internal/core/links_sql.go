//go:build linux

package core

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"

	"github.com/heavycaffeiner/stowcloud/go/internal/acl"
	"github.com/heavycaffeiner/stowcloud/go/internal/num"
	"github.com/heavycaffeiner/stowcloud/go/internal/secret"
	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
)

// The at-rest seam and the SQL for share links. The ciphertext format is
// owned by auth (nonce || ciphertext, XChaCha20-Poly1305, AAD binding the
// token hash and key version); this package just carries a cipher a server
// wired from the loaded master key, so the format is defined in exactly one
// place and a rotation keeps opening what it re-seals.

// Every statement against the share_link table, as a constant. The columns
// match state.db's share_link exactly: hash and encrypted token side by side
// so public access never depends on decryption.

const (
	sqlInsertShareLink = `
INSERT INTO share_link(token_hash, token_enc, token_key_ver, share, path,
                       dev, ino, btime_present, btime_ns,
                       owner, perms, password_hash, expires_ns, max_downloads,
                       downloads, label, note, created_ns)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?)`

	sqlListLinksOwner = `
SELECT id, token_hash, token_enc, token_key_ver, share, path,
       dev, ino, btime_present, btime_ns,
       owner, perms, password_hash, expires_ns, max_downloads,
       downloads, label, note, created_ns
FROM share_link WHERE owner = ? ORDER BY id`

	sqlLinkByID = `
SELECT id, token_hash, token_enc, token_key_ver, share, path,
       dev, ino, btime_present, btime_ns,
       owner, perms, password_hash, expires_ns, max_downloads,
       downloads, label, note, created_ns
FROM share_link WHERE id = ?`

	sqlLinkByHash = `
SELECT id, token_hash, token_enc, token_key_ver, share, path,
       dev, ino, btime_present, btime_ns,
       owner, perms, password_hash, expires_ns, max_downloads,
       downloads, label, note, created_ns
FROM share_link WHERE token_hash = ?`

	sqlDeleteLink = `DELETE FROM share_link WHERE id = ? AND owner = ?`

	sqlLinkPassword = `SELECT password_hash FROM share_link WHERE id = ?`

	sqlNoteLinkDownload = `
UPDATE share_link
SET downloads = downloads + 1
WHERE id = ? AND (max_downloads IS NULL OR downloads < max_downloads)`

	sqlLinkKeyVer = `SELECT ver FROM key_version WHERE id = 1`
)

// linkKeyVer is the key version stamped on every token this core mints. It is
// read from the durable key_version row once per creation, which the auth
// package keeps in step with the key ring.
func (c *Core) linkKeyVer(ctx context.Context) (uint32, error) {
	var ver int64
	err := c.state.SQL().QueryRowContext(ctx, sqlLinkKeyVer).Scan(&ver)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	v, nerr := num.Narrow[uint32](ver)
	if nerr != nil {
		return 0, fmt.Errorf("the stored key version does not fit: %w", nerr)
	}
	return v, nil
}

// LinkCipher is the at-rest cryptography, satisfied by auth.LinkCipher. It is
// an interface here so a Core can hold a zero value that refuses to seal
// until the server wires a real key.
type LinkCipher interface {
	Seal(token, tokenHash []byte, keyVer uint32) ([]byte, error)
	Open(blob, tokenHash []byte, keyVer uint32) ([]byte, error)
}

// passwordVerifier is the Argon2 path for link passwords, satisfied by a
// callback the server wires from the auth service. A nil verifier means link
// passwords are unsupported, which fails a check rather than silently passing
// one.
type passwordVerifier func(ctx context.Context, enc, candidate string) (bool, error)

// passwordHasher is the other half. A link password has to be hashed before it
// reaches a row, and the hashing is the auth service's: it owns the parameters,
// the gate that bounds how many run at once, and the format the verifier reads.
type passwordHasher func(ctx context.Context, plain string) (string, error)

// AttachLinkCrypto wires the master-key cipher and the password path. They are
// one call because they are one feature: share links that cannot open their own
// tokens, hash their own passwords or check them are not a partial feature.
func (c *Core) AttachLinkCrypto(cipher LinkCipher, hash passwordHasher, verify passwordVerifier) {
	c.linkCipher = cipher
	c.hashLinkPw = hash
	c.verifyLinkPw = verify
}

// sealLinkToken routes to the attached cipher, failing closed when none was
// wired.
//
// The nil check is here rather than left to the call site because the failure
// without it is a panic in the middle of minting: the cipher is attached at
// startup, so a build that forgets is one where every link creation crashes
// the request rather than reporting that the feature is not wired.
func (c *Core) sealLinkToken(token []byte, hash []byte, ver uint32) ([]byte, error) {
	if c.linkCipher == nil {
		return nil, errors.New("no link cipher is attached")
	}
	return c.linkCipher.Seal(token, hash, ver)
}

// openLinkToken is the same on the way back.
func (c *Core) openLinkToken(sealed []byte, hash []byte, ver uint32) ([]byte, error) {
	if c.linkCipher == nil {
		return nil, errors.New("no link cipher is attached")
	}
	return c.linkCipher.Open(sealed, hash, ver)
}

// hashLinkPassword routes to the attached hasher, failing closed when none was
// wired rather than storing what it was handed.
//
// Failing closed matters more here than anywhere else on this path: the value
// is a password, and the fallback nobody notices is writing it down.
func (c *Core) hashLinkPassword(ctx context.Context, plain string) (string, error) {
	if c.hashLinkPw == nil {
		return "", errors.New("no password hasher is attached")
	}
	return c.hashLinkPw(ctx, plain)
}

// verifyLinkPassword routes to the attached verifier, failing closed when
// none was wired.
func (c *Core) verifyLinkPassword(ctx context.Context, enc, candidate string) (bool, error) {
	if c.verifyLinkPw == nil {
		return false, errors.New("no password verifier is attached")
	}
	return c.verifyLinkPw(ctx, enc, candidate)
}

// ---- bind helpers ----

func passwordArg(pw *string) any {
	if pw == nil {
		return nil
	}
	return *pw
}

func expiryArg(ns int64) any {
	if ns == 0 {
		return nil
	}
	return ns
}

func maxDownArg(n int32) any {
	if n < 0 {
		return nil
	}
	return n
}

func stringArg(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func newSecret(b []byte) *secret.Secret {
	s := secret.New(b)
	return &s
}

// mintToken is 16 bytes of CSPRNG, base64url without padding: 22 characters.
func mintToken() (string, error) {
	var b [tokenLen]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("minting a share-link token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// linkTokenHash is the only representation of a token ever written down.
func linkTokenHash(token []byte) []byte {
	sum := sha256.Sum256(token)
	return sum[:]
}

// scanner is the minimal row surface both *sql.Row and *sql.Rows satisfy.
type scanner interface {
	Scan(...any) error
}

// scanLink reads one share_link row into a Link.
func (c *Core) scanLink(row scanner) (Link, error) {
	var (
		l            Link
		tokenEnc     []byte
		keyVer       sql.NullInt64
		dev, ino     sql.NullInt64
		btime        sql.NullInt64
		present      sql.NullInt64
		share, ownr  int64
		perms        int64
		path         string
		label, note  sql.NullString
		expires      sql.NullInt64
		maxDown      sql.NullInt64
		downs, cretd int64
		pw           sql.NullString
	)
	err := row.Scan(&l.ID, &l.TokenHash, &tokenEnc, &keyVer,
		&share, &path, &dev, &ino, &present, &btime,
		&ownr, &perms, &pw, &expires, &maxDown,
		&downs, &label, &note, &cretd)
	if err != nil {
		return Link{}, err
	}
	sh, snerr := num.Narrow[uint32](share)
	if snerr != nil {
		return Link{}, fmt.Errorf("a share link carries a share id that does not fit: %w", snerr)
	}
	l.Share = ShareID(sh)
	p, perr := vfs.ParseSharePath(path)
	if perr != nil {
		return Link{}, fmt.Errorf("a share link carries a path this server would refuse: %w", perr)
	}
	l.Path = p
	l.Owner = UserID(ownr)
	pm, pmerr := num.Narrow[uint16](perms)
	if pmerr != nil {
		return Link{}, fmt.Errorf("a share link carries a permission set that does not fit: %w", pmerr)
	}
	l.Perms = acl.Perms(pm)
	l.Expires = expires.Int64
	l.MaxDown = -1
	if maxDown.Valid {
		md, merr := num.Narrow[int32](maxDown.Int64)
		if merr != nil {
			return Link{}, fmt.Errorf("a share link carries a download cap that does not fit: %w", merr)
		}
		l.MaxDown = md
	}
	dn, derr := num.Narrow[int32](downs)
	if derr != nil {
		return Link{}, fmt.Errorf("a share link carries a download count that does not fit: %w", derr)
	}
	l.Downs = dn
	l.Label = label.String
	l.Note = note.String
	l.CreatedNs = cretd
	l.HasPassword = pw.Valid
	if dev.Valid && ino.Valid && present.Valid && present.Int64 == 1 && btime.Valid {
		// A pinned identity always carries a birth time, which is what
		// distinguishes the original inode from one reused after a delete.
		d, i, b := dev.Int64, ino.Int64, btime.Int64
		l.dev, l.ino, l.btime = &d, &i, &b
	}
	if tokenEnc != nil && keyVer.Valid && c.linkCipher != nil {
		kv, kverr := num.Narrow[uint32](keyVer.Int64)
		if kverr == nil {
			if tok, oerr := c.openLinkToken(tokenEnc, l.TokenHash, kv); oerr == nil {
				l.Token = newSecret(tok)
			}
		}
	}
	return l, nil
}

// The update statements, one column each. One statement per field rather than
// a built one: a statement assembled from the fields a patch happens to carry
// is a statement whose text depends on input, which is what every statement
// here being a constant exists to prevent.
const (
	sqlUpdateLinkPerms    = `UPDATE share_link SET perms = ? WHERE id = ?`
	sqlUpdateLinkPassword = `UPDATE share_link SET password_hash = ? WHERE id = ?` //nolint:gosec // G101 reads the identifier: this is a statement, not a credential.
	sqlUpdateLinkExpiry   = `UPDATE share_link SET expires_ns = ? WHERE id = ?`
	sqlUpdateLinkMaxDown  = `UPDATE share_link SET max_downloads = ? WHERE id = ?`
	sqlUpdateLinkLabel    = `UPDATE share_link SET label = ? WHERE id = ?`
	sqlUpdateLinkNote     = `UPDATE share_link SET note = ? WHERE id = ?`
)
