package fromrust

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/heavycaffeiner/stowcloud/go/internal/clock"
)

// keep is what a transform returns for a row that comes across. Every other
// return names why the row did not.
const keep Reason = ""

// into carries every table across inside one transaction, so a failure part
// way leaves no state.db worth keeping.
//
// Accounts and groups go first because everything else references one of them,
// and a row whose account did not come across is dropped and counted rather
// than aborting the import: a dangling reference in the old files is a fact
// about them, not a reason to refuse an operator their data.
func (s *sources) into(ctx context.Context, tx *sql.Tx, rep *Report, clk clock.Clock) error {
	users, userByName, err := s.copyUsers(ctx, tx, rep)
	if err != nil {
		return err
	}
	groups, err := s.copyGroups(ctx, tx, rep)
	if err != nil {
		return err
	}
	if err := s.copySMBSecrets(ctx, tx, rep, users); err != nil {
		return err
	}
	if err := s.copyTotpUsed(ctx, tx, rep, users, clk); err != nil {
		return err
	}

	for _, step := range []struct {
		table string
		src   *sql.DB
		sel   string
		ins   string
		cols  int
		xform func([]any) ([]any, Reason, error)
	}{
		{"membership", s.auth, selMembership, insMembership, 2,
			func(v []any) ([]any, Reason, error) {
				switch {
				case !known(users, v[0]):
					return nil, ReasonUnknownUser, nil
				case !known(groups, v[1]):
					return nil, ReasonUnknownGroup, nil
				}
				return v, keep, nil
			}},
		{"session", s.auth, selSession, insSession, 8, dropUnknownUser(users, 1)},
		{"app_password", s.auth, selAppPassword, insAppPassword, 12, dropUnknownUser(users, 2)},
		{"recovery_code", s.auth, selRecoveryCode, insRecoveryCode, 3, dropUnknownUser(users, 0)},
		{"oidc_link", s.auth, selOidcLink, insOidcLink, 5, dropUnknownUser(users, 2)},
		{"audit", s.auth, selAudit, insAudit, 8, orphanAudit(users)},
		{"grant", s.acl, selGrant, insGrant, 10, splitPrincipal(users, groups)},
		{"settings", s.settings, selSettings, insSettings, 2, nil},
	} {
		kept, drops, cerr := copyRows(ctx, tx, step.src, step.sel, step.ins, step.cols, step.xform)
		if cerr != nil {
			return fmt.Errorf("importing %s: %w", step.table, cerr)
		}
		record(rep, step.table, kept, drops)
	}

	if err := s.copyShareLinks(ctx, tx, rep, users); err != nil {
		return err
	}
	if err := s.copyUploads(ctx, tx, rep, users); err != nil {
		return err
	}
	if err := s.copyIdentityKeyed(ctx, tx, rep, users); err != nil {
		return err
	}
	if err := s.copyLocks(ctx, tx, rep, users, clk); err != nil {
		return err
	}
	if err := s.copyShares(ctx, tx, rep); err != nil {
		return err
	}
	if err := s.copyIndexSettings(ctx, tx, rep); err != nil {
		return err
	}
	if err := s.copyCompat(ctx, tx, rep, users, userByName, clk); err != nil {
		return err
	}
	return s.copyJobs(ctx, tx, rep, users)
}

func record(rep *Report, table string, kept int, drops map[Reason]int) {
	rep.Copied[table] += kept
	for reason, n := range drops {
		rep.Dropped[Drop{Table: table, Reason: reason}] += n
	}
}

// copyUsers carries the accounts and lifts the TOTP secret out of the user row
// into the table that now holds it.
func (s *sources) copyUsers(ctx context.Context, tx *sql.Tx, rep *Report) (map[int64]bool, map[string]int64, error) {
	users := map[int64]bool{}
	// The account names, because the compat login flows record an approval by
	// name and everything downstream keys on the id.
	byName := map[string]int64{}
	kept, _, err := copyRows(ctx, tx, s.auth, selUser, insUser, 11,
		func(v []any) ([]any, Reason, error) {
			id, ok := asInt(v[0])
			if !ok {
				return nil, keep, errors.New("a user row carries a non-integer id")
			}
			users[id] = true
			if name, nok := v[1].(string); nok {
				byName[name] = id
			}
			return v, keep, nil
		})
	if err != nil {
		return nil, nil, fmt.Errorf("importing user: %w", err)
	}
	record(rep, "user", kept, nil)

	// The secrets are sealed under the key version the old database recorded,
	// so the version travels with them: re-sealing is the master key's own
	// rotation and not an import's business. A database that predates the
	// key_version table means version 1; a table that is declared and empty,
	// or holds a non-integer, is corruption rather than another default.
	keyVer := int64(1)
	hasKV, kerr := hasTable(ctx, s.auth, "key_version")
	if kerr != nil {
		return nil, nil, fmt.Errorf("looking for the key_version table: %w", kerr)
	}
	if hasKV {
		if kerr := s.auth.QueryRowContext(ctx, selKeyVersion).Scan(&keyVer); errors.Is(kerr, sql.ErrNoRows) {
			return nil, nil, errors.New("the declared key_version table is empty, which is corruption")
		} else if kerr != nil {
			return nil, nil, fmt.Errorf("reading the key version: %w", kerr)
		}
	}
	secrets, _, err := copyRows(ctx, tx, s.auth, selTotpSecret, insTotpSecret, 3,
		func(v []any) ([]any, Reason, error) {
			return []any{v[0], v[1], keyVer, v[2]}, keep, nil
		})
	if err != nil {
		return nil, nil, fmt.Errorf("importing totp_secret: %w", err)
	}
	record(rep, "totp_secret", secrets, nil)
	return users, byName, nil
}

func (s *sources) copyGroups(ctx context.Context, tx *sql.Tx, rep *Report) (map[int64]bool, error) {
	groups := map[int64]bool{}
	kept, _, err := copyRows(ctx, tx, s.auth, selGroup, insGroup, 2,
		func(v []any) ([]any, Reason, error) {
			id, ok := asInt(v[0])
			if !ok {
				return nil, keep, errors.New("a group row carries a non-integer id")
			}
			groups[id] = true
			return v, keep, nil
		})
	if err != nil {
		return nil, fmt.Errorf("importing group: %w", err)
	}
	record(rep, "group", kept, nil)
	return groups, nil
}

// copySMBSecrets carries the encrypted NT hashes, preserving each one's
// recorded key version: re-sealing is the master key's own rotation, and an
// import is not the place to touch a ciphertext it cannot open. A source that
// predates the table has nothing to carry. A ciphertext whose user is missing
// or whose recorded key version is missing aborts the import: dropping either
// would be a credential disappearing in silence.
func (s *sources) copySMBSecrets(ctx context.Context, tx *sql.Tx, rep *Report, users map[int64]bool) error {
	ok, err := hasTable(ctx, s.auth, "user_smb_secret")
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	kept, drops, err := copyRows(ctx, tx, s.auth, selSMB, insSMB, 3,
		func(v []any) ([]any, Reason, error) {
			if !known(users, v[0]) {
				return nil, keep, fmt.Errorf(
					"the SMB ciphertext belongs to user %v, which %s holds no row for: "+
						"refusing rather than dropping a credential in silence", v[0], authFile)
			}
			if v[2] == nil {
				return nil, keep, errors.New("an SMB ciphertext carries no recorded key version: refusing")
			}
			return v, keep, nil
		})
	if err != nil {
		return fmt.Errorf("importing user_smb_secret: %w", err)
	}
	record(rep, "user_smb_secret", kept, drops)
	return nil
}

// copyTotpUsed carries only the replay steps still inside the accepted
// window. A step the window has left cannot reject anything, and it is the
// one bit of replay state that is meaningful after a cutover; the rest are
// transient and reported as such.
func (s *sources) copyTotpUsed(ctx context.Context, tx *sql.Tx, rep *Report, users map[int64]bool, clk clock.Clock) error {
	ok, err := hasTable(ctx, s.auth, "totp_used")
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	nowStep := clk.Nanos() / (totpStepSeconds * 1e9)
	keepFrom := nowStep - totpWindowSteps
	kept, drops, err := copyRows(ctx, tx, s.auth, selTotpUsed, insTotpUsed, 2,
		func(v []any) ([]any, Reason, error) {
			if !known(users, v[0]) {
				return nil, ReasonUnknownUser, nil
			}
			step, ok := asInt(v[1])
			if !ok {
				return nil, keep, errors.New("a TOTP replay row carries a non-integer step")
			}
			if step < keepFrom {
				return nil, ReasonStaleFactor, nil
			}
			return []any{v[0], v[1], clk.Nanos()}, keep, nil
		})
	if err != nil {
		return fmt.Errorf("importing totp_used: %w", err)
	}
	record(rep, "totp_used", kept, drops)
	return nil
}

// The TOTP drift window the replay guard uses, mirrored here so the importer
// keeps the same steps live that the verifier would accept.
const (
	totpStepSeconds = 30
	totpWindowSteps = 1
)

// copyShareLinks carries the links and turns a Rust node id into the identity
// of the file it named.
//
// A link keeps a path and, when it was made against a file, that file's
// identity; it does not follow a rename. So the two representations are
// path-only and a complete identity, and anything the old database cannot be
// resolved into is a refusal. Weakening an identity-bearing link to path-only
// would make whatever is created at that path next publicly readable under a
// token somebody already has.
func (s *sources) copyShareLinks(ctx context.Context, tx *sql.Tx, rep *Report, users map[int64]bool) error {
	kept, drops, err := copyRows(ctx, tx, s.links, selShareLink, insShareLink, 15,
		func(v []any) ([]any, Reason, error) {
			if !known(users, v[6]) {
				return nil, ReasonUnknownUser, nil
			}
			target, terr := s.linkTarget(ctx, v[0], v[5])
			if terr != nil {
				return nil, keep, terr
			}
			// A ciphertext the Rust build sealed carries no version in its AAD,
			// and version 0 is the name for that. Both columns move together.
			var keyVer any
			if v[2] != nil {
				keyVer = int64(legacyTokenKeyVersion)
			}
			out := []any{v[0], v[1], v[2], keyVer, v[3], v[4]}
			out = append(out, target.dev, target.ino, target.present, target.btime)
			out = append(out, v[6:]...)
			return out, keep, nil
		})
	if err != nil {
		return fmt.Errorf("importing share_link: %w", err)
	}
	record(rep, "share_link", kept, drops)
	return nil
}

// linkTarget is the four identity columns for one link: all nil for a Rust row
// whose fileid was NULL, and a complete tuple otherwise.
type linkTarget struct{ dev, ino, present, btime any }

func (s *sources) linkTarget(ctx context.Context, linkID, fileid any) (linkTarget, error) {
	if fileid == nil {
		return linkTarget{}, nil
	}
	id, ok := asInt(fileid)
	if !ok {
		return linkTarget{}, fmt.Errorf("share link %v carries a non-integer file id", linkID)
	}
	if s.meta == nil {
		return linkTarget{}, fmt.Errorf(
			"share link %v targets file id %d and this directory holds no %s to resolve it in",
			linkID, id, metaFile)
	}

	var share, dev, ino int64
	var btime *int64
	err := s.meta.QueryRowContext(ctx, selNodeIdent, id).Scan(&share, &dev, &ino, &btime)
	if errors.Is(err, sql.ErrNoRows) {
		return linkTarget{}, fmt.Errorf(
			"share link %v targets file id %d, which %s has no row for", linkID, id, metaFile)
	}
	if err != nil {
		return linkTarget{}, fmt.Errorf("reading the cache for share link %v: %w", linkID, err)
	}
	if btime == nil {
		return linkTarget{}, fmt.Errorf(
			"share link %v targets file id %d, whose filesystem reports no birth time: "+
				"without one the link cannot tell the original file from a replacement "+
				"that reused its inode number", linkID, id)
	}
	if dev == 0 && ino == 0 {
		return linkTarget{}, fmt.Errorf(
			"share link %v targets file id %d, which the cache records with no device or "+
				"inode number", linkID, id)
	}
	return linkTarget{dev: dev, ino: ino, present: int64(1), btime: *btime}, nil
}

// copyUploads carries the sessions and unpacks each one's interval set into
// rows. Stored as a blob, a partially written set is a corrupt one; stored as
// rows it is a shorter one.
func (s *sources) copyUploads(ctx context.Context, tx *sql.Tx, rep *Report, users map[int64]bool) error {
	if s.upload == nil {
		return nil
	}
	// The sessions that actually came across, so an alias naming one that did
	// not is dropped rather than becoming a transfer id that resolves to a
	// session id no row holds.
	sessions := map[string]bool{}
	rows, err := s.upload.QueryContext(ctx, selUploadSession)
	if err != nil {
		return fmt.Errorf("importing upload_session: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()

	const cols = 24
	vals, ptrs := scanBuffers(cols)
	var kept, dropped, intervals, corrupt int
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return fmt.Errorf("importing upload_session: %w", err)
		}
		if !known(users, vals[1]) {
			dropped++
			continue
		}
		// The last column is the interval blob, which the destination holds as
		// its own table rather than as a column.
		session := vals[0]
		if _, err := tx.ExecContext(ctx, insUploadSession, vals[:cols-1]...); err != nil {
			return fmt.Errorf("importing upload_session: %w", err)
		}
		kept++
		if id, ok := session.([]byte); ok {
			sessions[string(id)] = true
		}

		blob, isBlob := vals[cols-1].([]byte)
		runs, derr := decodeIntervals(blob)
		if !isBlob || derr != nil {
			// A set that will not decode means the session cannot report what
			// it holds, so the client re-sends from the start. That is a
			// slower upload, not a lost one, and it is better than an import
			// that stops.
			corrupt++
			continue
		}
		for _, r := range runs {
			if _, err := tx.ExecContext(ctx, insUploadInterval, session, toSQL(r[0]), toSQL(r[1])); err != nil {
				return fmt.Errorf("importing upload_interval: %w", err)
			}
			intervals++
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("importing upload_session: %w", err)
	}
	record(rep, "upload_session", kept, map[Reason]int{ReasonUnknownUser: dropped})
	record(rep, "upload_interval", intervals, map[Reason]int{ReasonCorruptRange: corrupt})

	if err := s.copyUploadAliases(ctx, tx, rep, users, sessions); err != nil {
		return err
	}
	if err := s.copyChunkSettings(ctx, tx, rep); err != nil {
		return err
	}
	return s.copyTouchedDirs(ctx, tx, rep)
}

// copyUploadAliases carries the transfer-id bindings, which is what makes a
// named chunk collection resumable after the cutover: the client keeps using
// the id it chose, and without the binding that id means nothing to the new
// binary.
//
// An alias whose session did not come across is dropped. Keeping it would
// leave a transfer id resolving to a session id no row holds, which is a
// lookup that fails later and more confusingly than one that fails now.
func (s *sources) copyUploadAliases(
	ctx context.Context, tx *sql.Tx, rep *Report, users map[int64]bool, sessions map[string]bool,
) error {
	// A deployment older than the alias table has no such table, which is a
	// fact about when it was installed rather than an error.
	ok, err := hasTable(ctx, s.upload, "upload_alias")
	if err != nil || !ok {
		return err
	}
	kept, drops, err := copyRows(ctx, tx, s.upload, selUploadAlias, insUploadAlias, 6,
		func(v []any) ([]any, Reason, error) {
			if !known(users, v[1]) {
				return nil, ReasonUnknownUser, nil
			}
			// The old table stores the session handle in its transport form
			// rather than as raw bytes, so it is decoded before being
			// compared against the sessions this import actually wrote.
			raw, ok := asSessionBytes(v[2])
			if !ok || !sessions[string(raw)] {
				return nil, ReasonMissingSession, nil
			}
			return []any{v[0], v[1], raw, v[3], v[4], v[5]}, keep, nil
		})
	if err != nil {
		return fmt.Errorf("importing upload_alias: %w", err)
	}
	record(rep, "upload_alias", kept, drops)
	return nil
}

// copyChunkSettings carries the admin-stored chunk floor and default.
//
// What is being carried is the row's existence as much as the two numbers:
// absence is what tells the settings screen the values came from the config
// file, and fabricating a row would report an admin decision nobody made.
func (s *sources) copyChunkSettings(ctx context.Context, tx *sql.Tx, rep *Report) error {
	ok, terr := hasTable(ctx, s.upload, "upload_chunk_settings")
	if terr != nil || !ok {
		return terr
	}
	var chunkMin, chunkDefault int64
	err := s.upload.QueryRowContext(ctx, selUploadChunkSettings).Scan(&chunkMin, &chunkDefault)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("importing upload_chunk_settings: %w", err)
	}
	if _, err := tx.ExecContext(ctx, insUploadChunkSettings, chunkMin, chunkDefault); err != nil {
		return fmt.Errorf("importing upload_chunk_settings: %w", err)
	}
	record(rep, "upload_chunk_settings", 1, nil)
	return nil
}

// copyTouchedDirs carries the directories part files were created in, so the
// first sweep after the cutover can still find an orphan the old build left.
func (s *sources) copyTouchedDirs(ctx context.Context, tx *sql.Tx, rep *Report) error {
	ok, terr := hasTable(ctx, s.upload, "upload_touched_dirs")
	if terr != nil || !ok {
		return terr
	}
	kept, drops, err := copyRows(ctx, tx, s.upload, selUploadTouchedDirs, insUploadTouchedDir, 2, nil)
	if err != nil {
		return fmt.Errorf("importing upload_touched_dirs: %w", err)
	}
	record(rep, "upload_touched_dir", kept, drops)
	return nil
}

// copyIdentityKeyed carries the two tables whose rows used to point at a node
// id: dead properties, which lived in the cache itself, and favorites, which
// lived in the compatibility layer's own database.
//
// Unlike a share link these may be dropped when the node is gone. They decorate
// a file rather than granting access to one, so losing one costs a colour or a
// star and cannot expose anything.
func (s *sources) copyIdentityKeyed(ctx context.Context, tx *sql.Tx, rep *Report, users map[int64]bool) error {
	props, drops, err := copyRows(ctx, tx, s.meta, selDavProp, insDavProp, 4,
		func(v []any) ([]any, Reason, error) {
			id, ok, ierr := s.identOf(ctx, v[0])
			if ierr != nil {
				return nil, keep, ierr
			}
			if !ok {
				return nil, ReasonMissingNode, nil
			}
			return []any{id.share, id.dev, id.ino, id.present, id.btime, v[1], v[2], v[3]}, keep, nil
		})
	if err != nil {
		return fmt.Errorf("importing dav_prop: %w", err)
	}
	record(rep, "dav_prop", props, drops)

	favs, fdrops, err := copyRows(ctx, tx, s.compat, selFavorite, insFavorite, 2,
		func(v []any) ([]any, Reason, error) {
			if !known(users, v[0]) {
				return nil, ReasonUnknownUser, nil
			}
			id, ok, ierr := s.identOf(ctx, v[1])
			if ierr != nil {
				return nil, keep, ierr
			}
			if !ok {
				return nil, ReasonMissingNode, nil
			}
			return []any{v[0], id.share, id.dev, id.ino, id.present, id.btime}, keep, nil
		})
	if err != nil {
		return fmt.Errorf("importing favorite: %w", err)
	}
	record(rep, "favorite", favs, fdrops)
	return nil
}

// ident is a file's identity as the cache recorded it, flattened into the
// columns the durable half keys by.
type ident struct{ share, dev, ino, present, btime int64 }

// identOf turns a node id into the identity of the file it named, and reports
// whether the cache could answer at all. A missing cache and a missing row are
// the same answer to the caller, which decides what a row with no file is
// worth.
func (s *sources) identOf(ctx context.Context, fileid any) (ident, bool, error) {
	id, ok := asInt(fileid)
	if !ok || s.meta == nil {
		return ident{}, false, nil
	}
	var out ident
	var btime *int64
	err := s.meta.QueryRowContext(ctx, selNodeIdent, id).Scan(&out.share, &out.dev, &out.ino, &btime)
	if errors.Is(err, sql.ErrNoRows) {
		return ident{}, false, nil
	}
	if err != nil {
		return ident{}, false, fmt.Errorf("reading the cache for node %d: %w", id, err)
	}
	if btime != nil {
		out.present, out.btime = 1, *btime
	}
	return out, true, nil
}

// copyRows moves one table. xform may rewrite a row, drop it by naming a
// reason, or fail the import. A nil source is a file that deployment never had.
func copyRows(
	ctx context.Context, tx *sql.Tx, src *sql.DB, sel, ins string, cols int,
	xform func([]any) ([]any, Reason, error),
) (kept int, drops map[Reason]int, err error) {
	if src == nil {
		return 0, nil, nil
	}
	rows, err := src.QueryContext(ctx, sel)
	if err != nil {
		return 0, nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()

	drops = map[Reason]int{}
	vals, ptrs := scanBuffers(cols)
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return kept, drops, err
		}
		out := vals
		if xform != nil {
			var reason Reason
			var xerr error
			if out, reason, xerr = xform(vals); xerr != nil {
				return kept, drops, xerr
			} else if reason != keep {
				drops[reason]++
				continue
			}
		}
		if _, err := tx.ExecContext(ctx, ins, out...); err != nil {
			return kept, drops, err
		}
		kept++
	}
	return kept, drops, rows.Err()
}

// scanBuffers returns the value slots and scan pointers for one row.
func scanBuffers(cols int) (vals, ptrs []any) {
	vals = make([]any, cols)
	ptrs = make([]any, cols)
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	return vals, ptrs
}

// hasTable reports whether a source database has a named table, so an import
// can skip a table a source that predates it never had.
func hasTable(ctx context.Context, db *sql.DB, table string) (bool, error) {
	var n int
	err := db.QueryRowContext(ctx, sqlHasTable, table).Scan(&n)
	return n > 0, err
}

func asInt(v any) (int64, bool) {
	n, ok := v.(int64)
	return n, ok
}

func known(set map[int64]bool, v any) bool {
	n, ok := asInt(v)
	return ok && set[n]
}

// dropUnknownUser drops a row whose account did not come across.
func dropUnknownUser(users map[int64]bool, col int) func([]any) ([]any, Reason, error) {
	return func(v []any) ([]any, Reason, error) {
		if !known(users, v[col]) {
			return nil, ReasonUnknownUser, nil
		}
		return v, keep, nil
	}
}

// orphanAudit keeps the row and forgets the actor. A record of what an account
// did outlives the account, which is the point of having one.
func orphanAudit(users map[int64]bool) func([]any) ([]any, Reason, error) {
	return func(v []any) ([]any, Reason, error) {
		if v[1] != nil && !known(users, v[1]) {
			v[1] = nil
		}
		return v, keep, nil
	}
}

// splitPrincipal turns the old kind-and-id pair into the two columns a foreign
// key can be written against.
func splitPrincipal(users, groups map[int64]bool) func([]any) ([]any, Reason, error) {
	return func(v []any) ([]any, Reason, error) {
		kind, ok := asInt(v[1])
		if !ok {
			return nil, keep, errors.New("a grant row carries a non-integer principal kind")
		}
		var user, group any
		switch kind {
		case principalUser:
			if !known(users, v[2]) {
				return nil, ReasonUnknownUser, nil
			}
			user = v[2]
		case principalGroup:
			if !known(groups, v[2]) {
				return nil, ReasonUnknownGroup, nil
			}
			group = v[2]
		default:
			return nil, keep, fmt.Errorf("a grant row carries principal kind %d", kind)
		}
		out := []any{v[0], user, group}
		return append(out, v[3:]...), keep, nil
	}
}

func sortedKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// sortedDrops orders by table and then by reason, so two runs of the same
// import print the same report.
func sortedDrops(m map[Drop]int) []Drop {
	out := make([]Drop, 0, len(m))
	for k, n := range m {
		if n > 0 {
			out = append(out, k)
		}
	}
	slices.SortFunc(out, func(a, b Drop) int {
		if c := strings.Compare(a.Table, b.Table); c != 0 {
			return c
		}
		return strings.Compare(string(a.Reason), string(b.Reason))
	})
	return out
}

// SQLite has one integer type and it is signed, so a byte offset with the top
// bit set is stored as its two's complement. Nothing orders these as numbers.
//
//nolint:gosec // the reinterpretation is the point; see above.
func toSQL(v uint64) int64 { return int64(v) }

// copyIndexSettings merges the administrator's index override into the unified
// settings row.
//
// The index payload itself is a rebuildable cache and is not copied: the new
// build crawls and writes its own segments. This one value cannot be
// reconstructed from the filesystem or from the config file, because it is a
// decision somebody made.
//
// The merge decodes to a generic map rather than a typed struct, so a section
// the old build wrote that this one does not know about survives the round
// trip. A typed decode would silently drop it.
func (s *sources) copyIndexSettings(ctx context.Context, tx *sql.Tx, rep *Report) error {
	if s.index == nil {
		return nil
	}
	ok, terr := hasTable(ctx, s.index, "index_settings")
	if terr != nil || !ok {
		return terr
	}

	var nameEnabled int64
	err := s.index.QueryRowContext(ctx, selIndexSettings).Scan(&nameEnabled)
	if errors.Is(err, sql.ErrNoRows) {
		// No row means no administrator ever set it, and fabricating one would
		// report a decision nobody made.
		return nil
	}
	if err != nil {
		return fmt.Errorf("importing index_settings: %w", err)
	}

	// Whatever the settings copy already wrote, so this adds a key rather than
	// replacing the document.
	merged := map[string]any{}
	var existing string
	switch rerr := tx.QueryRowContext(ctx, selSettingsRow).Scan(&existing); {
	case rerr == nil:
		if existing != "" {
			if jerr := json.Unmarshal([]byte(existing), &merged); jerr != nil {
				return fmt.Errorf("importing index_settings: the settings row is not an object: %w", jerr)
			}
		}
	case errors.Is(rerr, sql.ErrNoRows):
		// No settings row yet; this creates one carrying only the override.
	default:
		return fmt.Errorf("importing index_settings: %w", rerr)
	}

	merged["search"] = map[string]any{"name_index_enabled": nameEnabled != 0}

	encoded, jerr := json.Marshal(merged)
	if jerr != nil {
		return fmt.Errorf("importing index_settings: %w", jerr)
	}
	if _, eerr := tx.ExecContext(ctx, upsSettingsRow, string(encoded)); eerr != nil {
		return fmt.Errorf("importing index_settings: %w", eerr)
	}
	record(rep, "index_settings", 1, nil)
	return nil
}

// copyCompat carries what the compatibility layer owns durably.
//
// Three things, and each for a different reason. The instance identity, so a
// client that saw one identity does not treat this as a different server and
// re-sync everything it holds. The active upload aliases, so an in-flight
// chunked upload survives the cutover rather than restarting. And the
// unexpired login flows, which are the delicate one.
func (s *sources) copyCompat(
	ctx context.Context, tx *sql.Tx, rep *Report, users map[int64]bool,
	userByName map[string]int64, clk clock.Clock,
) error {
	if s.compat == nil {
		return nil
	}
	if err := s.copyInstanceID(ctx, tx, rep); err != nil {
		return err
	}
	if err := s.copyCompatAliases(ctx, tx, rep, users); err != nil {
		return err
	}
	return s.copyLoginFlows(ctx, tx, rep, users, userByName, clk)
}

// copyInstanceID preserves the identity this deployment presents.
//
// Minted once and never regenerated, because a client that saw one identity
// and then another treats the server as a different server.
func (s *sources) copyInstanceID(ctx context.Context, tx *sql.Tx, rep *Report) error {
	ok, terr := hasTable(ctx, s.compat, "nc_instance")
	if terr != nil || !ok {
		return terr
	}
	var id string
	err := s.compat.QueryRowContext(ctx, selNcInstance, ncInstanceIDKey).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("importing nc_instance: %w", err)
	}
	if id == "" {
		return nil
	}
	if _, eerr := tx.ExecContext(ctx, insCompatKV, "instance_id", id); eerr != nil {
		return fmt.Errorf("importing nc_instance: %w", eerr)
	}
	record(rep, "compat_kv", 1, nil)
	return nil
}

// copyCompatAliases carries the client-chosen names of in-flight uploads.
//
// An alias whose user or session did not survive is dropped rather than
// carried: it would resolve to a session no row holds, which is a transfer the
// client believes it can resume and cannot.
func (s *sources) copyCompatAliases(
	ctx context.Context, tx *sql.Tx, rep *Report, users map[int64]bool,
) error {
	ok, terr := hasTable(ctx, s.compat, "nc_upload_alias")
	if terr != nil || !ok {
		return terr
	}
	sessions, serr := survivingSessions(ctx, tx)
	if serr != nil {
		return serr
	}

	kept, drops, err := copyRows(ctx, tx, s.compat, selNcUploadAlias, insCompatUploadAlias, 3,
		func(v []any) ([]any, Reason, error) {
			if !known(users, v[0]) {
				return nil, ReasonUnknownUser, nil
			}
			// The old table stores the session handle in its transport form
			// rather than as raw bytes, so it is decoded before being
			// compared against the sessions this import actually wrote.
			raw, ok := asSessionBytes(v[2])
			if !ok || !sessions[string(raw)] {
				return nil, ReasonMissingSession, nil
			}
			return []any{v[0], v[1], raw}, keep, nil
		})
	if err != nil {
		return fmt.Errorf("importing nc_upload_alias: %w", err)
	}
	record(rep, "compat_upload_alias", kept, drops)
	return nil
}

// asSessionBytes decodes an upload session handle from the form the old table
// stored it in. A value that does not decode names no session at all, which is
// the same outcome as one naming a session that did not survive.
func asSessionBytes(v any) ([]byte, bool) {
	switch t := v.(type) {
	case []byte:
		// Already raw, which is what an older database holds.
		return t, true
	case string:
		raw, err := base64.RawURLEncoding.DecodeString(t)
		if err != nil {
			return nil, false
		}
		return raw, true
	}
	return nil, false
}

// survivingSessions is the set of upload sessions this import already wrote,
// so an alias naming one that did not come across is dropped rather than left
// pointing at nothing.
func survivingSessions(ctx context.Context, tx *sql.Tx) (map[string]bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM upload_session`)
	if err != nil {
		return nil, fmt.Errorf("reading the imported upload sessions: %w", err)
	}
	defer func() {
		_ = rows.Close() //nolint:errcheck // the scan error below is the answer.
	}()

	out := map[string]bool{}
	for rows.Next() {
		var id []byte
		if serr := rows.Scan(&id); serr != nil {
			return nil, fmt.Errorf("reading the imported upload sessions: %w", serr)
		}
		out[string(id)] = true
	}
	return out, rows.Err()
}

// copyLoginFlows carries the unexpired device-login flows.
//
// This is the delicate one, and it is where the import corrects a defect in
// the shape it reads rather than reproducing it. The old table carried a
// temporary plaintext app password between approval and collection, so an
// approved flow nobody collected left a live credential nobody knew about.
//
// So for an approved flow the already-copied credential is revoked before the
// flow is translated, and only the authorization marker comes across: polling
// mints a replacement at delivery. An expired flow is a reasoned drop. A
// malformed one, or an approved one whose credential cannot be matched, stops
// the migration rather than leaving an orphan credential behind.
func (s *sources) copyLoginFlows(
	ctx context.Context, tx *sql.Tx, rep *Report, users map[int64]bool,
	userByName map[string]int64, clk clock.Clock,
) error {
	ok, terr := hasTable(ctx, s.compat, "nc_login_flow")
	if terr != nil || !ok {
		return terr
	}

	rows, err := s.compat.QueryContext(ctx, selNcLoginFlow)
	if err != nil {
		return fmt.Errorf("importing nc_login_flow: %w", err)
	}
	defer func() {
		_ = rows.Close() //nolint:errcheck // the scan error below is the answer.
	}()

	const flowTTLNs = int64(20 * 60 * 1_000_000_000)
	now := clk.Nanos()
	kept := 0
	drops := map[Reason]int{}

	for rows.Next() {
		var (
			pollDigest, loginDigest []byte
			createdNs               int64
			approvedLogin           sql.NullString
			appPassword             sql.NullString
		)
		if serr := rows.Scan(&pollDigest, &loginDigest, &createdNs,
			&approvedLogin, &appPassword); serr != nil {
			return fmt.Errorf("importing nc_login_flow: %w", serr)
		}
		// The old table records the approval by login name. The account it
		// names is looked up here, because everything downstream keys on the
		// id, and a name that no longer resolves is an approval for an account
		// that is gone.
		var approvedUser sql.NullInt64
		if approvedLogin.Valid && approvedLogin.String != "" {
			id, found := userByName[approvedLogin.String]
			if !found {
				drops[ReasonUnknownUser]++
				continue
			}
			approvedUser = sql.NullInt64{Int64: id, Valid: true}
		}

		// A row with no digests cannot be looked up by either token, so it is
		// unusable rather than merely odd. Refusing beats writing a row that
		// can only ever be swept.
		if len(pollDigest) == 0 || len(loginDigest) == 0 {
			return fmt.Errorf("importing nc_login_flow: a flow with no token digests")
		}

		if now-createdNs >= flowTTLNs {
			drops[ReasonExpired]++
			continue
		}
		if approvedUser.Valid && !users[approvedUser.Int64] {
			drops[ReasonUnknownUser]++
			continue
		}

		// An approved flow's already-minted credential is revoked here, so the
		// cutover leaves none behind. Failing to match one is a refusal: the
		// alternative is a live credential nobody can account for.
		if approvedUser.Valid && appPassword.Valid && appPassword.String != "" {
			if rerr := revokeCopiedCredential(ctx, tx, appPassword.String); rerr != nil {
				return rerr
			}
		}

		var user any
		if approvedUser.Valid {
			user = approvedUser.Int64
		}
		if _, ierr := tx.ExecContext(ctx, insNcLoginFlow,
			pollDigest, loginDigest, createdNs, user, approvedLogin.String); ierr != nil {
			return fmt.Errorf("importing nc_login_flow: %w", ierr)
		}
		kept++
	}
	if rerr := rows.Err(); rerr != nil {
		return fmt.Errorf("importing nc_login_flow: %w", rerr)
	}

	record(rep, "compat_login_flow", kept, drops)
	return nil
}

// revokeCopiedCredential removes the app password an approved flow already
// minted, so no orphan is left behind by the shape correction.
func revokeCopiedCredential(ctx context.Context, tx *sql.Tx, plaintext string) error {
	sum := sha256.Sum256([]byte(plaintext))
	res, err := tx.ExecContext(ctx, delAppPasswordByHash, sum[:])
	if err != nil {
		return fmt.Errorf("importing nc_login_flow: revoking a copied credential: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("importing nc_login_flow: revoking a copied credential: %w", err)
	}
	if n == 0 {
		// The credential this flow says it minted is not in the imported set.
		// Continuing would leave a flow whose password nobody can revoke, so
		// the migration stops and says which one.
		return fmt.Errorf("importing nc_login_flow: an approved flow's credential " +
			"is not among the imported app passwords, so it cannot be revoked")
	}
	return nil
}
