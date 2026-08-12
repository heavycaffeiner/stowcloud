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

var (
	// ErrLinkTargetAmbiguous is the all-zero identity the Phase 2 importer
	// wrote. It used that one value both for a Rust link that legitimately had
	// no file id and for one whose id it could not resolve, so nothing can now
	// tell "path only" from "the target could not be found". Converting it to
	// path-only would hand public access to whatever is created at that path
	// next.
	ErrLinkTargetAmbiguous = errors.New("a share link carries the ambiguous all-zero identity")

	// ErrLinkTargetMalformed is any other partial tuple: some identity columns
	// set and some not, or a birth time recorded as absent. It is corruption in
	// the durable half, and the answer is the same as above.
	ErrLinkTargetMalformed = errors.New("a share link carries a partial identity")
)

// theImporterFix is the operator instruction both refusals carry. The generated
// state.db is the thing to move aside, because the Rust sources it was built
// from were never written to and are still the authority.
const theImporterFix = "move the generated state.db aside and rerun " +
	"'stowcloud migrate --from-rust' with this binary, against the untouched Rust databases"

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
			id                            int64
			dev, ino, present, btime      *int64
			set                           int
			pathOnly, coherent, fabricate bool
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
		fabricate = set == 4 && *dev == 0 && *ino == 0 && *present == 0 && *btime == 0

		switch {
		case pathOnly || coherent:
		case fabricate:
			return fmt.Errorf("%w: share link %d. %s", ErrLinkTargetAmbiguous, id, theImporterFix)
		default:
			return fmt.Errorf("%w: share link %d. %s", ErrLinkTargetMalformed, id, theImporterFix)
		}
	}
	return rows.Err()
}
