package state

// Every statement the upload store runs, written out whole.
const (
	sqlInsertUploadSession = `
INSERT INTO upload_session(id, user, share, dest, part_name, spool_dir, mode, total_len,
                           chunk_size, chunk_min_at_creation, random_access, next_name,
                           write_head, spooled_names, if_match, filename, mtime_ns, mime,
                           relative_path, verify, verify_digest, created_ns, expires_ns, state,
                           cache_dir, cache_merged)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

	uploadSessionColumns = `
id, user, share, dest, part_name, spool_dir, mode, total_len, chunk_size,
chunk_min_at_creation, random_access, next_name, write_head, spooled_names,
if_match, filename, mtime_ns, mime, relative_path, verify, verify_digest,
created_ns, expires_ns, state, cache_dir, cache_merged`

	sqlReadUploadSession = `SELECT ` + uploadSessionColumns + ` FROM upload_session WHERE id = ?`

	// Every session is read before any directory, so a session created between
	// the two passes is never mistaken for an orphan.
	sqlListUploadSessions = `SELECT ` + uploadSessionColumns + ` FROM upload_session`

	sqlListExpiredUploadSessions = `
SELECT ` + uploadSessionColumns + ` FROM upload_session WHERE expires_ns <= ?`

	// The changeable portion of a session row. Destination, part name and mode
	// are never rewritten, because a session able to relocate its own
	// destination could publish somewhere the permission check never
	// examined.
	sqlUpdateUploadSession = `
UPDATE upload_session
SET total_len = ?, next_name = ?, write_head = ?, spooled_names = ?,
    mtime_ns = ?, expires_ns = ?, state = ?, cache_merged = ?
WHERE id = ?`

	// The merge frontier advances independently of the rest of the row. The
	// merger runs while chunks continue arriving, so writing the full row from
	// there would restore stale copies of fields another writer has already
	// advanced.
	sqlAdvanceUploadCacheMerged = `
UPDATE upload_session SET cache_merged = ? WHERE id = ? AND cache_merged < ?`

	sqlDeleteUploadSession = `DELETE FROM upload_session WHERE id = ?`

	sqlReadUploadIntervals = `SELECT lo, hi FROM upload_interval WHERE session = ? ORDER BY lo`

	// Intervals are stored as rows rather than a blob, so a partially written
	// set is merely shorter instead of corrupt.
	sqlInsertUploadInterval = `
INSERT INTO upload_interval(session, lo, hi) VALUES (?, ?, ?)
ON CONFLICT(session, lo) DO UPDATE SET hi = excluded.hi`

	sqlDeleteUploadIntervals = `DELETE FROM upload_interval WHERE session = ?`

	// Recording a range is itself evidence the session is alive, so the lifetime
	// extension shares the transaction with the ranges.
	sqlTouchUploadSessionExpiry = `UPDATE upload_session SET expires_ns = ? WHERE id = ?`

	sqlCountUploadSessionsForUser = `
SELECT count(*) FROM upload_session WHERE user = ? AND state = 0`

	// Only receiving sessions reserve space. An aborted session's part file
	// belongs to the sweep, and counting it would deny an account capacity it is
	// about to reclaim.
	sqlSumUploadReservedForUser = `
SELECT coalesce(sum(coalesce(total_len, 0)), 0) FROM upload_session
WHERE user = ? AND state = 0`

	sqlInsertUploadAlias = `
INSERT INTO upload_alias(tid, user, session, share, dest, created_ns)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(user, tid) DO NOTHING`

	// Scoped by the account on every read. The transfer id is the client's
	// own choice, so it is guessable; the account is what makes the lookup
	// safe.
	sqlReadUploadAlias = `
SELECT session, share, dest FROM upload_alias WHERE user = ? AND tid = ?`

	sqlDeleteUploadAlias = `DELETE FROM upload_alias WHERE user = ? AND tid = ?`

	sqlDeleteUploadAliasesForSession = `DELETE FROM upload_alias WHERE session = ?`

	// Entries are only ever added, never removed. A directory that held one part
	// file will hold another, and dropping it is how the sweep loses the very
	// orphan it exists to locate.
	sqlTouchUploadDir = `
INSERT INTO upload_touched_dir(share, dir) VALUES (?, ?)
ON CONFLICT(share, dir) DO NOTHING`

	sqlListUploadTouchedDirs = `SELECT share, dir FROM upload_touched_dir`

	sqlReadChunkSettings = `SELECT chunk_min, chunk_default FROM upload_chunk_settings WHERE id = 1`

	sqlWriteChunkSettings = `
INSERT INTO upload_chunk_settings(id, chunk_min, chunk_default) VALUES (1, ?, ?)
ON CONFLICT(id) DO UPDATE SET chunk_min = excluded.chunk_min,
                              chunk_default = excluded.chunk_default`

	sqlReadCacheSettings = `SELECT enabled FROM upload_cache_settings WHERE id = 1`

	sqlWriteCacheSettings = `
INSERT INTO upload_cache_settings(id, enabled) VALUES (1, ?)
ON CONFLICT(id) DO UPDATE SET enabled = excluded.enabled`
)
