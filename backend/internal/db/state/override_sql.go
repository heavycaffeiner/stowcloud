package state

const (
	sqlReadFileIDOverride = `
SELECT id FROM fileid_override
WHERE share = ? AND dev = ? AND ino = ? AND btime_present = ? AND btime_ns = ?`

	sqlReadFileIDOverrideOwner = `
SELECT share, dev, ino, btime_present, btime_ns FROM fileid_override WHERE id = ?`

	sqlWriteFileIDOverride = `
INSERT INTO fileid_override(share, dev, ino, btime_present, btime_ns, id)
VALUES (?, ?, ?, ?, ?, ?)`

	sqlCountFileIDOverrides = `SELECT count(*) FROM fileid_override`
)
