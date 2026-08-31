package state

const (
	sqlReadConfigSecret = `SELECT value, key_ver FROM config_secret WHERE name = ?`

	sqlWriteConfigSecret = `
INSERT INTO config_secret(name, value, key_ver) VALUES (?, ?, ?)
ON CONFLICT(name) DO UPDATE SET value = excluded.value, key_ver = excluded.key_ver`

	sqlDeleteConfigSecret = `DELETE FROM config_secret WHERE name = ?`
)
