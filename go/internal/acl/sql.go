package acl

// Every statement this package runs, as a constant. Nothing is built from
// parts (D14): a SQL string assembled at runtime is an injection waiting for
// an input.
const (
	sqlReadGrants = `
SELECT id, user, "group", share, subpath, allow, deny, inherit, label, created_ns
FROM "grant"`

	sqlReadMemberships = `SELECT user, "group" FROM membership`

	sqlInsertGrant = `
INSERT INTO "grant"(user, "group", share, subpath, allow, deny, inherit, label, created_ns)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`

	// Who a grant is for and which share it covers are not updatable: they
	// identify it. See UpdateGrant.
	sqlUpdateGrant = `UPDATE "grant" SET allow = ?, deny = ?, inherit = ?, label = ? WHERE id = ?`

	sqlDeleteGrant = `DELETE FROM "grant" WHERE id = ?`
)
