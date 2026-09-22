package state

const (
	sqlInsertAudit = `
INSERT INTO audit(ts_ns, actor, event, target, ip, ua, result, detail)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`

	// Two constants rather than one with an appended clause: the cursor is
	// optional, and a statement built from optional parts is what these being
	// constants prevents.
	sqlSelectAuditPage = `
SELECT rowid, ts_ns, actor, event, target, ip, ua, result, detail
  FROM audit
 ORDER BY rowid DESC
 LIMIT ?`

	sqlSelectAuditPageBefore = `
SELECT rowid, ts_ns, actor, event, target, ip, ua, result, detail
  FROM audit
 WHERE rowid < ?
 ORDER BY rowid DESC
 LIMIT ?`

	sqlSelectAuditPageEvent = `
SELECT rowid, ts_ns, actor, event, target, ip, ua, result, detail
  FROM audit
 WHERE event = ?
 ORDER BY rowid DESC
 LIMIT ?`

	sqlSelectAuditPageBeforeEvent = `
SELECT rowid, ts_ns, actor, event, target, ip, ua, result, detail
  FROM audit
 WHERE event = ? AND rowid < ?
 ORDER BY rowid DESC
 LIMIT ?`

	sqlSelectAuditPageActor = `
SELECT rowid, ts_ns, actor, event, target, ip, ua, result, detail
  FROM audit
 WHERE actor = ?
 ORDER BY rowid DESC
 LIMIT ?`

	sqlSelectAuditPageBeforeActor = `
SELECT rowid, ts_ns, actor, event, target, ip, ua, result, detail
  FROM audit
 WHERE actor = ? AND rowid < ?
 ORDER BY rowid DESC
 LIMIT ?`

	sqlSelectAuditCounts = `
SELECT ((ts_ns - ?) / ?) AS bucket_idx, result, COUNT(*)
  FROM audit
 WHERE ts_ns >= ? AND ts_ns <= ?
 GROUP BY bucket_idx, result`

	sqlSelectAuditCountsEvent = `
SELECT ((ts_ns - ?) / ?) AS bucket_idx, result, COUNT(*)
  FROM audit
 WHERE ts_ns >= ? AND ts_ns <= ? AND event = ?
 GROUP BY bucket_idx, result`

	sqlDeleteAuditBefore = `DELETE FROM audit WHERE ts_ns < ?`

	sqlDeleteAuditOldestKeepN = `
DELETE FROM audit
 WHERE rowid NOT IN (
   SELECT rowid FROM audit ORDER BY rowid DESC LIMIT ?
 )`
)
