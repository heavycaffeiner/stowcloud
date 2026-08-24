package state

// Every statement the share registry runs, as a constant (D14). Nothing is
// built from parts.
const (
	sqlListShares = `SELECT id, name, host_path, created_ns FROM share_definition ORDER BY id`

	sqlInsertShare = `INSERT INTO share_definition(name, host_path, created_ns) VALUES (?, ?, ?)`

	sqlUpdateShare = `UPDATE share_definition SET name = ?, host_path = ? WHERE id = ?`

	sqlDeleteShare = `DELETE FROM share_definition WHERE id = ?`

	sqlIdentityOverrideFor = `SELECT share_id, name, host_path FROM share_identity_override WHERE share_id = ?`

	sqlUpsertIdentityOverride = `
INSERT INTO share_identity_override(share_id, name, host_path) VALUES (?, ?, ?)
ON CONFLICT(share_id) DO UPDATE SET name = excluded.name, host_path = excluded.host_path`

	sqlTrashOverrideFor = `SELECT enabled FROM share_trash_override WHERE share_id = ?`

	sqlUpsertTrashOverride = `
INSERT INTO share_trash_override(share_id, enabled) VALUES (?, ?)
ON CONFLICT(share_id) DO UPDATE SET enabled = excluded.enabled`

	sqlCountOverrides = `
SELECT ((SELECT count(*) FROM share_identity_override) +
        (SELECT count(*) FROM share_trash_override))`
)
