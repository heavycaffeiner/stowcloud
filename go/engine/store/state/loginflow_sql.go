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

	// The null check is what makes a second approval a no-op rather than an
	// overwrite: the row moves only while nobody has approved it.
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
)
