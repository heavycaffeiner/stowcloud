// Linux only, for the same reason as the rest of this package.
//go:build linux

// The content capability claim.
//
// A short-lived, sealed value that names one file and one thing to do with it.
// It carries no reusable credential: a claim that leaked would let its holder
// fetch that one file for a few minutes, and nothing else, and only while the
// named account still has permission.
package handler

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/chacha20poly1305"
)

// ClaimLifetime is the longest a claim stays openable.
//
// Short because the claim travels in a URL, and a URL is written to browser
// history, proxy logs and whatever the page was shared into. Five minutes is
// enough for a page to load its thumbnails and short enough that the copy in a
// log is useless.
const ClaimLifetime = 5 * time.Minute

// ClaimPurpose is what a claim authorises. It is bound into the sealed value,
// so a thumbnail claim cannot be opened as a download.
type ClaimPurpose string

const (
	// PurposeThumb fetches a server-generated preview.
	PurposeThumb ClaimPurpose = "thumb"
	// PurposeDownload fetches the file's own bytes.
	PurposeDownload ClaimPurpose = "download"
)

// ClaimFormat is this build's claim format. It is bound into the AAD, so a
// value written by another format cannot be opened here even under the right
// key.
//
// Separate from the key version, which travels in the value: rotating a key
// must not look like a format change, and changing the format must not depend
// on anyone having rotated.
const ClaimFormat = 1

// ErrClaim is any claim this server will not open.
//
// One error for every reason: expired, malformed, wrong purpose, unknown
// version and wrong key all answer the same, because telling them apart tells
// a caller which part of a forged claim to fix.
var ErrClaim = errors.New("the capability is not valid")

// Claim is what a claim says once opened.
type Claim struct {
	Purpose ClaimPurpose `json:"p"`
	// UserID is whose permission the fetch is re-resolved under. The claim
	// does not grant access; it names an account, and the fetch checks what
	// that account may do now.
	UserID int64 `json:"u"`
	// Path is the virtual path, as a string. It is parsed by the service that
	// resolves it, not here.
	Path string `json:"f"`
	// Width and Height are the requested dimensions, zero for a download.
	Width  int `json:"w,omitempty"`
	Height int `json:"h,omitempty"`

	IssuedNs  int64 `json:"i"`
	ExpiresNs int64 `json:"e"`
}

// ClaimKey is the sealing key and the version it belongs to.
//
// Versioned because rotation has to keep opening claims minted moments before
// it: a five-minute claim outlives the rotation that replaced its key, and a
// user whose page is loading should not see every thumbnail fail.
type ClaimKey struct {
	Version uint32
	Key     []byte
}

// SealClaim produces the URL-safe value.
func SealClaim(k ClaimKey, c Claim, nowNs int64) (string, error) {
	// The library refuses a wrong-sized key too, so this is not what protects
	// against one. It is here to say the size in this file's own words rather
	// than reporting a cipher's internal message to a caller who supplied a
	// configuration value.
	if len(k.Key) != chacha20poly1305.KeySize {
		return "", fmt.Errorf("%w: the key is %d bytes", ErrClaim, len(k.Key))
	}
	if c.Purpose != PurposeThumb && c.Purpose != PurposeDownload {
		return "", fmt.Errorf("%w: %q is not a purpose", ErrClaim, c.Purpose)
	}

	c.IssuedNs = nowNs
	c.ExpiresNs = nowNs + int64(ClaimLifetime)

	plain, err := json.Marshal(c)
	if err != nil {
		return "", fmt.Errorf("%w: %s", ErrClaim, err)
	}

	aead, err := chacha20poly1305.NewX(k.Key)
	if err != nil {
		return "", fmt.Errorf("%w: %s", ErrClaim, err)
	}
	nonce := make([]byte, aead.NonceSize())
	if _, rerr := rand.Read(nonce); rerr != nil {
		return "", fmt.Errorf("%w: %s", ErrClaim, rerr)
	}

	sealed := aead.Seal(nil, nonce, plain, claimAAD(k.Version, c.Purpose))

	// The version travels outside the sealed value because the opener needs it
	// to pick a key before it can open anything. It is bound into the AAD as
	// well, so changing it in transit fails the seal rather than selecting a
	// different key for the same bytes.
	body := append([]byte(nil), nonce...)
	body = append(body, sealed...)
	return strconv.FormatUint(uint64(k.Version), 10) + "." +
		base64.RawURLEncoding.EncodeToString(body), nil
}

// OpenClaim opens a claim for the purpose the route expects.
//
// keys maps a version to its key, which is what lets a rotation keep opening
// claims minted under the key it replaced.
func OpenClaim(keys map[uint32][]byte, want ClaimPurpose, value string, nowNs int64) (Claim, error) {
	version, body, err := splitClaim(value)
	if err != nil {
		return Claim{}, err
	}
	key, ok := keys[version]
	if !ok || len(key) != chacha20poly1305.KeySize {
		return Claim{}, ErrClaim
	}

	aead, aerr := chacha20poly1305.NewX(key)
	if aerr != nil {
		return Claim{}, ErrClaim
	}
	if len(body) < aead.NonceSize() {
		return Claim{}, ErrClaim
	}
	nonce, sealed := body[:aead.NonceSize()], body[aead.NonceSize():]

	// The expected purpose goes into the AAD, so a thumbnail claim presented
	// to the download route fails to open at all rather than opening and being
	// rejected afterwards.
	plain, oerr := aead.Open(nil, nonce, sealed, claimAAD(version, want))
	if oerr != nil {
		return Claim{}, ErrClaim
	}

	var c Claim
	dec := json.NewDecoder(bytes.NewReader(plain))
	dec.DisallowUnknownFields()
	if derr := dec.Decode(&c); derr != nil {
		return Claim{}, ErrClaim
	}
	if c.Purpose != want {
		// Redundant with the AAD, and measured to be: removing either one
		// alone still refuses a crossed purpose, and only removing both lets
		// one through. Kept because the two protect different things. The AAD
		// stops a claim being opened at all under the wrong purpose; this
		// stops a value whose sealed body disagrees with the AAD it was sealed
		// under from reaching a handler, which would be a bug in this file
		// rather than an attack.
		return Claim{}, ErrClaim
	}
	if nowNs >= c.ExpiresNs {
		return Claim{}, ErrClaim
	}
	// A claim whose lifetime exceeds the bound was not minted by this server,
	// whatever it says: the seal proves the key, and this proves the rule.
	if c.ExpiresNs-c.IssuedNs > int64(ClaimLifetime) {
		return Claim{}, ErrClaim
	}
	return c, nil
}

// splitClaim separates the version from the sealed body.
func splitClaim(value string) (uint32, []byte, error) {
	head, tail, found := strings.Cut(value, ".")
	if !found || head == "" || tail == "" {
		return 0, nil, ErrClaim
	}
	v, err := strconv.ParseUint(head, 10, 32)
	if err != nil {
		return 0, nil, ErrClaim
	}
	body, derr := base64.RawURLEncoding.DecodeString(tail)
	if derr != nil {
		return 0, nil, ErrClaim
	}
	// In range: ParseUint was given a 32-bit width above.
	return uint32(v), body, nil
}

// claimAAD is the associated data: the format, the key version and the
// purpose.
//
// None is encrypted and all are authenticated, which is what makes a claim
// openable for one purpose only, under one key, in one format.
func claimAAD(keyVersion uint32, purpose ClaimPurpose) []byte {
	return []byte("sc-claim/f" + strconv.Itoa(ClaimFormat) +
		"/k" + strconv.FormatUint(uint64(keyVersion), 10) +
		"/" + string(purpose))
}
