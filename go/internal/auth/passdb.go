package auth

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"unicode/utf16"

	"golang.org/x/crypto/md4" //nolint:gosec,staticcheck // MD4 is fixed by the SMB protocol; the value is sealed at rest and only that algorithm matches.

	"github.com/heavycaffeiner/stowcloud/go/internal/secret"
	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
)

// The SMB passdb sink. Deleting a credential row closes SMB in the database
// and nowhere else: smbd authenticates against the last file that was
// published, so a revocation that stops at SQLite leaves the sidecar serving
// the revoked credential. Republishing is a sink every credential-changing
// path calls, not a step each one remembers.

// passdbEnabled reports whether an account is eligible to appear in the SMB
// passdb: not opted out of SMB, not disabled, and not carrying a second
// factor the SMB policy blocks.
func passdbEnabled(smbEnabled, disabled, has2fa bool) bool {
	return smbEnabled && !disabled && !has2fa
}

// republishPassdb is the sink. Every credential-changing path calls it, and it
// re-renders the whole passdb file from state, so a change that stops at one
// surface (set password, TOTP enrol, a disable) is visible to SMB on the next
// read. The file is a render, not the record: state.db remains the authority,
// and Phase 11 owns the exact smbd format. What this phase proves is that
// every path reaches the render.
func (s *Service) republishPassdb(ctx context.Context) (err error) {
	if s.passdb == "" {
		return nil
	}

	rows, err := s.st.SQL().QueryContext(ctx, sqlReadPassdb)
	if err != nil {
		return fmt.Errorf("reading the SMB accounts: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	type smbLine struct {
		name    string
		enabled bool
		fp      string
	}
	var lines []smbLine
	for rows.Next() {
		var (
			name                 string
			smbEnabled, disabled bool
			has2fa               bool
			nt                   []byte
		)
		if serr := rows.Scan(&name, &smbEnabled, &disabled, &has2fa, &nt); serr != nil {
			return serr
		}
		fp := ""
		if nt != nil {
			sum := sha256.Sum256(nt)
			fp = hex.EncodeToString(sum[:])
		}
		lines = append(lines, smbLine{name: name, enabled: passdbEnabled(smbEnabled, disabled, has2fa), fp: fp})
	}
	if err := rows.Err(); err != nil {
		return err
	}

	sort.Slice(lines, func(i, j int) bool { return lines[i].name < lines[j].name })
	var b strings.Builder
	for _, l := range lines {
		state := "disabled"
		if l.enabled {
			state = "enabled"
		}
		fmt.Fprintf(&b, "%s\t%s\tnt=%s\n", l.name, state, l.fp) //nolint:errcheck // strings.Builder.Write never fails.
	}

	return vfs.ReplaceFileDurable(s.passdb, 0o600, func(f *os.File) error {
		_, werr := f.WriteString(b.String())
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
