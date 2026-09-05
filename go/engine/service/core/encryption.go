//go:build linux

package core

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/state"
)

// End-to-end encryption for a share's file content: this file validates and
// stores a client's rclone-crypt scheme, salt and verifier, never a
// passphrase or a key. This server cannot decrypt what it stores.

// SchemeRcloneCrypt is the only on-disk format this server records. Named
// rather than assumed, so a second format arrives as a new value here instead
// of as a guess about what the stored bytes are.
const SchemeRcloneCrypt = "rclone-crypt-v1"

// rcloneFileMagic is the first eight bytes of every file rclone's crypt
// backend writes. The verifier is one such file, so this is the one
// structural claim about it that can be checked without a key.
const rcloneFileMagic = "RCLONE\x00\x00"

// verifierBytes is the exact size of a verifier: rclone's 32-byte header, one
// 16-byte Poly1305 tag, and the 19 bytes of the known plaintext. A fixed size
// rather than a bound, because a verifier that is any other length was not
// produced by the agreed procedure.
const verifierBytes = 32 + 16 + 19

// saltChars is the length of the salt, which is base64url of 128 bits with no
// padding. Checking the length is what makes the entropy claim enforceable:
// the client cannot substitute a short, guessable salt.
const saltChars = 22

// Encryption is one share's client-held encryption settings. None of it is a
// secret; see the package's own note above.
type Encryption struct {
	// Scheme names the on-disk format.
	Scheme string

	// Salt is what the client passes rclone as password2. It is returned to
	// clients and shown to the user, who has to type it into their own
	// rclone configuration.
	Salt string

	// Verifier is a fixed known plaintext encrypted under the derived key,
	// which is how a client proves a typed passphrase is the right one
	// before it encrypts or decrypts anything.
	Verifier []byte

	Created int64
}

// EncryptionOf reports a share's settings. The false return is every
// unencrypted share, which is the default and needs no row.
func (c *Core) EncryptionOf(ctx context.Context, id ShareID) (Encryption, bool, error) {
	row, ok, err := c.state.ReadShareEncryption(ctx, int64(id))
	if err != nil || !ok {
		return Encryption{}, false, err
	}
	return Encryption{
		Scheme:   row.Scheme,
		Salt:     row.Salt,
		Verifier: row.Verifier,
		Created:  row.Created,
	}, true, nil
}

// ShareEncrypted is the guard every operation that would have to read a
// share's bytes asks first.
//
// It is a query rather than a cached flag on purpose. The callers are
// thumbnail generation, archive listing and archive building, each of which is
// already about to do file I/O and image or zip work, so one indexed lookup
// against a primary key costs nothing beside them, and a cache is a second
// copy of the truth that a concurrent toggle can leave stale.
func (c *Core) ShareEncrypted(ctx context.Context, id ShareID) (bool, error) {
	_, ok, err := c.EncryptionOf(ctx, id)
	return ok, err
}

// EncryptedShares lists which shares are encrypted, so a client learns the
// whole set in one request instead of asking per share before every upload.
func (c *Core) EncryptedShares(ctx context.Context) ([]ShareID, error) {
	rows, err := c.state.ListShareEncryption(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]ShareID, 0, len(rows))
	for _, r := range rows {
		// A stored id too wide for a ShareID is a corrupt row, not a share
		// to serve, and skipping it keeps one bad row from failing the whole
		// set for every other share.
		id, nerr := num.Narrow[uint32](r.Share)
		if nerr != nil {
			c.warn("a share encryption row carries an impossible share id",
				"share", r.Share, "error", nerr)
			continue
		}
		out = append(out, ShareID(id))
	}
	return out, nil
}

// EnableEncryption records a share's settings.
//
// The share must be empty. Enabling over existing files would leave plaintext
// and ciphertext side by side under one setting, with nothing on either file
// saying which it is, and no way to tell them apart later without trying to
// decrypt every one. Refusing is also what makes the setting reversible: a
// share that must be empty to turn encryption on is empty when it is turned
// off, so nothing is ever stranded as bytes nobody can read.
func (c *Core) EnableEncryption(ctx context.Context, id ShareID, e Encryption) error {
	if err := validEncryption(e); err != nil {
		return err
	}
	if err := c.requireEmptyShare(ctx, id); err != nil {
		return err
	}
	return c.state.WriteShareEncryption(ctx, state.ShareEncryptionRow{
		Share:    int64(id),
		Scheme:   e.Scheme,
		Salt:     e.Salt,
		Verifier: e.Verifier,
		Created:  c.clk.Now().UnixNano(),
	})
}

// DisableEncryption drops a share's settings.
//
// The share must be empty here too, for the reason it had to be empty to
// enable: this server cannot decrypt what is stored, so dropping the salt the
// passphrase derives with would leave the content unreadable by anyone.
func (c *Core) DisableEncryption(ctx context.Context, id ShareID) error {
	if _, ok, err := c.EncryptionOf(ctx, id); err != nil {
		return err
	} else if !ok {
		return nil
	}
	if err := c.requireEmptyShare(ctx, id); err != nil {
		return err
	}
	return c.state.DeleteShareEncryption(ctx, int64(id))
}

// validEncryption is the whole of the boundary validation, and it is short
// because nothing here is decryptable by this server: the format name, the
// salt's length and alphabet, and the verifier's exact size and magic bytes
// are every claim that can be checked without the passphrase.
func validEncryption(e Encryption) error {
	switch {
	case e.Scheme != SchemeRcloneCrypt:
		return fmt.Errorf("%w: %q is not an encryption scheme this server records",
			ErrUnprocessable, e.Scheme)
	case len(e.Salt) != saltChars:
		return fmt.Errorf("%w: the salt is %d characters, not %d",
			ErrUnprocessable, len(e.Salt), saltChars)
	case !base64URLOnly(e.Salt):
		return fmt.Errorf("%w: the salt is not base64url", ErrUnprocessable)
	case len(e.Verifier) != verifierBytes:
		return fmt.Errorf("%w: the verifier is %d bytes, not %d",
			ErrUnprocessable, len(e.Verifier), verifierBytes)
	case !strings.HasPrefix(string(e.Verifier), rcloneFileMagic):
		return fmt.Errorf("%w: the verifier is not an rclone crypt file", ErrUnprocessable)
	default:
		return nil
	}
}

// base64URLOnly reports whether s uses only the unpadded base64url alphabet.
func base64URLOnly(s string) bool {
	for i := range len(s) {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}

// requireEmptyShare refuses a share root holding anything a client can see,
// and a share whose trash still holds anything, even if the visible tree is
// empty.
//
// Reserved bookkeeping names are excluded from the visible-tree listing: a
// part file from an abandoned upload is this server's own housekeeping, not
// content whose encryption state would become ambiguous. The trash is not
// excluded the same way, and is checked on its own: a share that looks empty
// on screen can still hold a deleted file underneath, and disabling
// encryption over that trash would strand ciphertext nobody can read again,
// while enabling it would leave plaintext there for a later restore to drop
// into an encrypted share.
func (c *Core) requireEmptyShare(ctx context.Context, id ShareID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if berr := c.ShareBroken(id); berr != nil {
		return berr
	}
	root, ok := c.ShareRoot(id)
	if !ok {
		return fmt.Errorf("%w: share %d", ErrNotFound, id)
	}
	occupied := false
	err := root.ReadDirFunc(vfs.RootPath(), vfs.HideReserved, func(vfs.DirEntry) bool {
		occupied = true
		return false
	})
	if err != nil {
		return mapVFSErr(err)
	}
	if occupied {
		return fmt.Errorf(
			"%w: the share must be empty to change its encryption", ErrUnprocessable)
	}
	trash, err := vfs.RootPath().JoinControl(trashDir)
	if err != nil {
		return err
	}
	trashed := false
	err = root.ReadDirFunc(trash, vfs.IncludeReserved, func(vfs.DirEntry) bool {
		trashed = true
		return false
	})
	if err != nil && !errors.Is(err, vfs.ErrNotFound) {
		return mapVFSErr(err)
	}
	if trashed {
		return fmt.Errorf(
			"%w: the share's trash still holds files; empty the trash before changing encryption",
			ErrUnprocessable)
	}
	return nil
}
