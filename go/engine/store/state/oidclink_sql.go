package state

const (
	sqlInsertOIDCFlow = `
INSERT INTO oidc_flow(state_digest, user, nonce, binding_digest, code_verifier,
                      redirect_uri, return_to, created_ns)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`

	sqlSelectOIDCFlow = `
SELECT user, nonce, binding_digest, code_verifier, redirect_uri, return_to, created_ns
  FROM oidc_flow
 WHERE state_digest = ? AND created_ns >= ?`

	sqlDeleteOIDCFlow = `DELETE FROM oidc_flow WHERE state_digest = ?`

	sqlSweepOIDCFlows = `DELETE FROM oidc_flow WHERE created_ns < ?`

	sqlInsertOIDCLink = `
INSERT INTO oidc_link(issuer, subject, user, linked_ns) VALUES (?, ?, ?, ?)`

	sqlSelectOIDCLinkByUser = `
SELECT issuer, subject, linked_ns, last_login_ns FROM oidc_link WHERE user = ?`

	sqlSelectOIDCLinkByIdentity = `
SELECT user FROM oidc_link WHERE issuer = ? AND subject = ?`

	sqlDeleteOIDCLinkByUser = `DELETE FROM oidc_link WHERE user = ?`

	sqlTouchOIDCLink = `
UPDATE oidc_link SET last_login_ns = ? WHERE issuer = ? AND subject = ?`
)
