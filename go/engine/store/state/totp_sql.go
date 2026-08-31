package state

const (
	sqlUpsertTOTP = `
INSERT INTO totp_secret(user, secret_ct, key_ver, created_ns) VALUES (?, ?, ?, ?)
ON CONFLICT(user) DO UPDATE SET secret_ct = excluded.secret_ct, key_ver = excluded.key_ver`

	sqlSelectTOTP = `SELECT secret_ct, key_ver FROM totp_secret WHERE user = ?`

	sqlDeleteTOTP = `DELETE FROM totp_secret WHERE user = ?`

	// The conflict clause is what makes the claim atomic: a second insert of
	// the same step changes nothing and reports zero rows.
	sqlInsertTOTPUsed = `
INSERT INTO totp_used(user, step, used_ns) VALUES (?, ?, ?)
ON CONFLICT(user, step) DO NOTHING`

	sqlDeleteTOTPUsedBefore = `DELETE FROM totp_used WHERE user = ? AND step < ?`

	sqlDeleteTOTPUsedAll = `DELETE FROM totp_used WHERE user = ?`
)
