package state

// Every statement the share registry runs, as a constant (D14).
const (
	shareColumns = `id, name, host_path, shared_externally, trash_enabled, symlink_policy, created_ns`

	sqlListShares = `SELECT ` + shareColumns + ` FROM share_definition ORDER BY id`

	sqlInsertShare = `
INSERT INTO share_definition(name, host_path, shared_externally, trash_enabled, symlink_policy, created_ns)
VALUES (?, ?, ?, ?, ?, ?)`

	// The definition, without the id or the creation time: neither is
	// something an edit may move. The id is referenced by every grant, every
	// link and every cache row, and the creation time is a fact.
	sqlUpdateShare = `
UPDATE share_definition
SET name = ?, host_path = ?, shared_externally = ?, trash_enabled = ?, symlink_policy = ?
WHERE id = ?`

	sqlDeleteShare = `DELETE FROM share_definition WHERE id = ?`
)
