package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// A share link keeps a path and, when it was made against a file rather than a
// share root, the identity of that file. It does not follow a rename: access
// stats the stored path and requires the stored identity to match, so a
// rename or a replacement at that path makes the link gone.
//
// That contract is why there are exactly two representations and why the third
// one has to be refused rather than guessed at.

// ErrLinkTargetMalformed is a partial identity tuple: some columns set and
// some not, or a birth time recorded as absent. There are exactly two shapes a
// link may hold, path-only and a complete identity, and anything else is
// corruption in the durable half.
//
// It is refused rather than repaired. Reading a partial tuple as path-only
// would hand public access to whatever is created at that path next, which is
// the one outcome worse than a link that stops working.
var ErrLinkTargetMalformed = errors.New("a share link carries a partial identity")

// theOperatorFix is what a refusal carries. Nothing can reconstruct the
// missing half of the tuple, so the row has to go: the link stops working,
// which is what a corrupt link should do.
const theOperatorFix = "restore state.db from a backup, or delete the link row and issue a new link"

// checkShareLinkTargets refuses a link whose target this schema cannot
// represent, naming it. The CHECK constraints in migration 2 refuse the same
// rows, but a constraint failure says which constraint and not which link, and
// an operator holding a durable database needs the link.
func checkShareLinkTargets(ctx context.Context, tx *sql.Tx) (err error) {
	rows, err := tx.QueryContext(ctx, sqlReadShareLinkTargets)
	if err != nil {
		return fmt.Errorf("reading share link targets: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()

	for rows.Next() {
		var (
			id                       int64
			dev, ino, present, btime *int64
			set                      int
			pathOnly, coherent       bool
		)
		if serr := rows.Scan(&id, &dev, &ino, &present, &btime); serr != nil {
			return fmt.Errorf("reading a share link target: %w", serr)
		}
		for _, col := range []*int64{dev, ino, present, btime} {
			if col != nil {
				set++
			}
		}
		pathOnly = set == 0
		coherent = set == 4 && *present == 1 && (*dev != 0 || *ino != 0)

		if !pathOnly && !coherent {
			return fmt.Errorf("%w: share link %d. %s", ErrLinkTargetMalformed, id, theOperatorFix)
		}
	}
	return rows.Err()
}
