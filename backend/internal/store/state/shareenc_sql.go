package state

const (
	shareEncColumns = `share, scheme, salt, verifier, created_ns`

	sqlListShareEncryption = `SELECT ` + shareEncColumns +
		` FROM share_encryption ORDER BY share`

	sqlReadShareEncryption = `SELECT ` + shareEncColumns +
		` FROM share_encryption WHERE share = ?`

	sqlWriteShareEncryption = `
INSERT INTO share_encryption(share, scheme, salt, verifier, created_ns)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(share) DO UPDATE SET
	scheme = excluded.scheme,
	salt = excluded.salt,
	verifier = excluded.verifier,
	created_ns = excluded.created_ns`

	sqlDeleteShareEncryption = `DELETE FROM share_encryption WHERE share = ?`
)
