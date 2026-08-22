package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"unicode/utf16"

	"golang.org/x/crypto/md4" //nolint:gosec,staticcheck // MD4 is fixed by the SMB protocol; the value is sealed at rest and only that algorithm matches.

	"github.com/heavycaffeiner/stowcloud/go/internal/clock"
	"github.com/heavycaffeiner/stowcloud/go/internal/secret"
	"github.com/heavycaffeiner/stowcloud/go/internal/smb"
	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
)

// The SMB passdb sink. Deleting a credential row closes SMB in the database
// and nowhere else: smbd authenticates against the last file that was
// published, so a revocation that stops at SQLite leaves the sidecar serving
// the revoked credential. Republishing is a sink every credential-changing
// path calls, not a step each one remembers.
//
// This is the one place in the product where a committed transaction is not
// yet a completed security decision.

// SMBBaseUid is what an account's row id is offset by to produce the uid its
// entry carries, in both this file and the account file beside it.
//
// The two have to agree. The import tool resolves an entry to an account
// through this uid, and imports nothing at all when it names none: no error,
// no log line, a zero exit, an empty database, and every login refused as an
// unknown user.
const SMBBaseUid = 30000

// TOTPPolicy decides what an account carrying a second factor may do over SMB.
//
// The policy decides what is published, never what is stored, so moving it back
// restores access without anyone setting a password again.
type TOTPPolicy uint8

const (
	// TOTPRequireSeparate is the default. An account under it reaches SMB with
	// the dedicated password it was told to set.
	TOTPRequireSeparate TOTPPolicy = iota
	// TOTPBlock excludes every enrolled account, whatever credential it holds.
	TOTPBlock
)

// passdbEnabled reports whether an account is eligible to appear in the SMB
// passdb: not opted out of SMB, not disabled, and not carrying a second
// factor the SMB policy blocks.
func passdbEnabled(smbEnabled, disabled, has2fa bool, policy TOTPPolicy) bool {
	if has2fa && policy == TOTPBlock {
		return false
	}
	return smbEnabled && !disabled
}

// PublishPassdb re-renders the credential file now, for the publisher that
// pushes a whole SMB configuration rather than reacting to one credential
// change.
//
// It renders and stops. Calling the sink here would be the publisher asking
// this service for the file and this service asking the publisher to publish.
func (s *Service) PublishPassdb(ctx context.Context) error {
	return s.renderPassdb(ctx)
}

// republishPassdb is the sink. Every credential-changing path calls it, and it
// re-renders the whole file from state, so a change that stops at one surface
// (a password set, an enrolment, a disable) is visible to SMB on the next read.
//
// It then asks the whole-configuration publisher to push, because the rendered
// file is not what smbd authenticates against: the sidecar imports it into the
// credential database, and a file written with nobody told is a revocation
// that lands whenever something else happens to publish.
func (s *Service) republishPassdb(ctx context.Context) error {
	if err := s.renderPassdb(ctx); err != nil {
		return err
	}
	if publish := s.smbPublisher(); publish != nil {
		publish(ctx)
	}
	return nil
}

// renderPassdb writes the credential file from what the database holds.
//
// The file is a render and not the record: the database remains the authority.
// An account that is not eligible is left out entirely rather than written with
// a disabled marker, because a marker is a line the import tool still reads and
// the absence is what actually revokes.
func (s *Service) renderPassdb(ctx context.Context) (err error) {
	if s.passdb == "" {
		return nil
	}

	key, keyVer := s.mk.Active()

	rows, err := s.st.SQL().QueryContext(ctx, sqlReadPassdb)
	if err != nil {
		return fmt.Errorf("reading the SMB accounts: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()

	type entry struct {
		name string
		uid  uint32
		nt   [16]byte
	}
	var entries []entry
	for rows.Next() {
		var (
			id                   int64
			name                 string
			smbEnabled, disabled bool
			has2fa               bool
			ct                   []byte
			rowKeyVer            sql.NullInt64
		)
		if serr := rows.Scan(&id, &name, &smbEnabled, &disabled, &has2fa, &ct, &rowKeyVer); serr != nil {
			return serr
		}
		if !passdbEnabled(smbEnabled, disabled, has2fa, s.smbTOTPPolicy) || ct == nil {
			continue
		}

		// The hash was sealed against the account's own row id and the key
		// version in force at the time, so both are needed to open it.
		rowVer, ok := sealVersion(rowKeyVer)
		if !ok {
			continue
		}
		sealKey := key
		if rowVer != keyVer {
			k, held := s.mk.Get(rowVer)
			if !held {
				// A hash under a key this ring no longer holds is skipped
				// rather than failing the republish: one undecryptable row
				// must not keep every other account's revocation from being
				// published.
				continue
			}
			sealKey = k
		}
		nt, oerr := openNT(sealKey, ct, id, rowVer)
		if oerr != nil {
			continue
		}

		uid, uerr := smbUid(id)
		if uerr != nil {
			return uerr
		}
		entries = append(entries, entry{name: name, uid: uid, nt: nt})
	}
	if err := rows.Err(); err != nil {
		return err
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })

	// The timestamp is shared across the file rather than read per line, so
	// two republishes of unchanged state produce identical bytes within the
	// same second and a diff shows only real changes.
	lct := fmt.Sprintf("%08X", clock.System().Now().Unix())

	var b []byte
	for _, e := range entries {
		line, lerr := smbpasswdLine(e.name, e.uid, e.nt, lct)
		if lerr != nil {
			return lerr
		}
		b = append(b, line...)
	}

	return vfs.ReplaceFileDurable(s.passdb, 0o600, func(f *os.File) error {
		_, werr := f.Write(b)
		return werr
	})
}

// smbUid offsets a row id into the uid the entry carries.
//
// A row id that does not fit is a refusal rather than a wrapped number: the
// wrapped one would collide with another account's uid, and the import would
// then keep whichever of the two it saw last.
func smbUid(rowID int64) (uint32, error) {
	const maxRowID = int64(^uint32(0)) - SMBBaseUid
	if rowID <= 0 || rowID > maxRowID {
		return 0, fmt.Errorf("smb: account id %d has no representable uid", rowID)
	}
	return SMBBaseUid + uint32(rowID), nil //nolint:gosec // the bound directly above is what makes this fit.
}

// sealVersion narrows a stored key version. A row whose version is absent or
// outside the range a version can take is skipped: it cannot be opened, and
// guessing a version would try the wrong key.
func sealVersion(v sql.NullInt64) (uint32, bool) {
	if !v.Valid || v.Int64 < 0 || v.Int64 > int64(^uint32(0)) {
		return 0, false
	}
	return uint32(v.Int64), true //nolint:gosec // the bound directly above is what makes this fit.
}

// smbpasswdLine renders one entry.
//
// The LANMAN field is always the disabled marker: only the modern challenge
// response is supported, and a LANMAN hash present is a credential an attacker
// can break offline in minutes.
func smbpasswdLine(name string, uid uint32, nt [16]byte, lct string) ([]byte, error) {
	// The format is colon-separated with one record per line and has no escape
	// at all, so a name carrying either is refused rather than written. The
	// same check guards the account file this has to agree with.
	if strings.ContainsAny(name, ":\n\r\x00") {
		return nil, fmt.Errorf("smb: account name %q cannot be written to the passdb", name)
	}
	const lanmanDisabled = "XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX"
	return fmt.Appendf(nil, "%s:%d:%s:%X:[U          ]:LCT-%s:\n",
		name, uid, lanmanDisabled, nt, lct), nil
}

// PublishPasswdEntries writes the account file that sits beside the passdb.
//
// It carries the same uid for the same account, because the import tool
// matches the two through it. Publishing one without the other is what makes a
// login fail as an unknown user with nothing logged anywhere.
func (s *Service) PublishPasswdEntries(ctx context.Context, path string, gid uint32) (err error) {
	rows, err := s.st.SQL().QueryContext(ctx, sqlReadPassdb)
	if err != nil {
		return fmt.Errorf("reading the SMB accounts: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()

	var users []smb.User
	for rows.Next() {
		var (
			id                   int64
			name                 string
			smbEnabled, disabled bool
			has2fa               bool
			ct                   []byte
			rowKeyVer            sql.NullInt64
		)
		if serr := rows.Scan(&id, &name, &smbEnabled, &disabled, &has2fa, &ct, &rowKeyVer); serr != nil {
			return serr
		}
		if !passdbEnabled(smbEnabled, disabled, has2fa, s.smbTOTPPolicy) || ct == nil {
			continue
		}
		uid, uerr := smbUid(id)
		if uerr != nil {
			return uerr
		}
		users = append(users, smb.User{Name: name, Uid: uid})
	}
	if rerr := rows.Err(); rerr != nil {
		return rerr
	}

	b, err := smb.PasswdEntries(users, gid)
	if err != nil {
		return err
	}
	return vfs.ReplaceFileDurable(path, 0o644, func(f *os.File) error {
		_, werr := f.Write(b)
		return werr
	})
}

// sealAndStoreNT derives the NT hash of a password and seals it under the
// active master key, binding the user id and key version into the AAD.
func (s *Service) sealAndStoreNT(ctx context.Context, tx *sql.Tx, userID int64, pw secret.Secret, key [keyLen]byte, keyVer uint32) error {
	nt := ntHash(pw)
	ct, err := sealNT(key, nt, userID, keyVer)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, sqlUpsertSMBSecret, userID, ct, keyVer)
	return err
}

// ntHash is MD4 of the UTF-16LE encoding of the password, the value SMB
// derives its authentication from. The algorithm is fixed by the protocol;
// the mitigation is that the result is only ever sealed at rest and handed to
// the sidecar.
func ntHash(pw secret.Secret) [16]byte {
	units := utf16.Encode([]rune(string(pw.Reveal())))
	b := make([]byte, 0, len(units)*2)
	for _, u := range units {
		b = append(b, byte(u), byte(u>>8)) //nolint:gosec // UTF-16LE is exactly the low then the high byte of each unit.
	}
	var out [16]byte
	h := md4.New() //nolint:gosec // RFC 1321 output is fixed by the NTLM protocol; see the import note.
	h.Write(b)     //nolint:errcheck // md4.Write never fails.
	copy(out[:], h.Sum(nil))
	return out
}
