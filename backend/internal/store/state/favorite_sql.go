package state

const (
	sqlSelectFavorites = `
SELECT share, dev, ino, btime_present, btime_ns, path
  FROM favorite
 WHERE user = ?
 ORDER BY share, path`

	sqlUpsertFavorite = `
INSERT INTO favorite(user, share, dev, ino, btime_present, btime_ns, path)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(user, share, dev, ino, btime_present, btime_ns)
DO UPDATE SET path = excluded.path`

	sqlDeleteFavorite = `
DELETE FROM favorite
 WHERE user = ? AND share = ? AND dev = ? AND ino = ?
   AND btime_present = ? AND btime_ns = ?`
)
