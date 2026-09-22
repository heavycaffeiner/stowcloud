package state

// Every statement the share registry runs, written out whole.
const (
	shareColumns = `id, name, host_path, shared_externally, trash_enabled, symlink_policy, created_ns,
backend, backend_config, backend_secret, backend_secret_keyver`

	sqlListShares = `SELECT ` + shareColumns + ` FROM share_definition ORDER BY id`

	sqlInsertShare = `
INSERT INTO share_definition(
	name, host_path, shared_externally, trash_enabled, symlink_policy, created_ns, backend, backend_config
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`

	// The definition minus the id, the creation time and the credential.
	// Neither of the first two may an edit alter: every grant, link and
	// cache row references the id, and the creation time records what
	// happened. The credential is written by sqlUpdateShareSecret instead,
	// so an edit that says nothing about it cannot overwrite it with
	// nothing.
	sqlUpdateShare = `
UPDATE share_definition
SET name = ?, host_path = ?, shared_externally = ?, trash_enabled = ?, symlink_policy = ?,
    backend = ?, backend_config = ?
WHERE id = ?`

	sqlUpdateShareSecret = `
UPDATE share_definition SET backend_secret = ?, backend_secret_keyver = ? WHERE id = ?`

	sqlDeleteShare = `DELETE FROM share_definition WHERE id = ?`
)
