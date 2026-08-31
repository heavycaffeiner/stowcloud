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
)
