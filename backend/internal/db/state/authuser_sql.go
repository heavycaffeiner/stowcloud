package state

// Every statement the account aggregate runs. The TOTP-enrolled flag is a
// correlated EXISTS rather than a join, because it is one bit and the row it
// would join to is at most one.
const (
	accountColumns = `
SELECT u.id, u.name, u.display, u.pw_hash, u.disabled, u.role,
       u.smb_enabled, u.smb_opt_out, u.created_ns, u.quota_bytes, u.usage_bytes,
       EXISTS(SELECT 1 FROM totp_secret t WHERE t.user = u.id)
  FROM user u`

	sqlAccountByName = accountColumns + ` WHERE u.name = ?`

	sqlAccountByID = accountColumns + ` WHERE u.id = ?`

	sqlListAccounts = accountColumns + ` ORDER BY u.id`

	sqlCountAccounts = `SELECT COUNT(*) FROM user`

	sqlCountAdmins = `SELECT COUNT(*) FROM user WHERE role = 1`

	// Disabled administrators do not count: an account nobody can sign in as
	// cannot re-enable anything.
	sqlCountThisActiveAdmin = `
SELECT COUNT(*) FROM user WHERE role = 1 AND disabled = 0 AND id = ?`

	sqlCountOtherActiveAdmins = `
SELECT COUNT(*) FROM user WHERE role = 1 AND disabled = 0 AND id <> ?`

	sqlInsertAccount = `
INSERT INTO user(name, display, pw_hash, disabled, role, smb_enabled, created_ns)
VALUES (?, ?, ?, 0, ?, 1, ?)`

	sqlUpdateAccountPassword = `UPDATE user SET pw_hash = ? WHERE id = ?`

	sqlUpdateAccountDisabled = `UPDATE user SET disabled = ? WHERE id = ?`

	sqlUpdateAccountSMBAccess = `
UPDATE user SET smb_opt_out = ?, smb_enabled = ? WHERE id = ?`

	sqlUpdateAccountSMBEnabled = `UPDATE user SET smb_enabled = ? WHERE id = ?`

	sqlUpdateAccountQuota = `UPDATE user SET quota_bytes = ? WHERE id = ?`

	sqlDeleteAccount = `DELETE FROM user WHERE id = ?`

	sqlListGroups = `SELECT id, name FROM "group" ORDER BY id`

	sqlGroupByName = `SELECT id FROM "group" WHERE name = ?`

	sqlInsertGroup = `INSERT INTO "group"(name) VALUES (?)`

	sqlRenameGroup = `UPDATE "group" SET name = ? WHERE id = ?`

	sqlDeleteGroup = `DELETE FROM "group" WHERE id = ?`

	// Adding twice is not an error: the caller asked for a state, and the
	// state is reached either way.
	sqlAddMembership = `INSERT OR IGNORE INTO membership(user, "group") VALUES (?, ?)`

	sqlRemoveMembership = `DELETE FROM membership WHERE user = ? AND "group" = ?`

	sqlDeleteMembershipsOfUser = `DELETE FROM membership WHERE user = ?`

	sqlMembershipsOfUser = `SELECT "group" FROM membership WHERE user = ?`
)
