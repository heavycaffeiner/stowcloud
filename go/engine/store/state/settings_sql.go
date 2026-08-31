package state

// The administrator's overrides held as a single document. settings.go explains
// why one document beats a column per setting.
const (
	sqlReadSettings = `SELECT json FROM settings WHERE id = 1`

	sqlWriteSettings = `
INSERT INTO settings(id, json) VALUES (1, ?)
ON CONFLICT(id) DO UPDATE SET json = excluded.json`
)
