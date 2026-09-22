package state

const (
	sqlInsertRecoveryCode = `
INSERT INTO recovery_code(user, code_hash, used_ns) VALUES (?, ?, NULL)`

	sqlDeleteRecoveryCodes = `DELETE FROM recovery_code WHERE user = ?`

	// Deleted rather than marked used: the row's only purpose is to be
	// matched once, and a used row is a hash kept for nothing.
	sqlConsumeRecoveryCode = `
DELETE FROM recovery_code WHERE user = ? AND code_hash = ? AND used_ns IS NULL`

	sqlCountRecoveryCodes = `
SELECT COUNT(*) FROM recovery_code WHERE user = ? AND used_ns IS NULL`
)
