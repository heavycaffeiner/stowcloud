package state

// D14: every statement is a constant and every value is a parameter.

const sqlInsertLoginFlow = `
INSERT INTO compat_login_flow(poll_digest, login_digest, created_ns)
VALUES (?, ?, ?)`

const sqlSelectLoginFlowByPoll = `
SELECT poll_digest, login_digest, created_ns, approved_user, approved_login, last_poll_ns
  FROM compat_login_flow
 WHERE poll_digest = ?`

const sqlSelectLoginFlowByLogin = `
SELECT poll_digest, login_digest, created_ns, approved_user, approved_login, last_poll_ns
  FROM compat_login_flow
 WHERE login_digest = ?`

// The null check is what makes a second approval a no-op rather than an
// overwrite: the row moves only while nobody has approved it.
const sqlApproveLoginFlow = `
UPDATE compat_login_flow
   SET approved_user = ?, approved_login = ?
 WHERE login_digest = ? AND approved_user IS NULL`

const sqlSelectLoginFlowApproval = `
SELECT approved_user FROM compat_login_flow WHERE login_digest = ?`

// Same shape as the approval above: the interval is compared inside the
// statement, so two polls racing cannot both find themselves early enough.
const sqlTouchLoginFlowPoll = `
UPDATE compat_login_flow
   SET last_poll_ns = ?
 WHERE poll_digest = ? AND last_poll_ns <= ?`

const sqlSelectLoginFlowLastPoll = `
SELECT last_poll_ns FROM compat_login_flow WHERE poll_digest = ?`

const sqlDeleteLoginFlow = `
DELETE FROM compat_login_flow WHERE poll_digest = ?`

const sqlSweepLoginFlows = `
DELETE FROM compat_login_flow WHERE created_ns < ?`
