package cache

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
)

// The derivation. A node id is a function of the file's identity, so a cache
// that was deleted rebuilds to the same ids and every sync client attached to
// this server keeps its journal.
const (
	// derivationPrefix domain-separates this hash from every other one, and
	// carries the version that a deliberate change to the derivation would
	// move. Changing it changes every id.
	derivationPrefix = "stowcloud/fileid/v1"

	keyLen = len(derivationPrefix) + 8 + 8 + 8 + 1 + 8 + 4

	// idBits keeps the id inside a signed 64-bit integer. A sync client
	// consumes it as one, so an id with the top bit set reaches some of them
	// as a negative number.
	idBits = 63

	mask63 = int64(1<<63 - 1)

	// maxAttempts bounds the walk past a collision. Reaching it means the
	// derivation is not distributing, which is a different problem from two
	// files colliding once, and looping forever would hide it.
	maxAttempts = 64
)

// DeriveID is the pure half of AllocateID: no I/O, no uniqueness check. It is
// exported so a test can assert the derivation directly rather than through
// the table.
//
// attempt is zero in every normal case. It exists so that two files whose
// identities hash to the same id can be given different ones.
func DeriveID(ident Ident, attempt uint32) FileID { return derive(ident, attempt, idBits) }

// derive folds the hash into bits bits rather than always into 63.
//
// The width is a parameter for one reason: forcing a collision at 63 bits
// needs a corpus nobody can build in a test, and the collision path is the
// half of this design that a rebuild depends on. Nothing outside this package
// can choose it; New fixes it at idBits.
func derive(ident Ident, attempt uint32, bits uint) FileID {
	var key [keyLen]byte
	n := copy(key[:], derivationPrefix)
	binary.BigEndian.PutUint64(key[n:], uint64(ident.Share))
	binary.BigEndian.PutUint64(key[n+8:], ident.Dev)
	binary.BigEndian.PutUint64(key[n+16:], ident.Ino)
	if ident.Btime != nil {
		// The flag byte is what keeps a file with btime_ns = 0 and a file with
		// no btime at all from deriving the same id.
		key[n+24] = 1
		//nolint:gosec // a birth time before the epoch is a fact about the file
		// rather than an error, so it goes into the key as its bit pattern.
		binary.BigEndian.PutUint64(key[n+25:], uint64(*ident.Btime))
	}
	binary.BigEndian.PutUint32(key[n+33:], attempt)

	sum := sha256.Sum256(key[:])

	// Assembled a byte at a time, so the id never passes through an unsigned
	// conversion. The value is the first eight bytes read big-endian with the
	// top bit cleared, which is what the two's complement of this loop is.
	var v int64
	for _, b := range sum[:8] {
		v = v<<8 | int64(b)
	}
	mask := mask63 >> (idBits - bits)
	v &= mask
	return FileID(1 + v%mask)
}

// AllocateID returns the id for ident, deriving it and consulting the override
// table first. It is the only function that may decide an id.
//
// tx is the cache's write transaction. An override row, where a collision
// forces one, is committed to the durable half before this returns and
// therefore before the node row that uses the id: a crash between the two
// leaves an override with no node, which is the state every rebuild starts
// from anyway. The other order leaves a node holding an id nothing recorded,
// and the next rebuild races the collision again.
//
// A caller must not cache the result across a share re-registration: the share
// id is part of the derivation.
func (d *DB) AllocateID(ctx context.Context, tx *sql.Tx, ident Ident) (FileID, error) {
	// The override is consulted first, always, so that a past decision is
	// never revisited.
	id, ok, err := d.ov.LookupFileID(ctx, ident)
	if err != nil {
		return 0, fmt.Errorf("reading the id override: %w", err)
	}
	if ok {
		return id, nil
	}

	for attempt := uint32(0); attempt <= maxAttempts; attempt++ {
		id := derive(ident, attempt, d.bits)
		holder, held, err := identOf(ctx, tx, id)
		if err != nil {
			return 0, err
		}
		if held && !holder.Equal(ident) {
			continue
		}
		if attempt == 0 {
			return id, nil
		}
		if err := d.ov.RecordFileID(ctx, ident, id); err != nil {
			return 0, fmt.Errorf("recording the id override: %w", err)
		}
		// Worth an operator's attention not because anything is wrong but
		// because it is the first evidence that this corpus has reached the
		// size where a 63-bit collision stops being abstract.
		slog.Warn("two files derived the same node id, and the second was moved",
			slog.Uint64("share", uint64(ident.Share)),
			slog.Uint64("dev", ident.Dev),
			slog.Uint64("ino", ident.Ino),
			slog.Int64("id", int64(id)),
			slog.Uint64("attempt", uint64(attempt)))
		return id, nil
	}
	return 0, fmt.Errorf("no free id for (share %d, dev %d, ino %d) after %d attempts",
		ident.Share, ident.Dev, ident.Ino, maxAttempts)
}

// identOf reports which identity holds id, if any.
func identOf(ctx context.Context, tx *sql.Tx, id FileID) (Ident, bool, error) {
	var (
		share, dev, ino int64
		btime           *int64
	)
	err := tx.QueryRowContext(ctx, sqlNodeIdentByID, int64(id)).Scan(&share, &dev, &ino, &btime)
	if errors.Is(err, sql.ErrNoRows) {
		return Ident{}, false, nil
	}
	if err != nil {
		return Ident{}, false, fmt.Errorf("reading the holder of node id %d: %w", id, err)
	}
	holder, err := identFromSQL(share, dev, ino, btime)
	if err != nil {
		return Ident{}, false, err
	}
	return holder, true, nil
}
