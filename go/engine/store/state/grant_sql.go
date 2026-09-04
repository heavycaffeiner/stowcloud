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

	// A grant's subject and its share cannot be updated, since they identify it
	// rather than describe it.
	sqlUpdateGrant = `UPDATE "grant" SET allow = ?, deny = ?, inherit = ?, label = ? WHERE id = ?`

	sqlDeleteGrant = `DELETE FROM "grant" WHERE id = ?`

	// The cascade DeleteShare runs, in the same transaction as the share row
	// it belongs to.
	sqlDeleteGrantsForShare = `DELETE FROM "grant" WHERE share = ?`

	// Every row that shares a subject, share, subpath and reach with another,
	// ordered so the rows of one group arrive together with the earliest first.
	// Read by the fold that step 13 runs before its unique index.
	sqlDuplicateGrantRows = `
SELECT g.id, g.allow, g.deny, g.label,
       coalesce(g.user, -1), coalesce(g."group", -1), g.share, g.subpath, g.inherit
FROM "grant" g
JOIN (
  SELECT coalesce(user, -1) AS ku, coalesce("group", -1) AS kg, share, subpath, inherit
  FROM "grant"
  GROUP BY ku, kg, share, subpath, inherit
  HAVING count(*) > 1
) d
  ON coalesce(g.user, -1) = d.ku AND coalesce(g."group", -1) = d.kg
 AND g.share = d.share AND g.subpath = d.subpath AND g.inherit = d.inherit
ORDER BY d.ku, d.kg, g.share, g.subpath, g.inherit, g.id`
)
