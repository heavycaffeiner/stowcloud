package state

// D14: every statement is a constant and every value is a parameter. Nothing
// here is built from a string a caller supplied.

const sqlSelectDavProps = `
SELECT ns, name, value
  FROM dav_prop
 WHERE share = ? AND dev = ? AND ino = ? AND btime_present = ? AND btime_ns = ?
 ORDER BY ns, name`

const sqlCountDavProps = `
SELECT COUNT(*)
  FROM dav_prop
 WHERE share = ? AND dev = ? AND ino = ? AND btime_present = ? AND btime_ns = ?`

const sqlDavPropExists = `
SELECT COUNT(*)
  FROM dav_prop
 WHERE share = ? AND dev = ? AND ino = ? AND btime_present = ? AND btime_ns = ?
   AND ns = ? AND name = ?`

const sqlUpsertDavProp = `
INSERT INTO dav_prop(share, dev, ino, btime_present, btime_ns, ns, name, value)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(share, dev, ino, btime_present, btime_ns, ns, name)
DO UPDATE SET value = excluded.value`

const sqlDeleteDavProp = `
DELETE FROM dav_prop
 WHERE share = ? AND dev = ? AND ino = ? AND btime_present = ? AND btime_ns = ?
   AND ns = ? AND name = ?`

const sqlDeleteDavPropsAll = `
DELETE FROM dav_prop
 WHERE share = ? AND dev = ? AND ino = ? AND btime_present = ? AND btime_ns = ?`

// Locks are read with the deadline applied, so an expired row can never be
// honoured even if the sweep has not run.
const sqlSelectDavLocks = `
SELECT token, share, dev, ino, btime_present, btime_ns,
       path, principal, owner, depth, scope, expires_ns, timeout_s
  FROM dav_lock
 WHERE expires_ns > ?
 ORDER BY token`

const sqlCountDavLocks = `
SELECT COUNT(*) FROM dav_lock WHERE principal = ? AND expires_ns > ?`

const sqlInsertDavLock = `
INSERT INTO dav_lock(token, share, dev, ino, btime_present, btime_ns,
                     path, principal, owner, depth, scope, expires_ns, timeout_s)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

const sqlRefreshDavLock = `
UPDATE dav_lock SET expires_ns = ?, timeout_s = ?
 WHERE token = ? AND expires_ns > ?`

const sqlDeleteDavLock = `DELETE FROM dav_lock WHERE token = ?`

const sqlSweepDavLocks = `DELETE FROM dav_lock WHERE expires_ns <= ?`
