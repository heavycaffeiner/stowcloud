package state

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/limits"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
)

// The upload session store. These rows share this database rather than occupying
// their own, because an interrupted upload whose session row disappeared leaves
// a part file no one can ever complete, which is data loss disguised as
// cleanup.

// ErrNoSuchUploadSession reports a session id backed by no row.
var ErrNoSuchUploadSession = errors.New("no such upload session")

// UploadSession is one row of upload_session. It is the store's shape, not
// the engine's: the engine holds the interval set as a set and this holds it
// as rows in a second table.
type UploadSession struct {
	ID   []byte
	User int64
	// Share identifies where the completed file lands. A caller holding nothing
	// but a session id must still reach the share root, without maintaining a
	// private id-to-share table that a restart would discard.
	Share int64
	// Dest holds the share-relative destination path and is never rewritten. A
	// session able to relocate its own destination could publish somewhere the
	// permission check never examined.
	Dest string
	// PartName is the reserved control name the bytes accumulate under, in
	// the destination's own directory, because the rename that publishes it
	// is only atomic within one directory.
	PartName string
	// SpoolDir carries a value for name-ordered sessions and is empty for the
	// rest.
	SpoolDir string
	// CacheDir is the session's directory under the cache spool, and empty
	// for a session writing straight to the destination. Its presence is what
	// says which mode a session runs in, so a switch flipped mid-upload
	// cannot change where a session in flight looks for its bytes.
	CacheDir string
	// CacheMerged counts bytes from the file's start already present and durable
	// in the part file. Merging resumes from this point after a restart.
	CacheMerged int64
	Mode        int64
	// TotalLen is nil for a deferred length, which the client may supply
	// later and finalize requires.
	TotalLen *int64
	// ChunkSize records the size advertised at creation, and ChunkMinAtCreation
	// captures the floor as it stood then. Snapshotting the floor rather than
	// reading it live stops an administrator who changes it mid-upload from
	// retroactively invalidating a chunk that was legal when sent.
	ChunkSize          int64
	ChunkMinAtCreation int64
	RandomAccess       bool
	// NextName and WriteHead together form the name-ordered assembly cursor,
	// naming the chunk expected next and the offset in the part file where it
	// will land.
	NextName  int64
	WriteHead int64
	// SpooledNames lists out-of-order chunk names held in the spool directory
	// awaiting their predecessor.
	SpooledNames []uint32
	IfMatch      string
	Filename     string
	MtimeNs      *int64
	Mime         string
	RelativePath string
	// Verify and VerifyDigest are always written as a unit. An algorithm with no
	// expected value produces a check incapable of failing, so both are stored
	// and read together.
	Verify       *int64
	VerifyDigest []byte
	CreatedNs    int64
	ExpiresNs    int64
	State        int64
}

// UploadAlias binds a client-chosen transfer id to a session within a single
// account's namespace.
type UploadAlias struct {
	Session []byte
	Share   int64
	Dest    string
}

// CreateUploadSession writes a session row and its empty interval set in one
// transaction.
func (d *DB) CreateUploadSession(ctx context.Context, s UploadSession) error {
	if err := d.f.EnsureWritable(); err != nil {
		return err
	}
	names, err := encodeSpooledNames(s.SpooledNames)
	if err != nil {
		return err
	}
	return d.Write(ctx, func(tx *sql.Tx) error {
		_, ierr := tx.ExecContext(ctx, sqlInsertUploadSession,
			s.ID, s.User, s.Share, s.Dest, s.PartName, textArg(s.SpoolDir), s.Mode,
			nullInt(s.TotalLen), s.ChunkSize, s.ChunkMinAtCreation, s.RandomAccess,
			s.NextName, s.WriteHead, names, textArg(s.IfMatch), s.Filename,
			nullInt(s.MtimeNs), textArg(s.Mime), textArg(s.RelativePath),
			nullInt(s.Verify), blobArg(s.VerifyDigest), s.CreatedNs, s.ExpiresNs, s.State,
			textArg(s.CacheDir), s.CacheMerged)
		return ierr
	})
}

// ReadUploadSession retrieves a session row without its intervals.
func (d *DB) ReadUploadSession(ctx context.Context, id []byte) (UploadSession, error) {
	s, err := scanUploadSession(d.f.SQL().QueryRowContext(ctx, sqlReadUploadSession, id))
	if errors.Is(err, sql.ErrNoRows) {
		return UploadSession{}, ErrNoSuchUploadSession
	}
	if err != nil {
		return UploadSession{}, fmt.Errorf("reading an upload session: %w", err)
	}
	return s, nil
}

// ListUploadSessions retrieves every session row. The sweep obtains this before
// examining any directory, so a session created between the two passes is never
// mistaken for an orphan.
func (d *DB) ListUploadSessions(ctx context.Context) ([]UploadSession, error) {
	return d.queryUploadSessions(ctx, sqlListUploadSessions)
}

// ListExpiredUploadSessions retrieves sessions whose lifetime has elapsed.
func (d *DB) ListExpiredUploadSessions(ctx context.Context, nowNs int64) ([]UploadSession, error) {
	return d.queryUploadSessions(ctx, sqlListExpiredUploadSessions, nowNs)
}

func (d *DB) queryUploadSessions(
	ctx context.Context, stmt string, args ...any,
) (out []UploadSession, err error) {
	rows, err := d.f.SQL().QueryContext(ctx, stmt, args...)
	if err != nil {
		return nil, fmt.Errorf("listing upload sessions: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		s, serr := scanUploadSession(rows)
		if serr != nil {
			return nil, fmt.Errorf("listing upload sessions: %w", serr)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing upload sessions: %w", err)
	}
	return out, nil
}

// UpdateUploadSession rewrites the mutable portion of a session row.
func (d *DB) UpdateUploadSession(ctx context.Context, s UploadSession) error {
	names, err := encodeSpooledNames(s.SpooledNames)
	if err != nil {
		return err
	}
	return d.Write(ctx, func(tx *sql.Tx) error {
		_, ierr := tx.ExecContext(ctx, sqlUpdateUploadSession,
			nullInt(s.TotalLen), s.NextName, s.WriteHead, names,
			nullInt(s.MtimeNs), s.ExpiresNs, s.State, s.CacheMerged, s.ID)
		return ierr
	})
}

// AdvanceUploadCacheMerged records the merge's current extent.
//
// It touches a single column and never moves backwards, since it executes while
// chunks for the same session are still arriving. Writing the entire row from
// the merger would restore stale copies of fields the chunk writer has already
// advanced, and a retreating frontier would attempt to re-merge cache files that
// no longer exist.
func (d *DB) AdvanceUploadCacheMerged(ctx context.Context, id []byte, merged int64) error {
	return d.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, sqlAdvanceUploadCacheMerged, merged, id, merged)
		return err
	})
}

// RecordUploadInterval writes the ranges produced by a merge and extends the
// session's lifetime within one transaction.
//
// The complete set is rewritten instead of appending a single row, because an
// insert combining three runs into one must remove the two it absorbed. The run
// limit bounds the set, which bounds the write as well.
func (d *DB) RecordUploadInterval(
	ctx context.Context, id []byte, runs [][2]uint64, expiresNs int64,
) error {
	if err := d.f.EnsureWritable(); err != nil {
		return err
	}
	if len(runs) > limits.UploadIntervalRuns {
		return limits.Exceed("upload interval runs", limits.UploadIntervalRuns, int64(len(runs)))
	}
	return d.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, sqlDeleteUploadIntervals, id); err != nil {
			return err
		}
		for _, r := range runs {
			lo, lerr := num.Narrow[int64](r[0])
			hi, herr := num.Narrow[int64](r[1])
			if lerr != nil || herr != nil {
				return fmt.Errorf("an interval %d..%d does not fit the column: %w",
					r[0], r[1], errors.Join(lerr, herr))
			}
			if _, err := tx.ExecContext(ctx, sqlInsertUploadInterval, id, lo, hi); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, sqlTouchUploadSessionExpiry, expiresNs, id)
		return err
	})
}

// ReadUploadIntervals retrieves a session's recorded ranges in order.
func (d *DB) ReadUploadIntervals(ctx context.Context, id []byte) (out [][2]uint64, err error) {
	rows, err := d.f.SQL().QueryContext(ctx, sqlReadUploadIntervals, id)
	if err != nil {
		return nil, fmt.Errorf("reading upload intervals: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var lo, hi int64
		if serr := rows.Scan(&lo, &hi); serr != nil {
			return nil, fmt.Errorf("reading upload intervals: %w", serr)
		}
		ulo, lerr := num.Narrow[uint64](lo)
		uhi, herr := num.Narrow[uint64](hi)
		if lerr != nil || herr != nil {
			return nil, fmt.Errorf("a stored interval %d..%d is negative: %w",
				lo, hi, errors.Join(lerr, herr))
		}
		out = append(out, [2]uint64{ulo, uhi})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading upload intervals: %w", err)
	}
	return out, nil
}

// DeleteUploadSession removes a session along with its intervals and aliases.
// Both child tables cascade, and the alias deletion is stated explicitly because
// a transfer id surviving the session it names would continue addressing a
// released id.
func (d *DB) DeleteUploadSession(ctx context.Context, id []byte) error {
	return d.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, sqlDeleteUploadAliasesForSession, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, sqlDeleteUploadIntervals, id); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, sqlDeleteUploadSession, id)
		return err
	})
}

// CountUploadSessionsForUser reports how many receiving sessions an account
// holds.
func (d *DB) CountUploadSessionsForUser(ctx context.Context, user int64) (int64, error) {
	var n int64
	if err := d.f.SQL().QueryRowContext(ctx, sqlCountUploadSessionsForUser, user).Scan(&n); err != nil {
		return 0, fmt.Errorf("counting an account's upload sessions: %w", err)
	}
	return n, nil
}

// SumUploadReservedForUser is the declared length of everything an account
// has in flight. A declared length reserves a sparse part file, so this is
// what stops an account promising the disk away without writing a byte.
func (d *DB) SumUploadReservedForUser(ctx context.Context, user int64) (int64, error) {
	var n int64
	if err := d.f.SQL().QueryRowContext(ctx, sqlSumUploadReservedForUser, user).Scan(&n); err != nil {
		return 0, fmt.Errorf("summing an account's reserved upload bytes: %w", err)
	}
	return n, nil
}

// BindUploadAlias associates a transfer id with a session inside one account's
// namespace, returning false when the account already holds that id.
//
// Callers respond with a conflict rather than replacing the binding. A silent
// rebind would strand the first session's spool directory with nothing
// referencing it.
func (d *DB) BindUploadAlias(
	ctx context.Context, tid string, user int64, a UploadAlias, createdNs int64,
) (bool, error) {
	if err := d.f.EnsureWritable(); err != nil {
		return false, err
	}
	var bound bool
	err := d.Write(ctx, func(tx *sql.Tx) error {
		res, ierr := tx.ExecContext(ctx, sqlInsertUploadAlias,
			tid, user, a.Session, a.Share, a.Dest, createdNs)
		if ierr != nil {
			return ierr
		}
		n, rerr := res.RowsAffected()
		bound = n > 0
		return rerr
	})
	if err != nil {
		return false, fmt.Errorf("binding an upload transfer id: %w", err)
	}
	return bound, nil
}

// LookupUploadAlias resolves a transfer id inside one account's namespace. An id
// owned by a different account yields ErrNoSuchUploadSession, identical to one
// that never existed, keeping the lookup from acting as an existence oracle.
func (d *DB) LookupUploadAlias(ctx context.Context, tid string, user int64) (UploadAlias, error) {
	var a UploadAlias
	err := d.f.SQL().QueryRowContext(ctx, sqlReadUploadAlias, user, tid).
		Scan(&a.Session, &a.Share, &a.Dest)
	if errors.Is(err, sql.ErrNoRows) {
		return UploadAlias{}, ErrNoSuchUploadSession
	}
	if err != nil {
		return UploadAlias{}, fmt.Errorf("resolving an upload transfer id: %w", err)
	}
	return a, nil
}

// UnbindUploadAlias removes a transfer id from an account's namespace.
func (d *DB) UnbindUploadAlias(ctx context.Context, tid string, user int64) error {
	return d.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, sqlDeleteUploadAlias, user, tid)
		return err
	})
}

// TouchedDir names a directory where a part file has been created.
type TouchedDir struct {
	Share int64
	Dir   string
}

// TouchUploadDir notes a directory where a part file was created, so the sweep
// knows where to search for one whose session row has disappeared.
//
// How to treat a failure is the caller's decision rather than this package's.
// The associated upload is unaffected; what is lost is the sweep's record of
// where to look afterwards.
func (d *DB) TouchUploadDir(ctx context.Context, share int64, dir string) error {
	if err := d.f.EnsureWritable(); err != nil {
		return err
	}
	return d.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, sqlTouchUploadDir, share, dir)
		return err
	})
}

// ListUploadTouchedDirs returns every directory in which a part file has ever
// been created.
func (d *DB) ListUploadTouchedDirs(ctx context.Context) (out []TouchedDir, err error) {
	rows, err := d.f.SQL().QueryContext(ctx, sqlListUploadTouchedDirs)
	if err != nil {
		return nil, fmt.Errorf("listing the directories uploads have touched: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var t TouchedDir
		if serr := rows.Scan(&t.Share, &t.Dir); serr != nil {
			return nil, fmt.Errorf("listing the directories uploads have touched: %w", serr)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing the directories uploads have touched: %w", err)
	}
	return out, nil
}

// ChunkSettings returns the stored floor and default plus whether any row
// exists. That flag distinguishes values an administrator saved from the
// compiled-in fallbacks; without it both appear as the same pair of integers and
// the settings screen has nothing meaningful to display.
type ChunkSettings struct {
	Min      int64
	Default  int64
	Override bool
}

// ReadChunkSettings reads the admin override, reporting Override false when
// no row has ever been written.
func (d *DB) ReadChunkSettings(ctx context.Context) (ChunkSettings, error) {
	var c ChunkSettings
	err := d.f.SQL().QueryRowContext(ctx, sqlReadChunkSettings).Scan(&c.Min, &c.Default)
	if errors.Is(err, sql.ErrNoRows) {
		return ChunkSettings{}, nil
	}
	if err != nil {
		return ChunkSettings{}, fmt.Errorf("reading the chunk settings: %w", err)
	}
	c.Override = true
	return c, nil
}

// WriteChunkSettings stores an administrator's floor and default.
func (d *DB) WriteChunkSettings(ctx context.Context, minBytes, defaultBytes int64) error {
	if err := d.f.EnsureWritable(); err != nil {
		return err
	}
	return d.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, sqlWriteChunkSettings, minBytes, defaultBytes)
		return err
	})
}

// ReadUploadCacheEnabled reports whether chunks pass through the cache volume
// before reaching their destination. An absent row means disabled.
func (d *DB) ReadUploadCacheEnabled(ctx context.Context) (bool, error) {
	var on int64
	err := d.f.SQL().QueryRowContext(ctx, sqlReadCacheSettings).Scan(&on)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("reading the upload cache setting: %w", err)
	}
	return on != 0, nil
}

// WriteUploadCacheEnabled stores the cache switch.
func (d *DB) WriteUploadCacheEnabled(ctx context.Context, on bool) error {
	if err := d.f.EnsureWritable(); err != nil {
		return err
	}
	return d.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, sqlWriteCacheSettings, boolInt(on))
		return err
	})
}

func scanUploadSession(row interface{ Scan(...any) error }) (UploadSession, error) {
	var (
		s        UploadSession
		spoolDir sql.NullString
		totalLen sql.NullInt64
		ifMatch  sql.NullString
		mtimeNs  sql.NullInt64
		mime     sql.NullString
		relPath  sql.NullString
		verify   sql.NullInt64
		names    []byte
		cacheDir sql.NullString
	)
	if err := row.Scan(&s.ID, &s.User, &s.Share, &s.Dest, &s.PartName, &spoolDir, &s.Mode,
		&totalLen, &s.ChunkSize, &s.ChunkMinAtCreation, &s.RandomAccess, &s.NextName,
		&s.WriteHead, &names, &ifMatch, &s.Filename, &mtimeNs, &mime, &relPath,
		&verify, &s.VerifyDigest, &s.CreatedNs, &s.ExpiresNs, &s.State,
		&cacheDir, &s.CacheMerged); err != nil {
		return UploadSession{}, err
	}
	s.SpoolDir = spoolDir.String
	s.CacheDir = cacheDir.String
	s.IfMatch = ifMatch.String
	s.Mime = mime.String
	s.RelativePath = relPath.String
	if totalLen.Valid {
		v := totalLen.Int64
		s.TotalLen = &v
	}
	if mtimeNs.Valid {
		v := mtimeNs.Int64
		s.MtimeNs = &v
	}
	if verify.Valid {
		v := verify.Int64
		s.Verify = &v
	}
	spooled, err := decodeSpooledNames(names)
	if err != nil {
		return UploadSession{}, err
	}
	s.SpooledNames = spooled
	return s, nil
}

// encodeSpooledNames serializes out-of-order chunk names as fixed-width
// big-endian words. Fixed width rather than varint because the set is small,
// bounded, and read only by this file, and a decoder incapable of running off
// the end is one fewer thing to get wrong.
func encodeSpooledNames(names []uint32) ([]byte, error) {
	if len(names) > limits.UploadSpooledNames {
		return nil, limits.Exceed("upload spooled names",
			limits.UploadSpooledNames, int64(len(names)))
	}
	out := make([]byte, 4*len(names))
	for i, n := range names {
		binary.BigEndian.PutUint32(out[4*i:], n)
	}
	return out, nil
}

// decodeSpooledNames validates untrusted input on the way back in: the length
// must divide evenly and the count must respect the bound, since these bytes
// come from a file.
func decodeSpooledNames(b []byte) ([]uint32, error) {
	if len(b)%4 != 0 {
		return nil, fmt.Errorf(
			"a stored spooled-name list is %d bytes, which is not a whole number of names", len(b))
	}
	if len(b)/4 > limits.UploadSpooledNames {
		return nil, limits.Exceed("upload spooled names",
			limits.UploadSpooledNames, int64(len(b)/4))
	}
	if len(b) == 0 {
		return nil, nil
	}
	out := make([]uint32, len(b)/4)
	for i := range out {
		out[i] = binary.BigEndian.Uint32(b[4*i:])
	}
	return out, nil
}

// blobArg stores an empty blob as SQL NULL.
func blobArg(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}
