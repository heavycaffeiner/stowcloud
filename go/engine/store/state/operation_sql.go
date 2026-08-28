package state

// Every statement the operation store runs, written out whole.
const (
	sqlInsertOp = `
INSERT INTO operation(user, kind, state, progress, total, message, cancellation, created_ns)
VALUES (?, ?, ?, 0, ?, ?, 0, ?)`

	sqlReadOp = `
SELECT id, user, kind, state, progress, total, message, cancellation, created_ns, finished_ns
FROM operation WHERE id = ?`

	// An account's unfinished operations, newest first, subject to a limit.
	//
	// Restricted to unfinished because this serves a client reattaching after a
	// reload. A completed operation resurfacing here would suggest that a copy
	// somebody watched finish an hour ago is running now. Nothing prunes the
	// table, so omitting the filter would reopen the entire history on every
	// page load.
	sqlListOps = `
SELECT id, user, kind, state, progress, total, message, created_ns, finished_ns
FROM operation WHERE user = ? AND state IN (?, ?) ORDER BY id DESC LIMIT ?`

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

	sqlInsertOpItem = `
INSERT INTO operation_item(operation, idx, path, started)
VALUES (?, ?, ?, 0)`

	sqlMarkOpItemStarted = `UPDATE operation_item SET started = 1 WHERE operation = ? AND idx = ?`

	// Items lacking a result of their own, which is exactly what a job that
	// halted early left undone. started separates the two possibilities: in
	// flight when the process died, or never reached at all.
	sqlReadOpUnfinished = `
SELECT i.path, i.started
FROM operation_item i
LEFT JOIN operation_result r ON r.operation = i.operation AND r.idx = i.idx
WHERE i.operation = ? AND r.idx IS NULL
ORDER BY i.idx`
)
