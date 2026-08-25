package state

// Every statement the operation store runs, as a constant (D14).
const (
	sqlInsertOp = `
INSERT INTO operation(user, kind, state, progress, total, message, cancellation, created_ns)
VALUES (?, ?, ?, 0, ?, ?, 0, ?)`

	sqlReadOp = `
SELECT id, user, kind, state, progress, total, message, cancellation, created_ns, finished_ns
FROM operation WHERE id = ?`

	// One account's unfinished operations, newest first and bounded.
	//
	// Unfinished only: this feeds the tray's re-attach after a reload, and a
	// finished operation reappearing there tells somebody that a copy they
	// watched complete an hour ago is running now. Nothing prunes the table,
	// so without the filter every page load reopened the tray with the
	// account's whole history in it. See ListOps.
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

	// The items with no result of their own, which is what a job that stopped
	// short did not finish. started tells the two cases apart: in flight when
	// the process died, or never reached.
	sqlReadOpUnfinished = `
SELECT i.path, i.started
FROM operation_item i
LEFT JOIN operation_result r ON r.operation = i.operation AND r.idx = i.idx
WHERE i.operation = ? AND r.idx IS NULL
ORDER BY i.idx`
)
