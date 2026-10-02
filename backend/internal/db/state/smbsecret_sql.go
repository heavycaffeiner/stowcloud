package state

const (
	sqlUpsertSMBSecret = `
INSERT INTO user_smb_secret(user, nt_hash_ct, key_ver) VALUES (?, ?, ?)
ON CONFLICT(user) DO UPDATE SET nt_hash_ct = excluded.nt_hash_ct, key_ver = excluded.key_ver`

	sqlSelectSMBSecret = `SELECT nt_hash_ct, key_ver FROM user_smb_secret WHERE user = ?`

	sqlDeleteSMBSecret = `DELETE FROM user_smb_secret WHERE user = ?`

	sqlClearSMBOptOut = `UPDATE user SET smb_opt_out = 0, smb_enabled = 1 WHERE id = ?`

	// Name order, because the rendered file is in name order and two renders
	// of unchanged state have to produce identical bytes.
	sqlSelectPassdbRows = `
SELECT u.id, u.name, u.smb_enabled, u.disabled,
       EXISTS(SELECT 1 FROM totp_secret t WHERE t.user = u.id),
       (SELECT s.nt_hash_ct FROM user_smb_secret s WHERE s.user = u.id),
       (SELECT s.key_ver FROM user_smb_secret s WHERE s.user = u.id)
  FROM user u
 WHERE u.smb_opt_out = 0
 ORDER BY u.name`

	sqlSelectSMBRevertible = `
SELECT u.smb_opt_out,
       EXISTS(SELECT 1 FROM totp_secret t WHERE t.user = u.id),
       EXISTS(SELECT 1 FROM oidc_link o WHERE o.user = u.id)
  FROM user u WHERE u.id = ?`

	sqlSelectKeyVersion = `SELECT ver FROM key_version WHERE id = 1`

	sqlUpsertKeyVersion = `
INSERT INTO key_version(id, ver) VALUES (1, ?)
ON CONFLICT(id) DO UPDATE SET ver = excluded.ver`
)
