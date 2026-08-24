package core

import (
	"encoding/hex"

	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
	"lukechampine.com/blake3"
)

// FileETag derives a change token from the identity, size, mtime and ctime
// that statx actually exposes.
//
// Linux statx has no inode change-version field, so the token is always weak:
// it is useful for caching and conflict warnings but is not a strong
// validator. Reporting a metadata-derived token as strong would preserve
// F11's false guarantee, so the weak flag is the point of the return.
//
// What is deliberately not done is hashing the content to make it strong.
// That reads every byte of every file on every listing, and the product's
// premise is a 12 TB tree.
func FileETag(st vfs.Stat) (token string, weak bool) {
	// ctime is included because a rename or a move changes it where mtime
	// does not, which is how a file that was replaced by a move is told from
	// one that was not. mtime alone misses exactly the case F11 cares about:
	// another program rewriting a file in place.
	buf := make([]byte, 40)
	le64(buf[0:], st.Dev)
	le64(buf[8:], st.Ino)
	le64(buf[16:], st.Size)
	le64(buf[24:], uint64(st.MtimeNs)) //nolint:gosec // a mtime enters the hash as its bit pattern, not as a numeric input.
	var ctime uint64
	if st.CtimeNs != nil {
		ctime = uint64(*st.CtimeNs) //nolint:gosec // as above.
	}
	le64(buf[32:], ctime)
	sum := blake3.Sum256(buf)
	return hex.EncodeToString(sum[:16]), true
}

func le64(b []byte, v uint64) {
	for i := 0; i < 8; i++ {
		// The shift is masked to a byte before the conversion, so the G115
		// overflow reading cannot fire: the value is range-checked by the
		// mask, not truncated by the conversion.
		b[i] = byte((v >> (8 * i)) & 0xff)
	}
}
