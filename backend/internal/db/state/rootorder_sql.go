package state

const (
	sqlSelectRootOrder = `
SELECT label
  FROM root_order
 WHERE user = ?
 ORDER BY position`

	sqlDeleteRootOrder = `
DELETE FROM root_order
 WHERE user = ?`

	sqlInsertRootOrderEntry = `
INSERT INTO root_order(user, label, position)
VALUES (?, ?, ?)`
)
