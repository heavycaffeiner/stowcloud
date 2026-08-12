package fromrust

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
)

// into carries every table across inside one transaction, so a failure part
// way leaves no state.db worth keeping.
//
// Accounts and groups go first because everything else references one of them,
// and a row whose account did not come across is dropped and counted rather
// than aborting the import: a dangling reference in the old files is a fact
// about them, not a reason to refuse an operator their data.
func (s *sources) into(ctx context.Context, tx *sql.Tx, rep *Report) error {
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
		xform func([]any) ([]any, bool, error)
	}{
		{"membership", s.auth, selMembership, insMembership, 2,
			func(v []any) ([]any, bool, error) {
				return v, known(users, v[0]) && known(groups, v[1]), nil
			}},
		{"session", s.auth, selSession, insSession, 8, dropUnknownUser(users, 1)},
		{"app_password", s.auth, selAppPassword, insAppPassword, 12, dropUnknownUser(users, 2)},
		{"recovery_code", s.auth, selRecoveryCode, insRecoveryCode, 3, dropUnknownUser(users, 0)},
		{"oidc_link", s.auth, selOidcLink, insOidcLink, 5, dropUnknownUser(users, 2)},
		{"audit", s.auth, selAudit, insAudit, 8, orphanAudit(users)},
		{"grant", s.acl, selGrant, insGrant, 10, splitPrincipal(users, groups)},
		{"settings", s.settings, selSettings, insSettings, 2, nil},
	} {
		kept, dropped, cerr := copyRows(ctx, tx, step.src, step.sel, step.ins, step.cols, step.xform)
		if cerr != nil {
			return fmt.Errorf("importing %s: %w", step.table, cerr)
		}
		record(rep, step.table, kept, dropped)
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
	return nil
}

func record(rep *Report, table string, kept, dropped int) {
	rep.Copied[table] += kept
	if dropped > 0 {
		rep.Dropped[table] += dropped
	}
}

// copyUsers carries the accounts and lifts the TOTP secret out of the user row
// into the table that now holds it.
func (s *sources) copyUsers(ctx context.Context, tx *sql.Tx, rep *Report) (map[int64]bool, error) {
	users := map[int64]bool{}
	kept, _, err := copyRows(ctx, tx, s.auth, selUser, insUser, 11,
		func(v []any) ([]any, bool, error) {
			id, ok := asInt(v[0])
			if !ok {
				return nil, false, errors.New("a user row carries a non-integer id")
			}
			users[id] = true
			return v, true, nil
		})
	if err != nil {
		return nil, fmt.Errorf("importing user: %w", err)
	}
	record(rep, "user", kept, 0)

	// The secrets are sealed under the key version the old database recorded,
	// so the version travels with them: re-sealing is the master key's own
	// rotation and not an import's business.
	keyVer := int64(1)
	if kerr := s.auth.QueryRowContext(ctx, selKeyVersion).Scan(&keyVer); kerr != nil &&
		!errors.Is(kerr, sql.ErrNoRows) {
		return nil, fmt.Errorf("reading the key version: %w", kerr)
	}
	secrets, _, err := copyRows(ctx, tx, s.auth, selTotpSecret, insTotpSecret, 3,
		func(v []any) ([]any, bool, error) {
			return []any{v[0], v[1], keyVer, v[2]}, true, nil
		})
	if err != nil {
		return nil, fmt.Errorf("importing totp_secret: %w", err)
	}
	record(rep, "totp_secret", secrets, 0)
	return users, nil
}

func (s *sources) copyGroups(ctx context.Context, tx *sql.Tx, rep *Report) (map[int64]bool, error) {
	groups := map[int64]bool{}
	kept, _, err := copyRows(ctx, tx, s.auth, selGroup, insGroup, 2,
		func(v []any) ([]any, bool, error) {
			id, ok := asInt(v[0])
			if !ok {
				return nil, false, errors.New("a group row carries a non-integer id")
			}
			groups[id] = true
			return v, true, nil
		})
	if err != nil {
		return nil, fmt.Errorf("importing group: %w", err)
	}
	record(rep, "group", kept, 0)
	return groups, nil
}

// copyShareLinks carries the links and turns each one's node id into the
// file's identity, which is what a link now follows a rename by.
func (s *sources) copyShareLinks(ctx context.Context, tx *sql.Tx, rep *Report, users map[int64]bool) error {
	kept, dropped, err := copyRows(ctx, tx, s.links, selShareLink, insShareLink, 15,
		func(v []any) ([]any, bool, error) {
			if !known(users, v[6]) {
				return nil, false, nil
			}
			id, err := s.identOf(ctx, v[5])
			if err != nil {
				return nil, false, err
			}
			out := slices.Clone(v[:5])
			out = append(out, id.dev, id.ino, id.present, id.btime)
			out = append(out, v[6:]...)
			return out, true, nil
		})
	if err != nil {
		return fmt.Errorf("importing share_link: %w", err)
	}
	record(rep, "share_link", kept, dropped)
	return nil
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
	var kept, dropped, intervals int
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
			rep.Dropped["upload_interval"]++
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
	record(rep, "upload_session", kept, dropped)
	record(rep, "upload_interval", intervals, 0)
	return nil
}

// copyIdentityKeyed carries the two tables whose rows used to point at a node
// id: dead properties, which lived in the cache itself, and favorites, which
// lived in the compatibility layer's own database.
func (s *sources) copyIdentityKeyed(ctx context.Context, tx *sql.Tx, rep *Report, users map[int64]bool) error {
	props, dropped, err := copyRows(ctx, tx, s.meta, selDavProp, insDavProp, 4,
		func(v []any) ([]any, bool, error) {
			id, ierr := s.identOf(ctx, v[0])
			if ierr != nil {
				return nil, false, ierr
			}
			if id.share == 0 && id.dev == 0 && id.ino == 0 {
				// The node is gone, so nothing can say which file this
				// property belonged to.
				return nil, false, nil
			}
			return []any{id.share, id.dev, id.ino, id.present, id.btime, v[1], v[2], v[3]}, true, nil
		})
	if err != nil {
		return fmt.Errorf("importing dav_prop: %w", err)
	}
	record(rep, "dav_prop", props, dropped)

	favs, fdropped, err := copyRows(ctx, tx, s.compat, selFavorite, insFavorite, 2,
		func(v []any) ([]any, bool, error) {
			if !known(users, v[0]) {
				return nil, false, nil
			}
			id, ierr := s.identOf(ctx, v[1])
			if ierr != nil {
				return nil, false, ierr
			}
			if id.share == 0 && id.dev == 0 && id.ino == 0 {
				return nil, false, nil
			}
			return []any{v[0], id.share, id.dev, id.ino, id.present, id.btime}, true, nil
		})
	if err != nil {
		return fmt.Errorf("importing favorite: %w", err)
	}
	record(rep, "favorite", favs, fdropped)
	return nil
}

// ident is a file's identity as the cache recorded it, flattened into the
// columns the durable half keys by.
type ident struct{ share, dev, ino, present, btime int64 }

// identOf turns a node id into the identity of the file it named. A missing
// cache, or a missing row, answers with zeroes: the caller decides whether
// that is a row to drop or a column to leave empty.
func (s *sources) identOf(ctx context.Context, fileid any) (ident, error) {
	var out ident
	id, ok := asInt(fileid)
	if !ok || s.meta == nil {
		return out, nil
	}
	var btime *int64
	err := s.meta.QueryRowContext(ctx, selNodeIdent, id).Scan(&out.share, &out.dev, &out.ino, &btime)
	if errors.Is(err, sql.ErrNoRows) {
		return ident{}, nil
	}
	if err != nil {
		return ident{}, fmt.Errorf("reading the cache for node %d: %w", id, err)
	}
	if btime != nil {
		out.present, out.btime = 1, *btime
	}
	return out, nil
}

// copyRows moves one table. xform may rewrite a row, drop it by reporting
// false, or fail the import. A nil source is a file that deployment never had.
func copyRows(
	ctx context.Context, tx *sql.Tx, src *sql.DB, sel, ins string, cols int,
	xform func([]any) ([]any, bool, error),
) (kept, dropped int, err error) {
	if src == nil {
		return 0, 0, nil
	}
	rows, err := src.QueryContext(ctx, sel)
	if err != nil {
		return 0, 0, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()

	vals, ptrs := scanBuffers(cols)
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return kept, dropped, err
		}
		out := vals
		if xform != nil {
			var take bool
			var xerr error
			if out, take, xerr = xform(vals); xerr != nil {
				return kept, dropped, xerr
			} else if !take {
				dropped++
				continue
			}
		}
		if _, err := tx.ExecContext(ctx, ins, out...); err != nil {
			return kept, dropped, err
		}
		kept++
	}
	return kept, dropped, rows.Err()
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
func dropUnknownUser(users map[int64]bool, col int) func([]any) ([]any, bool, error) {
	return func(v []any) ([]any, bool, error) { return v, known(users, v[col]), nil }
}

// orphanAudit keeps the row and forgets the actor. A record of what an account
// did outlives the account, which is the point of having one.
func orphanAudit(users map[int64]bool) func([]any) ([]any, bool, error) {
	return func(v []any) ([]any, bool, error) {
		if v[1] != nil && !known(users, v[1]) {
			v[1] = nil
		}
		return v, true, nil
	}
}

// splitPrincipal turns the old kind-and-id pair into the two columns a foreign
// key can be written against.
func splitPrincipal(users, groups map[int64]bool) func([]any) ([]any, bool, error) {
	return func(v []any) ([]any, bool, error) {
		kind, ok := asInt(v[1])
		if !ok {
			return nil, false, errors.New("a grant row carries a non-integer principal kind")
		}
		var user, group any
		switch kind {
		case principalUser:
			if !known(users, v[2]) {
				return nil, false, nil
			}
			user = v[2]
		case principalGroup:
			if !known(groups, v[2]) {
				return nil, false, nil
			}
			group = v[2]
		default:
			return nil, false, fmt.Errorf("a grant row carries principal kind %d", kind)
		}
		out := []any{v[0], user, group}
		return append(out, v[3:]...), true, nil
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

// SQLite has one integer type and it is signed, so a byte offset with the top
// bit set is stored as its two's complement. Nothing orders these as numbers.
//
//nolint:gosec // the reinterpretation is the point; see above.
func toSQL(v uint64) int64 { return int64(v) }
