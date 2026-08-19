package state

// Every statement the upload store runs, as a constant (D14).
const (
	sqlInsertUploadSession = `
INSERT INTO upload_session(id, user, share, dest, part_name, spool_dir, mode, total_len,
                           chunk_size, chunk_min_at_creation, random_access, next_name,
                           write_head, spooled_names, if_match, filename, mtime_ns, mime,
                           relative_path, verify, verify_digest, created_ns, expires_ns, state)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

	uploadSessionColumns = `
id, user, share, dest, part_name, spool_dir, mode, total_len, chunk_size,
chunk_min_at_creation, random_access, next_name, write_head, spooled_names,
if_match, filename, mtime_ns, mime, relative_path, verify, verify_digest,
created_ns, expires_ns, state`

	sqlReadUploadSession = `SELECT ` + uploadSessionColumns + ` FROM upload_session WHERE id = ?`

	// The sweep reads every session before it reads the directories, so a
	// session created between the two passes is not mistaken for an orphan.
	sqlListUploadSessions = `SELECT ` + uploadSessionColumns + ` FROM upload_session`

	sqlListExpiredUploadSessions = `
SELECT ` + uploadSessionColumns + ` FROM upload_session WHERE expires_ns <= ?`

	// The mutable half of a session row. The immutable columns (the
	// destination, the part name, the mode) are never rewritten: a session
	// that could move its own destination is a session that could publish
	// somewhere the ACL check never looked.
	sqlUpdateUploadSession = `
UPDATE upload_session
SET total_len = ?, next_name = ?, write_head = ?, spooled_names = ?,
    mtime_ns = ?, expires_ns = ?, state = ?
WHERE id = ?`

	sqlDeleteUploadSession = `DELETE FROM upload_session WHERE id = ?`

	sqlReadUploadIntervals = `SELECT lo, hi FROM upload_interval WHERE session = ? ORDER BY lo`

	// The interval set is rows rather than a blob, so a partially written set
	// is a shorter one rather than a corrupt one. An insert that overlaps an
	// existing run replaces it, which is what a merge writes back.
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

	// Scoped by the account on every read. The transfer id is the client's own
	// choice, so it is guessable; the account is what makes the lookup safe.
	sqlReadUploadAlias = `
SELECT session, share, dest FROM upload_alias WHERE user = ? AND tid = ?`

	sqlDeleteUploadAlias = `DELETE FROM upload_alias WHERE user = ? AND tid = ?`

	sqlDeleteUploadAliasesForSession = `DELETE FROM upload_alias WHERE session = ?`

	sqlReadChunkSettings = `SELECT chunk_min, chunk_default FROM upload_chunk_settings WHERE id = 1`

	sqlWriteChunkSettings = `
INSERT INTO upload_chunk_settings(id, chunk_min, chunk_default) VALUES (1, ?, ?)
ON CONFLICT(id) DO UPDATE SET chunk_min = excluded.chunk_min,
                              chunk_default = excluded.chunk_default`
)
