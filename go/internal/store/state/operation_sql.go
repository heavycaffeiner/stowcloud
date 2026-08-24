package state

// Every statement the operation store runs, as a constant (D14).
const (
	sqlInsertOp = `
INSERT INTO operation(user, kind, state, progress, total, message, cancellation, created_ns)
VALUES (?, ?, ?, 0, ?, ?, 0, ?)`

	sqlReadOp = `
SELECT id, user, kind, state, progress, total, message, cancellation, created_ns, finished_ns
FROM operation WHERE id = ?`

	// One account's operations, newest first and bounded. See ListOps.
	sqlListOps = `
SELECT id, user, kind, state, progress, total, message, created_ns, finished_ns
FROM operation WHERE user = ? ORDER BY id DESC LIMIT ?`

	sqlReadOpResults = `
SELECT operation, idx, path, ok, reason, text
FROM operation_result WHERE operation = ? ORDER BY idx`

	sqlSetOpProgress = `UPDATE operation SET progress = ?, message = ? WHERE id = ?`

	sqlRequestOpCancel = `UPDATE operation SET cancellation = 1 WHERE id = ?`

	sqlSetOpState = `
UPDATE operation
SET state = ?, progress = ?, message = ?, finished_ns = ?
WHERE id = ?`

	sqlInsertOpResult = `
INSERT INTO operation_result(operation, idx, path, ok, reason, text)
VALUES (?, ?, ?, ?, ?, ?)`
)
