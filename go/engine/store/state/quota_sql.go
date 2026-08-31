package state

const (
	// The reservation and the cap check are one statement on purpose: a read
	// of the current usage followed by a write of the new one is two
	// accounts of the same bytes, and two uploads racing between them both
	// see headroom that only one of them can have.
	sqlReserveQuota = `
UPDATE user
SET usage_bytes = usage_bytes + ?
WHERE id = ?
  AND (quota_bytes IS NULL OR usage_bytes + ? <= quota_bytes)`

	// Clamped at zero in SQL rather than in Go, for the same reason: a
	// credit that read, subtracted and wrote back could go negative between
	// the read and the write.
	sqlReleaseQuota = `UPDATE user SET usage_bytes = max(0, usage_bytes - ?) WHERE id = ?`
)
