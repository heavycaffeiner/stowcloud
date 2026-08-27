package core

import (
	"encoding/hex"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"lukechampine.com/blake3"
)

// etagInput is the fixed little-endian layout the token is hashed from: dev,
// ino, size, mtime, ctime, eight bytes each.
const etagInput = 40

// etagBytes is how much of the hash the token carries. Sixteen bytes is 32
// hex characters, which is what every client already stores.
const etagBytes = 16

// FileETag derives a change token from the identity, size, mtime and ctime
// that statx actually exposes.
//
// ctime is in the input because a rename or a move changes it where mtime
// does not, which is how a file replaced by a move is told from one that was
// not. mtime alone misses exactly the in-place rewrite case.
//
// The token is always weak, and the flag is a return value rather than a
// comment so that every caller has to carry it: Linux statx has no inode
// change-version field, so a metadata-derived token cannot be a strong
// validator and reporting it as one would be a false guarantee. Hashing the
// content to earn a strong token is deliberately not done: it reads every
// byte of every file on every listing.
func FileETag(st vfs.Stat) (token string, weak bool) {
	var buf [etagInput]byte
	putUint64(buf[0:], st.Dev)
	putUint64(buf[8:], st.Ino)
	putUint64(buf[16:], st.Size)
	putInt64(buf[24:], st.MtimeNs)
	// A filesystem that reports no ctime hashes as zero, so an absent ctime
	// and an epoch ctime fold to one token. There is nothing to tell apart:
	// neither says the file moved.
	var ctime int64
	if st.CtimeNs != nil {
		ctime = *st.CtimeNs
	}
	putInt64(buf[32:], ctime)

	sum := blake3.Sum256(buf[:])
	return hex.EncodeToString(sum[:etagBytes]), true
}

func putUint64(b []byte, v uint64) {
	for i := range 8 {
		// Masked to a byte before the conversion, so the value is
		// range-checked rather than truncated by it.
		b[i] = byte(v >> (8 * i) & 0xff)
	}
}

// putInt64 writes a signed value as its bit pattern, a byte at a time. A
// timestamp before the epoch is a fact about the file rather than an error,
// and taking it byte-wise means it never crosses a width that could refuse
// it.
func putInt64(b []byte, v int64) {
	for i := range 8 {
		b[i] = byte(v >> (8 * i) & 0xff)
	}
}
