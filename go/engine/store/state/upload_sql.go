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

	// The sweep reads every session before it reads the directories, so a
	// session created between the two passes is not mistaken for an orphan.
	sqlListUploadSessions = `SELECT ` + uploadSessionColumns + ` FROM upload_session`

	sqlListExpiredUploadSessions = `
SELECT ` + uploadSessionColumns + ` FROM upload_session WHERE expires_ns <= ?`

	// The mutable half of a session row. The immutable columns (the
	// destination, the part name, the mode) are never rewritten: a session
	// that could move its own destination is a session that could publish
	// somewhere the permission check never looked.
	sqlUpdateUploadSession = `
UPDATE upload_session
SET total_len = ?, next_name = ?, write_head = ?, spooled_names = ?,
    mtime_ns = ?, expires_ns = ?, state = ?, cache_merged = ?
WHERE id = ?`

	// The merge frontier moves on its own, without the rest of the row: the
	// merger runs while chunks are still arriving, and writing the whole row
	// from there would put back a copy of fields another writer has since
	// moved.
	sqlAdvanceUploadCacheMerged = `
UPDATE upload_session SET cache_merged = ? WHERE id = ? AND cache_merged < ?`

	sqlDeleteUploadSession = `DELETE FROM upload_session WHERE id = ?`

	sqlReadUploadIntervals = `SELECT lo, hi FROM upload_interval WHERE session = ? ORDER BY lo`

	// The interval set is rows rather than a blob, so a partially written set
	// is a shorter one rather than a corrupt one.
	sqlInsertUploadInterval = `
INSERT INTO upload_interval(session, lo, hi) VALUES (?, ?, ?)
ON CONFLICT(session, lo) DO UPDATE SET hi = excluded.hi`

	sqlDeleteUploadIntervals = `DELETE FROM upload_interval WHERE session = ?`

	// Recording a range is also what proves the session is alive, so the
	// lifetime is pushed out in the same transaction as the ranges.
	sqlTouchUploadSessionExpiry = `UPDATE upload_session SET expires_ns = ? WHERE id = ?`

	sqlCountUploadSessionsForUser = `
SELECT count(*) FROM upload_session WHERE user = ? AND state = 0`

	// Only a receiving session reserves anything: an aborted one's part file
	// belongs to the sweep, and counting it would refuse an account room it
	// is about to get back.
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

	// The set is added to and never deleted from: a directory that held one
	// part file will hold another, and forgetting it is how the sweep loses
	// the orphan it exists to find.
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
