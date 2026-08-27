package state

const sqlReadShareLinkTargets = `
SELECT id, dev, ino, btime_present, btime_ns FROM share_link`

// The columns a link row carries, named once so every read of the table
// scans the same shape in the same order.
const linkColumns = `
id, token_hash, token_enc, token_key_ver, share, path,
dev, ino, btime_present, btime_ns,
owner, perms, password_hash, expires_ns, max_downloads,
downloads, label, note, created_ns`

const (
	sqlInsertLink = `
INSERT INTO share_link(token_hash, token_enc, token_key_ver, share, path,
                       dev, ino, btime_present, btime_ns,
                       owner, perms, password_hash, expires_ns, max_downloads,
                       downloads, label, note, created_ns)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?)`

	sqlLinkByID = `SELECT ` + linkColumns + ` FROM share_link WHERE id = ?`

	sqlLinkByHash = `SELECT ` + linkColumns + ` FROM share_link WHERE token_hash = ?`

	sqlListLinksByOwner = `SELECT ` + linkColumns + ` FROM share_link WHERE owner = ? ORDER BY id`

	// The ownership check and the delete are one statement, so the two
	// cannot disagree about who owns the row.
	sqlDeleteLink = `DELETE FROM share_link WHERE id = ? AND owner = ?`

	sqlLinkPasswordHash = `SELECT password_hash FROM share_link WHERE id = ?`

	// The cap check and the increment are one statement: a read of the
	// count followed by a write of the next one lets two downloads past a
	// cap of one.
	sqlConsumeLinkDownload = `
UPDATE share_link
SET downloads = downloads + 1
WHERE id = ? AND (max_downloads IS NULL OR downloads < max_downloads)`

	sqlLinkKeyVersion = `SELECT ver FROM key_version WHERE id = 1`
)

// One statement per patchable field. A statement assembled from the fields a
// patch happens to carry has text that depends on input, which is what these
// being constants prevents.
const (
	sqlUpdateLinkPerms    = `UPDATE share_link SET perms = ? WHERE id = ?`
	sqlUpdateLinkPassword = `UPDATE share_link SET password_hash = ? WHERE id = ?`
	sqlUpdateLinkExpiry   = `UPDATE share_link SET expires_ns = ? WHERE id = ?`
	sqlUpdateLinkMaxDown  = `UPDATE share_link SET max_downloads = ? WHERE id = ?`
	sqlUpdateLinkLabel    = `UPDATE share_link SET label = ? WHERE id = ?`
	sqlUpdateLinkNote     = `UPDATE share_link SET note = ? WHERE id = ?`
)
