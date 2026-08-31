package state

// Every statement the share registry runs, written out whole.
const (
	shareColumns = `id, name, host_path, shared_externally, trash_enabled, symlink_policy, created_ns`

	sqlListShares = `SELECT ` + shareColumns + ` FROM share_definition ORDER BY id`

	sqlInsertShare = `
INSERT INTO share_definition(name, host_path, shared_externally, trash_enabled, symlink_policy, created_ns)
VALUES (?, ?, ?, ?, ?, ?)`

	// The definition minus the id and creation time, neither of which an edit
	// may alter. Every grant, link and cache row references the id, and the
	// creation time records what happened.
	sqlUpdateShare = `
UPDATE share_definition
SET name = ?, host_path = ?, shared_externally = ?, trash_enabled = ?, symlink_policy = ?
WHERE id = ?`

	sqlDeleteShare = `DELETE FROM share_definition WHERE id = ?`
)
