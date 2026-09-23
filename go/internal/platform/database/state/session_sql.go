package state

const (
	sqlInsertSession = `
INSERT INTO session(id_hash, user, created_ns, last_seen_ns, absolute_expiry_ns,
                    ip_first, ua_first, amr)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`

	sqlSelectSession = `
SELECT id_hash, user, created_ns, last_seen_ns, absolute_expiry_ns,
       ip_first, ua_first, amr
  FROM session
 WHERE id_hash = ?`

	sqlListSessions = `
SELECT id_hash, created_ns, last_seen_ns, absolute_expiry_ns, ip_first, ua_first, amr
  FROM session
 WHERE user = ?
 ORDER BY last_seen_ns DESC`

	sqlTouchSession = `UPDATE session SET last_seen_ns = ? WHERE id_hash = ?`

	sqlDeleteSession = `DELETE FROM session WHERE id_hash = ?`

	sqlDeleteSessionOfUser = `DELETE FROM session WHERE user = ? AND id_hash = ?`

	sqlDeleteSessionsOfUser = `DELETE FROM session WHERE user = ?`
)
