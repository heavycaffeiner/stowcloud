package state

const (
	sqlInsertAppPassword = `
INSERT INTO app_password(token_hash, user, name, scope_perms, scope_shares,
                        created_ns, expires_ns)
VALUES (?, ?, ?, ?, ?, ?, ?)`

	sqlSelectAppPasswordByHash = `
SELECT id, user, name, scope_perms, scope_shares, created_ns, expires_ns,
       last_used_ns, wipe_requested
  FROM app_password
 WHERE token_hash = ?`

	sqlListAppPasswords = `
SELECT id, name, scope_perms, scope_shares, created_ns, expires_ns,
       last_used_ns, wipe_requested
  FROM app_password
 WHERE user = ?
 ORDER BY created_ns DESC`

	sqlDeleteAppPassword = `DELETE FROM app_password WHERE id = ? AND user = ?`

	// The wipe marks the credential and revokes it in one statement.
	sqlRequestAppPasswordWipe = `
UPDATE app_password SET wipe_requested = 1, expires_ns = 0
 WHERE user = ? AND id = ?`

	sqlTouchAppPassword = `
UPDATE app_password SET last_used_ns = ?, last_ip = ?, last_ua = ? WHERE id = ?`
)
