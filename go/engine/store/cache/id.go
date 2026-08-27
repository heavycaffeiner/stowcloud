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

// The derivation. A node id is a function of the file's identity, so a cache
// that was deleted rebuilds to the same ids and every sync client attached
// to this server keeps its journal.
const (
	// derivationPrefix domain-separates this hash from every other one and
	// carries the version a deliberate change to the derivation would move.
	// Changing it changes every id in the deployment.
	derivationPrefix = "stowcloud/fileid/v1"

	keyLen = len(derivationPrefix) + 8 + 8 + 8 + 1 + 8 + 4

	// idBits keeps the id inside a signed 64-bit integer. A sync client
	// consumes it as one, so an id with the top bit set reaches some clients
	// as a negative number.
	idBits = 63

	mask63 = int64(1<<63 - 1)

	// maxAttempts bounds the walk past a collision. Reaching it means the
	// derivation is not distributing, which is a different and worse problem
	// than two files colliding once, and a retry loop would hide it.
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

// derive folds the hash into bits bits rather than always into 63.
//
// The width is a parameter for one reason: forcing a collision at 63 bits
// needs a corpus nobody can build in a test, and the collision path is the
// half of this design a rebuild depends on. Nothing outside this package can
// choose it; New fixes it at idBits.
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

// AllocateID returns the id for an identity, consulting the override table
// before deriving anything. It is the only function that may decide an id.
//
// A candidate is free only when neither a different node nor a different
// override identity holds it. The second half is what makes a reservation
// outlive the cache: after a file is deleted its owner may not have been
// walked yet, and an id no node currently holds is still spoken for.
//
// tx is the cache's write transaction. The override rows a collision forces
// commit to the durable half before this returns, and therefore before the
// node row that uses the id: a crash between the two leaves reservations
// with no nodes, which is the state every rebuild starts from anyway. The
// other order leaves a node holding an id nothing recorded, and the next
// rebuild races the same collision with no memory of the first outcome.
//
// A caller must not cache the result across a share re-registration: the
// share id is part of the derivation.
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
		// Worth an operator's attention not because anything is wrong but
		// because it is the first evidence that this corpus has reached the
		// size where a 63-bit collision stops being abstract.
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

// available reports whether want is free for id, and who took it otherwise.
//
// The returned holder is a node holding the id with nothing durable recorded
// about it, which is the one case the caller has to write down. A
// reservation is already durable, so it comes back as occupied with no
// holder.
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
