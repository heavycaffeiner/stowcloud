package fsatomic

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// stagingPrefix marks a name this package writes while a replace is still
// in flight. Shared by convention with other packages that stage a file
// beside its destination, so a directory listing or an orphan sweep
// elsewhere in the tree can recognize it as reserved; this package mints
// its own suffix rather than sharing a generator, since it has no path
// type to route the name through.
const stagingPrefix = ".scpart-"

// stagingName mints a name unlikely enough that two concurrent replaces of
// the same destination never pick the same one; the staging create's
// O_EXCL then turns the rare collision into a refusal, never a clobber.
func stagingName() (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("minting a staging name: %w", err)
	}
	return stagingPrefix + hex.EncodeToString(b[:]), nil
}
