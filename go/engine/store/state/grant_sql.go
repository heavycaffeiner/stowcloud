package state

// Every statement the grant aggregate runs, written out whole. The filters a
// listing takes are applied in Go rather than appended to a WHERE clause,
// because a statement assembled from optional parts is what these being
// constants exists to prevent.
const (
	sqlReadGrants = `
SELECT id, user, "group", share, subpath, allow, deny, inherit, label, created_ns
FROM "grant"`

	sqlReadMemberships = `SELECT user, "group" FROM membership`

	sqlInsertGrant = `
INSERT INTO "grant"(user, "group", share, subpath, allow, deny, inherit, label, created_ns)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`

	// Who a grant is for and which share it covers are not updatable: they
	// identify it rather than describe it.
	sqlUpdateGrant = `UPDATE "grant" SET allow = ?, deny = ?, inherit = ?, label = ? WHERE id = ?`

	sqlDeleteGrant = `DELETE FROM "grant" WHERE id = ?`

	// The cascade DeleteShare runs, in the same transaction as the share row
	// it belongs to.
	sqlDeleteGrantsForShare = `DELETE FROM "grant" WHERE share = ?`
)
