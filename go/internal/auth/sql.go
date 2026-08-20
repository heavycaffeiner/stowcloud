package auth

// Every statement this package runs, as a constant prepared from nothing.
// Nothing here is assembled from parts: a query built from a string is an
// injection waiting for an input (D14), and the only value that ever varies is
// bound.
const (
	// The display name is optional and an imported account can carry none, so
	// an absent one reads as the empty string rather than failing the scan.
	// It failed the scan: a migrated account could not log in at all, and the
	// refusal was a server error rather than anything a person could act on.
	sqlReadUserByName = `
SELECT id, name, COALESCE(display, ''), pw_hash, disabled, smb_enabled, role
FROM user WHERE name = ?`

	sqlReadUserByID = `
SELECT id, name, COALESCE(display, ''), pw_hash, disabled, smb_enabled, role
FROM user WHERE id = ?`

	sqlInsertUser = `
INSERT INTO user(name, display, pw_hash, disabled, role, smb_enabled, created_ns)
VALUES (?, ?, ?, 0, ?, 1, ?)`

	sqlUpdatePassword = `UPDATE user SET pw_hash = ? WHERE id = ?` //nolint:gosec // G101 reads the identifier: this is a statement, not a credential.

	sqlUpdateDisabled = `UPDATE user SET disabled = ? WHERE id = ?`

	sqlUpdateSMBEnabled = `UPDATE user SET smb_enabled = ? WHERE id = ?`

	sqlInsertSession = `
INSERT INTO session(id_hash, user, created_ns, last_seen_ns, absolute_expiry_ns, ip_first, ua_first, amr)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`

	sqlReadSession = `
SELECT id_hash, user, created_ns, last_seen_ns, absolute_expiry_ns, ip_first, ua_first, amr
FROM session WHERE id_hash = ?`

	sqlTouchSession = `UPDATE session SET last_seen_ns = ? WHERE id_hash = ?`

	sqlDeleteSession = `DELETE FROM session WHERE id_hash = ?`

	sqlDeleteUserSessions = `DELETE FROM session WHERE user = ?`

	sqlInsertAppPassword = `INSERT INTO app_password(token_hash, user, name, scope_perms, scope_shares, created_ns, expires_ns) VALUES (?, ?, ?, ?, ?, ?, ?)` //nolint:gosec // G101 reads the identifier: this is a statement, not a credential.

	sqlReadAppPassword = `SELECT id, user, name, scope_perms, scope_shares, expires_ns, wipe_requested FROM app_password WHERE token_hash = ?` //nolint:gosec // G101 reads the identifier: this is a statement, not a credential.

	sqlDeleteAppPassword = `DELETE FROM app_password WHERE id = ? AND user = ?` //nolint:gosec // G101 reads the identifier: this is a statement, not a credential.

	sqlReadTOTP = `SELECT secret_ct, key_ver FROM totp_secret WHERE user = ?`

	sqlCountTOTP = `SELECT count(*) FROM totp_secret WHERE user = ?`

	sqlUpsertTOTP = `
INSERT INTO totp_secret(user, secret_ct, key_ver, created_ns) VALUES (?, ?, ?, ?)
ON CONFLICT(user) DO UPDATE SET secret_ct = excluded.secret_ct, key_ver = excluded.key_ver`

	sqlDeleteTOTP = `DELETE FROM totp_secret WHERE user = ?`

	sqlDeleteSMBSecret = `DELETE FROM user_smb_secret WHERE user = ?`

	sqlInsertTotpUsed = `
INSERT INTO totp_used(user, step, used_ns) VALUES (?, ?, ?)
ON CONFLICT(user, step) DO NOTHING`

	sqlReadTotpUsed = `SELECT step FROM totp_used WHERE user = ?`

	sqlDeleteTotpUsed = `DELETE FROM totp_used WHERE user = ? AND step < ?`

	sqlDeleteTotpReplay = `DELETE FROM totp_used WHERE user = ?`

	sqlInsertRecoveryCode = `
INSERT INTO recovery_code(user, code_hash, used_ns) VALUES (?, ?, NULL)`

	sqlDeleteAllRecovery = `DELETE FROM recovery_code WHERE user = ?`

	sqlCountUnusedRecoveryCodes = `SELECT COUNT(*) FROM recovery_code WHERE user = ? AND used_ns IS NULL`

	// The wipe marks the credential and revokes it in one statement: a device
	// that never reconnects to hear the request must not keep working.
	sqlRequestWipe = `UPDATE app_password SET wipe_requested = 1, expires_ns = 0 WHERE user = ? AND id = ?`

	sqlClearSMBOptOut = `UPDATE user SET smb_opt_out = 0, smb_enabled = 1 WHERE id = ?`

	sqlConsumeRecoveryCode = `
UPDATE recovery_code SET used_ns = ?
WHERE user = ? AND code_hash = ? AND used_ns IS NULL`

	sqlUpsertSMBSecret = `
INSERT INTO user_smb_secret(user, nt_hash_ct, key_ver) VALUES (?, ?, ?)
ON CONFLICT(user) DO UPDATE SET nt_hash_ct = excluded.nt_hash_ct, key_ver = excluded.key_ver`

	sqlReadKeyVersion = `SELECT ver FROM key_version WHERE id = 1`

	sqlWriteKeyVersion = `
INSERT INTO key_version(id, ver) VALUES (1, ?)
ON CONFLICT(id) DO UPDATE SET ver = excluded.ver`

	// The re-seal statements walk every encrypted row, so a rotation or the
	// startup AAD migration can rewrite each under a new key in one
	// transaction.
	sqlForEachTOTP = `SELECT user, secret_ct, key_ver FROM totp_secret`

	sqlForEachSMB = `SELECT user, nt_hash_ct, key_ver FROM user_smb_secret`

	sqlForEachLink = `
SELECT id, token_hash, token_enc, token_key_ver
FROM share_link WHERE token_enc IS NOT NULL`

	sqlSealLink = `UPDATE share_link SET token_enc = ?, token_key_ver = ? WHERE id = ?`

	sqlClearLink = `UPDATE share_link SET token_enc = NULL, token_key_ver = NULL WHERE id = ?`

	sqlReadPassdb = `SELECT u.id, u.name, u.smb_enabled, u.disabled, EXISTS(SELECT 1 FROM totp_secret t WHERE t.user = u.id) AS has2fa, (SELECT s.nt_hash_ct FROM user_smb_secret s WHERE s.user = u.id) AS nt, (SELECT s.key_ver FROM user_smb_secret s WHERE s.user = u.id) AS nt_ver FROM user u WHERE u.smb_opt_out = 0 ORDER BY u.name` //nolint:gosec // G101 reads the identifier: this is a statement, not a credential.

	sqlInsertGroup = `INSERT INTO "group"(id, name) VALUES (?, ?)`

	// The administrator's account listing. The display name is optional and an
	// imported account carries none, so an absent one reads as empty rather
	// than failing the scan.
	sqlListUsers = `
SELECT id, name, COALESCE(display, ''), disabled, role, smb_enabled, created_ns,
       quota_bytes, usage_bytes,
       EXISTS(SELECT 1 FROM totp_secret t WHERE t.user = user.id)
FROM user ORDER BY id`

	sqlDeleteUser = `DELETE FROM user WHERE id = ?`

	sqlSetQuota = `UPDATE user SET quota_bytes = ? WHERE id = ?`

	sqlListGroups = `SELECT id, name FROM "group" ORDER BY id`

	sqlListMemberships = `SELECT user, "group" FROM membership`

	sqlDeleteGroup = `DELETE FROM "group" WHERE id = ?`

	// Adding twice is not an error: the caller asked for a state and the state
	// is reached either way.
	sqlAddMembership = `INSERT OR IGNORE INTO membership(user, "group") VALUES (?, ?)`

	sqlRemoveMembership = `DELETE FROM membership WHERE user = ? AND "group" = ?`

	sqlDeleteMemberships = `DELETE FROM membership WHERE user = ?`

	sqlInsertMembership = `INSERT INTO membership(user, "group") VALUES (?, ?)`

	sqlMembershipsOfUser = `SELECT "group" FROM membership WHERE user = ?`

	sqlInsertAudit = `
INSERT INTO audit(ts_ns, actor, event, target, ip, ua, result, detail)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`

	sqlCountUsers = `SELECT COUNT(*) FROM user` //nolint:gosec // identifier, not a value.

	sqlHasAdmin = `SELECT COUNT(*) FROM user WHERE role = 1`

	sqlListSessions = `
SELECT id_hash, created_ns, last_seen_ns, absolute_expiry_ns, ip_first, ua_first, amr
FROM session WHERE user = ? ORDER BY last_seen_ns DESC`

	sqlDeleteUserSession = `DELETE FROM session WHERE user = ? AND id_hash = ?` //nolint:gosec // identifier, not a value.

	sqlListAppPasswords = `SELECT id, name, scope_perms, scope_shares, created_ns, expires_ns, last_used_ns FROM app_password WHERE user = ? ORDER BY created_ns DESC` //nolint:gosec // G101 reads the identifier: a statement, not a credential.
)

// The audit page, newest first. Bounded by the caller; see AuditPage.
const sqlReadAuditPage = `
SELECT rowid, ts_ns, actor, event, target, ip, ua, result
FROM audit ORDER BY rowid DESC LIMIT ?`
