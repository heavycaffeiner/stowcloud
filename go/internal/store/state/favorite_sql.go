package state

// D14: every statement is a constant and every value is a parameter.

const sqlSelectFavorites = `
SELECT share, dev, ino, btime_present, btime_ns, path
  FROM favorite
 WHERE user = ?
 ORDER BY share, path`

const sqlUpsertFavorite = `
INSERT INTO favorite(user, share, dev, ino, btime_present, btime_ns, path)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(user, share, dev, ino, btime_present, btime_ns)
DO UPDATE SET path = excluded.path`

const sqlDeleteFavorite = `
DELETE FROM favorite
 WHERE user = ? AND share = ? AND dev = ? AND ino = ?
   AND btime_present = ? AND btime_ns = ?`
