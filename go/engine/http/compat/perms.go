//go:build linux && compat_nc

// The vendor permission string, and the time header parsing beside it.
package compat

import (
	"errors"
	"math"
	"strings"
)

// The refusals a caller distinguishes.
var (
	// ErrBadTime reports an unusable time header.
	ErrBadTime = errors.New("an unusable time value")
)

// Ability is one thing a caller may do with a resource.
//
// Declared here rather than imported from the permission service. This package
// describes a wire format, and whoever mounts it maps the service's own type
// onto these: a vendor string that reached into the evaluator's type would tie
// the protocol to a decision the service is free to change.
type Ability uint8

// The abilities the letter string reads.
const (
	// CanRead is the ability to read the resource.
	CanRead Ability = 1 << iota
	// CanWrite is the ability to replace a file's content.
	CanWrite
	// CanCreate is the ability to add members to a collection.
	CanCreate
	// CanDelete is the ability to remove the resource.
	CanDelete
	// CanRename is the ability to change its name in place.
	CanRename
	// CanMove is the ability to move it elsewhere.
	CanMove
	// CanShare is the ability to grant others access.
	CanShare
)

// Has reports whether every named ability is present.
func (a Ability) Has(want Ability) bool { return a&want == want }

// Perms describes what a resource permits, for the letter string.
type Perms struct {
	// Allowed is the caller's ability set on the resource.
	Allowed Ability
	// Shareable is whether the deployment offers sharing at all.
	Shareable bool
	// Mounted is whether the resource is a share root the caller received.
	Mounted bool
	// Directory is whether the resource is a collection.
	Directory bool
}

// PermissionLetters renders the vendor permission string.
//
// The order is fixed and clients parse it positionally, so it is written out
// as a sequence rather than assembled from a set. A missing letter changes
// what a client offers the user: without W it hides editing, without CK it
// refuses to upload into a directory, and without D it hides delete.
//
// M is never emitted. It marks a resource mounted from elsewhere, and this
// server has no federation to mount from, so claiming it would make a client
// look for a remote it cannot reach.
func PermissionLetters(p Perms) string {
	letters := make([]byte, 0, 8)

	if p.Shareable && p.Allowed.Has(CanShare) {
		letters = append(letters, 'S')
	}
	// R is the reshare bit. Grant chains are not offered, so it appears only
	// where the caller may share and the resource is not itself received.
	if p.Shareable && p.Allowed.Has(CanShare) && !p.Mounted {
		letters = append(letters, 'R')
	}
	if p.Mounted {
		letters = append(letters, 'G')
	}
	if p.Allowed.Has(CanDelete) {
		letters = append(letters, 'D')
	}
	if p.Allowed.Has(CanRename) {
		letters = append(letters, 'N')
	}
	if p.Allowed.Has(CanMove) {
		letters = append(letters, 'V')
	}

	// The tail differs by kind: a file is written, a directory is created into.
	if p.Directory {
		if p.Allowed.Has(CanCreate) {
			letters = append(letters, 'C', 'K')
		}
		return string(letters)
	}
	if p.Allowed.Has(CanWrite) {
		letters = append(letters, 'W')
	}
	return string(letters)
}

// ParseSeconds reads a vendor time header.
//
// Integer or fixed-point seconds, with the fraction truncated rather than
// rounded: a rounded modification time can land in the future, and a client
// comparing it against its own clock then re-uploads a file it already has.
// Exponent notation and anything that does not fit refuse, because a time this
// server cannot represent is not a time it should store.
func ParseSeconds(raw string) (int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, ErrBadTime
	}

	negative := false
	switch raw[0] {
	case '-':
		negative = true
		raw = raw[1:]
	case '+':
		raw = raw[1:]
	}
	if raw == "" {
		return 0, ErrBadTime
	}

	whole := raw
	if dot := strings.IndexByte(raw, '.'); dot >= 0 {
		whole = raw[:dot]
		fraction := raw[dot+1:]
		// The fraction is discarded but still has to be digits: a value like
		// "1.5e3" is exponent notation wearing a decimal point.
		if fraction == "" || !allDigits(fraction) {
			return 0, ErrBadTime
		}
	}
	if whole == "" || !allDigits(whole) {
		return 0, ErrBadTime
	}

	var n int64
	for i := 0; i < len(whole); i++ {
		digit := int64(whole[i] - '0')
		if n > (math.MaxInt64-digit)/10 {
			return 0, ErrBadTime
		}
		n = n*10 + digit
	}
	if negative {
		return -n, nil
	}
	return n, nil
}

// allDigits reports whether every byte is an ASCII digit.
func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// ClampToInt64 narrows an unsigned count for a wire field that is signed.
//
// One helper rather than a cast at each site. A wrapped value reads as
// negative free space, and an Android client that sees negative free space
// parks every upload it has queued.
func ClampToInt64(v uint64) int64 {
	if v > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(v)
}

// QuotaUnlimited is the value the quota field carries when no limit applies.
//
// Only the quota field. The free, used and total fields stay real
// non-negative numbers, because a client subtracting a sentinel from a real
// number gets a size it then acts on.
const QuotaUnlimited = -3
