package state

// The administrator's stored overrides, as one document. See settings.go for
// why it is one document rather than a column per setting.
const (
	sqlReadSettings = `SELECT json FROM settings WHERE id = 1`

	sqlWriteSettings = `
INSERT INTO settings(id, json) VALUES (1, ?)
ON CONFLICT(id) DO UPDATE SET json = excluded.json`
)
