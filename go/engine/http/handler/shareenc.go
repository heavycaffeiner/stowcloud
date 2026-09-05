//go:build linux

// Share encryption, as any caller who can see the share reads it back.
package handler

import (
	"encoding/base64"

	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
)

// ShareEncryptionView is one share's rclone-crypt parameters.
//
// Every field here is public by construction or is what it is precisely so a
// user can type or paste it into `rclone config`: Scheme names the format,
// Salt is rclone's own `password2` value, and Verifier is the rclone-crypt
// encryption of a fixed known plaintext under the key the passphrase and
// Salt derive, which is what lets a client detect a mistyped passphrase
// before it writes a single byte under the wrong key. None of the three is
// the passphrase itself or anything derived from it that this server could
// reverse; the passphrase never reaches this server at all.
//
// Labels is a list rather than one string, and is a projection of the
// caller's own grants rather than a stored column: one account can hold two
// grants on the same share under two different subpaths, each projected
// under its own label, and a client keys its upload decision off the label
// it is about to write under. A single label field would silently drop the
// second projection, and the client would then treat one of the two as
// unencrypted, which is the one failure this design cannot afford: it would
// upload plaintext into an encrypted share. Two different callers therefore
// see two different Labels for the very same share, which is why this is
// never cached across accounts.
type ShareEncryptionView struct {
	Share     int64    `json:"share"`
	Labels    []string `json:"labels"`
	Scheme    string   `json:"scheme"`
	Salt      string   `json:"salt"`
	Verifier  string   `json:"verifier"`
	CreatedNs int64    `json:"created_ns"`
}

// ShareEncryptionOf projects one share's rclone-crypt parameters into the
// wire shape.
//
// Verifier is base64 rather than the raw 67 bytes: JSON has no binary type.
// Salt is sent verbatim, not base64 of anything else: it is already the
// exact 22-character string the user types into rclone as password2, and
// re-encoding it would hand back a value that no longer matches what they
// typed.
func ShareEncryptionOf(share core.ShareID, labels []string, e core.Encryption) ShareEncryptionView {
	return ShareEncryptionView{
		Share:     int64(share),
		Labels:    labels,
		Scheme:    e.Scheme,
		Salt:      e.Salt,
		Verifier:  base64.StdEncoding.EncodeToString(e.Verifier),
		CreatedNs: e.Created,
	}
}
