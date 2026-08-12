package fromrust

import (
	"context"
	"database/sql"
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
	users, err := s.copyUsers(ctx, tx, rep)
	if err != nil {
		return err
	}
	groups, err := s.copyGroups(ctx, tx, rep)
	if err != nil {
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
	return s.copyLocks(ctx, tx, rep, users, clk)
}

func record(rep *Report, table string, kept int, drops map[Reason]int) {
	rep.Copied[table] += kept
	for reason, n := range drops {
		rep.Dropped[Drop{Table: table, Reason: reason}] += n
	}
}

// copyUsers carries the accounts and lifts the TOTP secret out of the user row
// into the table that now holds it.
func (s *sources) copyUsers(ctx context.Context, tx *sql.Tx, rep *Report) (map[int64]bool, error) {
	users := map[int64]bool{}
	kept, _, err := copyRows(ctx, tx, s.auth, selUser, insUser, 11,
		func(v []any) ([]any, Reason, error) {
			id, ok := asInt(v[0])
			if !ok {
				return nil, keep, errors.New("a user row carries a non-integer id")
			}
			users[id] = true
			return v, keep, nil
		})
	if err != nil {
		return nil, fmt.Errorf("importing user: %w", err)
	}
	record(rep, "user", kept, nil)

	// The secrets are sealed under the key version the old database recorded,
	// so the version travels with them: re-sealing is the master key's own
	// rotation and not an import's business.
	keyVer := int64(1)
	if kerr := s.auth.QueryRowContext(ctx, selKeyVersion).Scan(&keyVer); kerr != nil &&
		!errors.Is(kerr, sql.ErrNoRows) {
		return nil, fmt.Errorf("reading the key version: %w", kerr)
	}
	secrets, _, err := copyRows(ctx, tx, s.auth, selTotpSecret, insTotpSecret, 3,
		func(v []any) ([]any, Reason, error) {
			return []any{v[0], v[1], keyVer, v[2]}, keep, nil
		})
	if err != nil {
		return nil, fmt.Errorf("importing totp_secret: %w", err)
	}
	record(rep, "totp_secret", secrets, nil)
	return users, nil
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

func scanBuffers(cols int) (vals, ptrs []any) {
	vals = make([]any, cols)
	ptrs = make([]any, cols)
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	return vals, ptrs
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
