package state

const (
	sqlForEachSMBSecret = `SELECT user, nt_hash_ct, key_ver FROM user_smb_secret`

	sqlForEachTOTPSecret = `SELECT user, secret_ct, key_ver FROM totp_secret`

	// Only the links that carry an owner copy: a link without one is not
	// sealed, and its hash still authenticates the public URL.
	sqlForEachSealedLink = `
SELECT id, token_hash, token_enc, token_key_ver
  FROM share_link WHERE token_enc IS NOT NULL`

	sqlForEachConfigSecret = `SELECT name, value, key_ver FROM config_secret`

	sqlResealTOTPSecret = `
UPDATE totp_secret SET secret_ct = ?, key_ver = ? WHERE user = ?`

	sqlResealLink = `UPDATE share_link SET token_enc = ?, token_key_ver = ? WHERE id = ?`

	sqlFirstSMBSecret = sqlForEachSMBSecret + ` LIMIT 1`

	sqlFirstTOTPSecret = sqlForEachTOTPSecret + ` LIMIT 1`

	sqlFirstSealedLink = sqlForEachSealedLink + ` LIMIT 1`

	sqlFirstConfigSecret = sqlForEachConfigSecret + ` LIMIT 1`
)
