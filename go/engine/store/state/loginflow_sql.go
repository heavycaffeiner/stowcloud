package state

const (
	sqlInsertLoginFlow = `
INSERT INTO compat_login_flow(poll_digest, login_digest, created_ns)
VALUES (?, ?, ?)`

	sqlSelectLoginFlowByPoll = `
SELECT poll_digest, login_digest, created_ns, approved_user, approved_login, last_poll_ns
  FROM compat_login_flow
 WHERE poll_digest = ?`

	sqlSelectLoginFlowByLogin = `
SELECT poll_digest, login_digest, created_ns, approved_user, approved_login, last_poll_ns
  FROM compat_login_flow
 WHERE login_digest = ?`

	// The null test is what turns a second approval into a no-op instead of an
	// overwrite: the row changes only while no approval exists.
	sqlApproveLoginFlow = `
UPDATE compat_login_flow
   SET approved_user = ?, approved_login = ?
 WHERE login_digest = ? AND approved_user IS NULL`

	sqlSelectLoginFlowApproval = `
SELECT approved_user FROM compat_login_flow WHERE login_digest = ?`

	// Same shape as the approval: the interval is compared inside the
	// statement, so two polls racing cannot both find themselves early
	// enough.
	sqlTouchLoginFlowPoll = `
UPDATE compat_login_flow
   SET last_poll_ns = ?
 WHERE poll_digest = ? AND last_poll_ns <= ?`

	sqlSelectLoginFlowLastPoll = `
SELECT last_poll_ns FROM compat_login_flow WHERE poll_digest = ?`

	sqlDeleteLoginFlow = `DELETE FROM compat_login_flow WHERE poll_digest = ?`

	sqlSweepLoginFlows = `DELETE FROM compat_login_flow WHERE created_ns < ?`

	// The same shape as the approval, one step further along: the row moves
	// out of unclaimed only while it is still unclaimed, so exactly one poll
	// may go on to mint a credential.
	sqlClaimLoginFlowDelivery = `
UPDATE compat_login_flow
   SET claimed_ns = ?
 WHERE poll_digest = ? AND approved_user IS NOT NULL AND claimed_ns = 0`

	sqlStoreLoginFlowDelivery = `
UPDATE compat_login_flow
   SET sealed_result = ?, sealed_key_ver = ?, credential_id = ?
 WHERE poll_digest = ?`

	sqlSelectLoginFlowDelivery = `
SELECT claimed_ns, sealed_result, sealed_key_ver, credential_id, delivered_ns
  FROM compat_login_flow
 WHERE poll_digest = ?`

	sqlMarkLoginFlowDelivered = `
UPDATE compat_login_flow SET delivered_ns = ? WHERE poll_digest = ?`

	// Only the temporary ciphertext, never the row: the flow itself expires on
	// its own clock, and dropping it here would lose the record that a
	// credential was minted.
	sqlClearLoginFlowMaterial = `
UPDATE compat_login_flow
   SET sealed_result = NULL, sealed_key_ver = 0
 WHERE created_ns < ?`
)
