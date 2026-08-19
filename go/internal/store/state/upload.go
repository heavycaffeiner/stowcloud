package state

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/heavycaffeiner/stowcloud/go/internal/limits"
	"github.com/heavycaffeiner/stowcloud/go/internal/num"
)

// The upload session store. The rows live here rather than in a database of
// their own because an interrupted upload whose session row vanished is a part
// file nobody will ever finish, and that is data loss dressed as cleanup.

// ErrNoSuchUploadSession is a session id that holds no row.
var ErrNoSuchUploadSession = errors.New("no such upload session")

// UploadSession is one row of upload_session. It is the store's shape, not the
// engine's: the engine holds the interval set as a set and this holds it as
// rows in a second table.
type UploadSession struct {
	ID   []byte
	User int64
	// Share is the share the finished file lands in. A caller holding only a
	// session id has to be able to reach the share root without keeping an
	// id-to-share table of its own that a restart would lose.
	Share int64
	// Dest is the share-relative destination path. It is never rewritten: a
	// session that could move its own destination could publish somewhere the
	// permission check never looked.
	Dest string
	// PartName is the reserved control name the bytes accumulate under, in the
	// destination's own directory, because the rename that publishes it is
	// only atomic within one directory.
	PartName string
	// SpoolDir is set for a name-ordered session and empty otherwise.
	SpoolDir string
	Mode     int64
	// TotalLen is nil for a deferred length, which the client may supply later
	// and finalize requires.
	TotalLen *int64
	// ChunkSize is the size advertised at creation, and ChunkMinAtCreation is
	// the floor snapshotted then. The floor is a snapshot rather than a live
	// read so an admin moving it mid-upload cannot retroactively make an
	// already-legal chunk illegal for a session that started under the old one.
	ChunkSize          int64
	ChunkMinAtCreation int64
	RandomAccess       bool
	// NextName and WriteHead are the name-ordered assembly cursor: the chunk
	// name expected next, and where in the part file it will be written.
	NextName  int64
	WriteHead int64
	// SpooledNames are the out-of-order chunk names sitting in the spool
	// directory waiting for their predecessor.
	SpooledNames []uint32
	IfMatch      string
	Filename     string
	MtimeNs      *int64
	Mime         string
	RelativePath string
	// Verify and VerifyDigest are always written together. An algorithm with
	// nothing to compare against is the shape that shipped once and could
	// never fail, so the pair is stored and read as a pair.
	Verify       *int64
	VerifyDigest []byte
	CreatedNs    int64
	ExpiresNs    int64
	State        int64
}

// UploadAlias is a client-chosen transfer id bound to a session, within one
// account's namespace.
type UploadAlias struct {
	Session []byte
	Share   int64
	Dest    string
}

// CreateUploadSession writes a session row and its (empty) interval set in one
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
			s.ID, s.User, s.Share, s.Dest, s.PartName, strArg(s.SpoolDir), s.Mode,
			nullInt(s.TotalLen), s.ChunkSize, s.ChunkMinAtCreation, s.RandomAccess,
			s.NextName, s.WriteHead, names, strArg(s.IfMatch), s.Filename,
			nullInt(s.MtimeNs), strArg(s.Mime), strArg(s.RelativePath),
			nullInt(s.Verify), blobArg(s.VerifyDigest), s.CreatedNs, s.ExpiresNs, s.State)
		return ierr
	})
}

// ReadUploadSession reads one session row, without its intervals.
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

// ListUploadSessions reads every session row. The sweep takes this before it
// reads a single directory, so a session created between the two passes is not
// mistaken for an orphan.
func (d *DB) ListUploadSessions(ctx context.Context) ([]UploadSession, error) {
	return d.queryUploadSessions(ctx, sqlListUploadSessions)
}

// ListExpiredUploadSessions reads the sessions whose lifetime has run out.
func (d *DB) ListExpiredUploadSessions(ctx context.Context, nowNs int64) ([]UploadSession, error) {
	return d.queryUploadSessions(ctx, sqlListExpiredUploadSessions, nowNs)
}

func (d *DB) queryUploadSessions(ctx context.Context, stmt string, args ...any) (out []UploadSession, err error) {
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
	return out, rows.Err()
}

// UpdateUploadSession rewrites the mutable half of a session row.
func (d *DB) UpdateUploadSession(ctx context.Context, s UploadSession) error {
	names, err := encodeSpooledNames(s.SpooledNames)
	if err != nil {
		return err
	}
	return d.Write(ctx, func(tx *sql.Tx) error {
		_, ierr := tx.ExecContext(ctx, sqlUpdateUploadSession,
			nullInt(s.TotalLen), s.NextName, s.WriteHead, names,
			nullInt(s.MtimeNs), s.ExpiresNs, s.State, s.ID)
		return ierr
	})
}

// RecordUploadInterval writes the ranges a merge produced and refreshes the
// session's lifetime, in one transaction.
//
// The whole set is rewritten rather than one row appended, because an insert
// that merged three runs into one has to delete the two it absorbed. The set
// is bounded by the run limit, so the write is bounded too.
func (d *DB) RecordUploadInterval(ctx context.Context, id []byte, runs [][2]uint64, expiresNs int64) error {
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

// ReadUploadIntervals reads one session's recorded ranges in order.
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
	return out, rows.Err()
}

// DeleteUploadSession removes a session, its intervals and its aliases. The
// two child tables cascade, and the alias delete is written out because a
// transfer id outliving the session it names would keep addressing a freed id.
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

// CountUploadSessionsForUser is how many receiving sessions an account holds.
func (d *DB) CountUploadSessionsForUser(ctx context.Context, user int64) (int64, error) {
	var n int64
	err := d.f.SQL().QueryRowContext(ctx, sqlCountUploadSessionsForUser, user).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("counting an account's upload sessions: %w", err)
	}
	return n, nil
}

// SumUploadReservedForUser is the declared length of everything an account has
// in flight. A declared length reserves a sparse part file, so this is what
// stops an account promising the disk away without writing a byte.
func (d *DB) SumUploadReservedForUser(ctx context.Context, user int64) (int64, error) {
	var n int64
	err := d.f.SQL().QueryRowContext(ctx, sqlSumUploadReservedForUser, user).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("summing an account's reserved upload bytes: %w", err)
	}
	return n, nil
}

// BindUploadAlias binds a transfer id to a session inside one account's
// namespace, and reports false when the account already holds that id.
//
// The caller answers a conflict rather than replacing: a silent rebind would
// orphan the first session's spool directory with nothing left pointing at it.
func (d *DB) BindUploadAlias(ctx context.Context, tid string, user int64, a UploadAlias, createdNs int64) (bool, error) {
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

// LookupUploadAlias resolves a transfer id within one account's namespace. A
// tid belonging to another account resolves to ErrNoSuchUploadSession, exactly
// like one that never existed, so the lookup is not an existence oracle.
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

// UnbindUploadAlias drops one transfer id from an account's namespace.
func (d *DB) UnbindUploadAlias(ctx context.Context, tid string, user int64) error {
	return d.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, sqlDeleteUploadAlias, user, tid)
		return err
	})
}

// TouchedDir is one directory a part file has been created in.
type TouchedDir struct {
	Share int64
	Dir   string
}

// TouchUploadDir records a directory a part file was created in, so the sweep
// knows where to look for one whose session row is gone.
//
// A failure is the caller's to decide about rather than this package's: the
// upload it belongs to is fine, and what has been lost is the sweep's record
// of where to look later.
func (d *DB) TouchUploadDir(ctx context.Context, share int64, dir string) error {
	if err := d.f.EnsureWritable(); err != nil {
		return err
	}
	return d.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, sqlTouchUploadDir, share, dir)
		return err
	})
}

// ListUploadTouchedDirs is every directory a part file has ever been created
// in.
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
	return out, rows.Err()
}

// ChunkSettings is the persisted floor and default, and whether a row exists
// at all. The flag is what separates "an admin stored this" from "it fell back
// to the config file": without it both are the same pair of integers and the
// settings screen has nothing to report.
type ChunkSettings struct {
	Min      int64
	Default  int64
	Override bool
}

// ReadChunkSettings reads the admin override, reporting Override false when no
// row has ever been written.
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

// WriteChunkSettings persists an admin's floor and default.
func (d *DB) WriteChunkSettings(ctx context.Context, minBytes, defaultBytes int64) error {
	if err := d.f.EnsureWritable(); err != nil {
		return err
	}
	return d.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, sqlWriteChunkSettings, minBytes, defaultBytes)
		return err
	})
}

// scanner is the one row shape both QueryRow and Rows satisfy.
type uploadScanner interface {
	Scan(dest ...any) error
}

func scanUploadSession(row uploadScanner) (UploadSession, error) {
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
	)
	if err := row.Scan(&s.ID, &s.User, &s.Share, &s.Dest, &s.PartName, &spoolDir, &s.Mode,
		&totalLen, &s.ChunkSize, &s.ChunkMinAtCreation, &s.RandomAccess, &s.NextName,
		&s.WriteHead, &names, &ifMatch, &s.Filename, &mtimeNs, &mime, &relPath,
		&verify, &s.VerifyDigest, &s.CreatedNs, &s.ExpiresNs, &s.State); err != nil {
		return UploadSession{}, err
	}
	s.SpoolDir = spoolDir.String
	s.IfMatch = ifMatch.String
	s.Mime = mime.String
	s.RelativePath = relPath.String
	if totalLen.Valid {
		s.TotalLen = &totalLen.Int64
	}
	if mtimeNs.Valid {
		s.MtimeNs = &mtimeNs.Int64
	}
	if verify.Valid {
		s.Verify = &verify.Int64
	}
	spooled, err := decodeSpooledNames(names)
	if err != nil {
		return UploadSession{}, err
	}
	s.SpooledNames = spooled
	return s, nil
}

// encodeSpooledNames packs the out-of-order chunk names as fixed-width
// big-endian words. Fixed width rather than a varint because the set is small,
// bounded and never read by anything but this file, and a decoder that cannot
// run off the end is one less thing to get right.
func encodeSpooledNames(names []uint32) ([]byte, error) {
	if len(names) > limits.UploadSpooledNames {
		return nil, limits.Exceed("upload spooled names", limits.UploadSpooledNames, int64(len(names)))
	}
	out := make([]byte, 4*len(names))
	for i, n := range names {
		binary.BigEndian.PutUint32(out[4*i:], n)
	}
	return out, nil
}

// decodeSpooledNames is the trust boundary on the way back: the length has to
// divide evenly and the count has to be within the bound, because these are
// bytes read back from a file.
func decodeSpooledNames(b []byte) ([]uint32, error) {
	if len(b)%4 != 0 {
		return nil, fmt.Errorf("a stored spooled-name list is %d bytes, which is not a whole number of names", len(b))
	}
	if len(b)/4 > limits.UploadSpooledNames {
		return nil, limits.Exceed("upload spooled names", limits.UploadSpooledNames, int64(len(b)/4))
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

func nullInt(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}

func blobArg(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}
