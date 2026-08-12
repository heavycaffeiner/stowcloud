package acl

// Every statement this package runs, as a constant. Nothing is built from
// parts (D14): a SQL string assembled at runtime is an injection waiting for
// an input.
const (
	sqlReadGrants = `
SELECT id, user, "group", share, subpath, allow, deny, inherit, label
FROM "grant"`

	sqlReadMemberships = `SELECT user, "group" FROM membership`
)
