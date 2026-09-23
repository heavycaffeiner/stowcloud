package state

// Every statement the operation store runs, written out whole.
const (
	sqlInsertOp = `
INSERT INTO operation(user, kind, state, progress, total, message, cancellation, created_ns, updated_ns, next_run_ns)
VALUES (?, ?, ?, 0, ?, ?, 0, ?, ?, ?)`

	sqlInsertQueuedOp = `
INSERT INTO operation(user, kind, state, progress, total, message, cancellation, created_ns,
 payload, progress_unit, attempt, max_attempts, next_run_ns, updated_ns)
VALUES (?, ?, ?, 0, ?, NULL, 0, ?, ?, ?, 0, ?, ?, ?)`

	sqlReadOp = `
SELECT id, user, kind, state, progress, total, message, cancellation, created_ns, finished_ns,
 payload, progress_unit, attempt, max_attempts, next_run_ns, updated_ns, started_ns,
 error_key, error_detail, lease_id, lease_expires_ns, pause_requested
FROM operation WHERE id = ?`

	sqlListOps = `
	SELECT id, user, kind, state, progress, total, message, cancellation, created_ns, finished_ns,
	 payload, progress_unit, attempt, max_attempts, next_run_ns, updated_ns, started_ns,
	 error_key, error_detail, lease_id, lease_expires_ns, pause_requested
	FROM operation WHERE user = ? AND state IN (?, ?, ?, ?) ORDER BY id DESC LIMIT ?`
	sqlReadOpResults = `
SELECT operation, idx, path, ok, reason, text
FROM operation_result WHERE operation = ? ORDER BY idx`

	sqlSetOpProgress = `UPDATE operation SET progress = ?, message = ?, updated_ns = updated_ns + 1 WHERE id = ?`

	sqlRequestOpCancel = `UPDATE operation SET cancellation = 1, pause_requested = 0 WHERE id = ?`

	sqlSetOpState = `
UPDATE operation
SET state = ?, progress = ?, message = ?, finished_ns = ?, updated_ns = updated_ns + 1,
 error_key = NULL, error_detail = NULL, lease_id = NULL, lease_expires_ns = 0
WHERE id = ?`

	sqlInterruptOp = `
UPDATE operation
SET state = ?, message = ?, finished_ns = ?, updated_ns = updated_ns + 1,
 lease_id = NULL, lease_expires_ns = 0
WHERE id = ?`

	sqlInsertOpResult = `
INSERT INTO operation_result(operation, idx, path, ok, reason, text)
VALUES (?, ?, ?, ?, ?, ?)`

	sqlInsertOpItem = `
INSERT INTO operation_item(operation, idx, path, started)
VALUES (?, ?, ?, 0)`

	sqlMarkOpItemStarted = `UPDATE operation_item SET started = 1 WHERE operation = ? AND idx = ?`

	sqlReadOpUnfinished = `
SELECT i.path, i.started
FROM operation_item i
LEFT JOIN operation_result r ON r.operation = i.operation AND r.idx = i.idx
WHERE i.operation = ? AND r.idx IS NULL
ORDER BY i.idx`
)
