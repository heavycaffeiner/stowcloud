package state

const (
	sqlSelectDavProps = `
SELECT ns, name, value
  FROM dav_prop
 WHERE share = ? AND dev = ? AND ino = ? AND btime_present = ? AND btime_ns = ?
 ORDER BY ns, name`

	sqlCountDavProps = `
SELECT COUNT(*)
  FROM dav_prop
 WHERE share = ? AND dev = ? AND ino = ? AND btime_present = ? AND btime_ns = ?`

	sqlDavPropExists = `
SELECT COUNT(*)
  FROM dav_prop
 WHERE share = ? AND dev = ? AND ino = ? AND btime_present = ? AND btime_ns = ?
   AND ns = ? AND name = ?`

	sqlUpsertDavProp = `
INSERT INTO dav_prop(share, dev, ino, btime_present, btime_ns, ns, name, value)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(share, dev, ino, btime_present, btime_ns, ns, name)
DO UPDATE SET value = excluded.value`

	sqlDeleteDavProp = `
DELETE FROM dav_prop
 WHERE share = ? AND dev = ? AND ino = ? AND btime_present = ? AND btime_ns = ?
   AND ns = ? AND name = ?`

	sqlDeleteDavPropsAll = `
DELETE FROM dav_prop
 WHERE share = ? AND dev = ? AND ino = ? AND btime_present = ? AND btime_ns = ?`
)

// Reads apply the deadline, so an expired row can never be enforced even when
// the sweep has not yet run.
const (
	sqlSelectDavLocks = `
SELECT token, share, dev, ino, btime_present, btime_ns,
       path, principal, owner, depth, scope, expires_ns, timeout_s
  FROM dav_lock
 WHERE expires_ns > ?
 ORDER BY token`

	sqlCountDavLocks = `
SELECT COUNT(*) FROM dav_lock WHERE principal = ? AND expires_ns > ?`

	sqlInsertDavLock = `
INSERT INTO dav_lock(token, share, dev, ino, btime_present, btime_ns,
                     path, principal, owner, depth, scope, expires_ns, timeout_s)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

	sqlRefreshDavLock = `
UPDATE dav_lock SET expires_ns = ?, timeout_s = ?
 WHERE token = ? AND expires_ns > ?`

	sqlDeleteDavLock = `DELETE FROM dav_lock WHERE token = ?`

	sqlSweepDavLocks = `DELETE FROM dav_lock WHERE expires_ns <= ?`
)
