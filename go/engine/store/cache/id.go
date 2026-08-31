package cache

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"

	"github.com/heavycaffeiner/stowcloud/go/engine/store/ident"
)

// The derivation. Node ids are computed from a file's identity, so a deleted
// cache rebuilds to identical ids and every sync client attached to this server
// retains its journal.
const (
	// derivationPrefix domain-separates this hash from every other one and
	// carries the version a deliberate change to the derivation would move.
	// Changing it changes every id in the deployment.
	derivationPrefix = "stowcloud/fileid/v1"

	keyLen = len(derivationPrefix) + 8 + 8 + 8 + 1 + 8 + 4

	// idBits confines the id to a signed 64-bit integer. Sync clients consume it
	// as one, so an id with its top bit set would arrive at some of them as a
	// negative number.
	idBits = 63

	mask63 = int64(1<<63 - 1)

	// maxAttempts limits the search following a collision. Hitting it indicates
	// the derivation is not distributing values, a distinct and more serious
	// problem than a single collision between two files, which an unbounded
	// retry loop would conceal.
	maxAttempts = 64
)

// DeriveID is the pure half of AllocateID: no I/O, no uniqueness check. It
// is exported so a caller, and a test, can assert the derivation directly
// rather than through the table.
//
// attempt is zero in every ordinary case. It exists so that two files whose
// identities derive the same id can be given different ones; nothing outside
// this package chooses a value.
func DeriveID(id ident.Ident, attempt uint32) ident.FileID { return derive(id, attempt, idBits) }

// derive reduces the hash to the given number of bits rather than always 63.
//
// Width is parameterized for a single reason: provoking a collision at 63 bits
// would require a corpus no test can assemble, and the collision path is the
// half of this design that rebuilds depend on. No caller outside this package
// selects it; New pins it to idBits.
func derive(id ident.Ident, attempt uint32, bits uint) ident.FileID {
	var key [keyLen]byte
	n := copy(key[:], derivationPrefix)
	binary.BigEndian.PutUint64(key[n:], uint64(id.Share))
	binary.BigEndian.PutUint64(key[n+8:], id.Dev)
	binary.BigEndian.PutUint64(key[n+16:], id.Ino)
	if id.Btime != nil {
		// The flag byte is what keeps a file with btime_ns = 0 and a file
		// with no btime at all from deriving the same id.
		key[n+24] = 1
		// A birth time before the epoch is a fact about the file rather
		// than an error, so it goes into the key as its bit pattern, taken
		// a byte at a time so the value never crosses a width it could be
		// refused by.
		for i := range 8 {
			key[n+25+i] = byte(*id.Btime >> (56 - 8*i) & 0xff)
		}
	}
	binary.BigEndian.PutUint32(key[n+33:], attempt)

	sum := sha256.Sum256(key[:])

	// Assembled a byte at a time, so the id never passes through an
	// unsigned conversion. The value is the first eight bytes read big
	// endian with the top bit cleared, which is what this loop plus the
	// mask below produce.
	var v int64
	for _, b := range sum[:8] {
		v = v<<8 | int64(b)
	}
	mask := mask63 >> (idBits - bits)
	v &= mask
	return ident.FileID(1 + v%mask)
}

// AllocateID produces the id for an identity, checking the override table before
// deriving anything. No other function may decide an id.
//
// A candidate counts as free only when neither another node nor another override
// identity holds it. That second condition is what lets a reservation outlast
// the cache: once a file is deleted its owner may not yet have been walked, and
// an id currently held by no node remains claimed.
//
// tx is the cache's write transaction. Override rows created by a collision
// commit to the durable half before this returns, hence before the node row
// using the id. A crash between the two leaves reservations without nodes, which
// is where every rebuild begins anyway. The opposite order would leave a node
// holding an unrecorded id, and the next rebuild would re-run the same collision
// with no memory of how it resolved before.
//
// Callers must not retain the result across a share re-registration, since the
// share id participates in the derivation.
func (d *DB) AllocateID(ctx context.Context, tx *sql.Tx, id ident.Ident) (ident.FileID, error) {
	// The override is consulted first, always, so a past decision is never
	// revisited.
	recorded, ok, err := d.ov.LookupFileID(ctx, id)
	if err != nil {
		return 0, fmt.Errorf("reading the id override: %w", err)
	}
	if ok {
		return recorded, nil
	}

	// The identity holding the base candidate, where one does. Its id
	// became an insertion-order decision the moment a collision existed, so
	// it is written down beside the newcomer's rather than left to be
	// derived again.
	var base []ident.Assignment

	for attempt := uint32(0); attempt <= maxAttempts; attempt++ {
		candidate := derive(id, attempt, d.bits)
		free, holder, err := d.available(ctx, tx, candidate, id)
		if err != nil {
			return 0, err
		}
		if !free {
			if attempt == 0 && holder != nil {
				base = []ident.Assignment{{Ident: *holder, ID: candidate}}
			}
			continue
		}
		if attempt == 0 {
			return candidate, nil
		}
		if err := d.ov.RecordFileIDs(ctx,
			append(base, ident.Assignment{Ident: id, ID: candidate})...); err != nil {
			return 0, fmt.Errorf("recording the id override: %w", err)
		}
		// Deserves operator attention not because something has broken but
		// because it is the first sign this corpus has grown to the size where
		// a 63-bit collision ceases to be theoretical.
		slog.Warn("two files derived the same node id, and the second was moved",
			slog.Uint64("share", uint64(id.Share)),
			slog.Uint64("dev", id.Dev),
			slog.Uint64("ino", id.Ino),
			slog.Int64("id", int64(candidate)),
			slog.Uint64("attempt", uint64(attempt)))
		return candidate, nil
	}
	return 0, fmt.Errorf("no free id for (share %d, dev %d, ino %d) after %d attempts",
		id.Share, id.Dev, id.Ino, maxAttempts)
}

// available reports whether want is free for id, naming the claimant when it is
// not.
//
// The returned holder is a node possessing the id with nothing durable recorded
// about it, the single case the caller must persist. Reservations are already
// durable, so they return as occupied with no holder.
func (d *DB) available(
	ctx context.Context, tx *sql.Tx, want ident.FileID, id ident.Ident,
) (bool, *ident.Ident, error) {
	owner, reserved, err := d.ov.LookupFileIDOwner(ctx, want)
	if err != nil {
		return false, nil, fmt.Errorf("reading the owner of id %d: %w", want, err)
	}
	if reserved {
		return owner.Equal(id), nil, nil
	}

	holder, held, err := d.identOf(ctx, tx, want)
	if err != nil {
		return false, nil, err
	}
	if !held || holder.Equal(id) {
		return true, nil, nil
	}
	return false, &holder, nil
}

// identOf reports which identity holds an id, if any.
func (d *DB) identOf(ctx context.Context, tx *sql.Tx, id ident.FileID) (ident.Ident, bool, error) {
	var (
		share, dev, ino int64
		btime           *int64
	)
	err := tx.StmtContext(ctx, d.st.nodeIdentByID).QueryRowContext(ctx, int64(id)).
		Scan(&share, &dev, &ino, &btime)
	if errors.Is(err, sql.ErrNoRows) {
		return ident.Ident{}, false, nil
	}
	if err != nil {
		return ident.Ident{}, false, fmt.Errorf("reading the holder of node id %d: %w", id, err)
	}
	holder, err := identFromRow(share, dev, ino, btime)
	if err != nil {
		return ident.Ident{}, false, err
	}
	return holder, true, nil
}

// identFromRow adapts a scanned row, where an absent birth time arrives as a
// nil pointer, to the flat form the neutral package takes.
func identFromRow(share, dev, ino int64, btime *int64) (ident.Ident, error) {
	if btime == nil {
		return ident.FromSQL(share, dev, ino, 0, 0)
	}
	return ident.FromSQL(share, dev, ino, 1, *btime)
}
