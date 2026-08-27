package core

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
)

// UserID is an account, as the auth layer addresses one. Opaque to the core:
// a grant names a user id, never a username.
type UserID int64

// ShareID is the VFS share id, the only id scheme the core recognises a
// share by.
//
// An alias rather than a distinct type: the VFS mints and consumes these
// ids, so a distinct type would force a conversion at every crossing while
// proving nothing, since both sides already share one id space.
type ShareID = vfs.ShareID

// Token is a caller-supplied validator: the ETag the client last saw, sent
// to prove nothing changed in between.
type Token string

// instanceIDBytes is the entropy behind a deployment's identity. Sixteen
// bytes, hex-encoded, is what clients store and compare.
const instanceIDBytes = 16

// NewInstanceID mints the identity a deployment presents to clients.
//
// It lives in the core because it is a property of the deployment rather
// than of any protocol. Minted once and never regenerated: a client that saw
// one identity and then another treats the server as a different server and
// re-syncs everything it holds.
//
// A failure of the random source is returned rather than papered over with a
// weaker source, because an identity two deployments could both mint is not
// an identity.
func NewInstanceID() (string, error) {
	buf := make([]byte, instanceIDBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("minting an instance id: %w", err)
	}
	return hex.EncodeToString(buf), nil
}
