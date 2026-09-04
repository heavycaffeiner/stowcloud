// Linux only, matching the package under test.
//go:build linux

package handler

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
)

const claimNow = int64(1700000000000000000)

func claimKey(b byte) ClaimKey {
	key := make([]byte, 32)
	for i := range key {
		key[i] = b
	}
	return ClaimKey{Version: 1, Key: key}
}

func keyring(k ClaimKey) map[uint32][]byte { return map[uint32][]byte{k.Version: k.Key} }

func aThumb() Claim {
	return Claim{Purpose: PurposeThumb, UserID: 7, Path: "photos/summer.jpg", Width: 256, Height: 256}
}

// A sealed claim opens back into what was put in it.
func TestAClaimRoundTrips(t *testing.T) {
	k := claimKey(1)
	sealed, err := SealClaim(k, aThumb(), claimNow)
	if err != nil {
		t.Fatalf("SealClaim: %v", err)
	}

	got, oerr := OpenClaim(keyring(k), PurposeThumb, sealed, claimNow)
	if oerr != nil {
		t.Fatalf("OpenClaim: %v", oerr)
	}
	if got.UserID != 7 || got.Path != "photos/summer.jpg" {
		t.Errorf("the claim opened as %+v", got)
	}
	if got.Width != 256 || got.Height != 256 {
		t.Errorf("the dimensions opened as %dx%d", got.Width, got.Height)
	}
	// A thumb claim only narrows a session, so it lives the longer of the two
	// lifetimes: the route that opens one refuses it for any other account.
	if got.ExpiresNs-got.IssuedNs != int64(ClaimLifetimeBound) {
		t.Errorf("the lifetime is %v", time.Duration(got.ExpiresNs-got.IssuedNs))
	}
}

// The claim carries no reusable credential. What leaks is the right to fetch
// one file for a few minutes, under an account whose permission is checked
// again at fetch time.
func TestAClaimCarriesNoCredential(t *testing.T) {
	k := claimKey(1)
	sealed, err := SealClaim(k, aThumb(), claimNow)
	if err != nil {
		t.Fatalf("SealClaim: %v", err)
	}

	// The sealed value is opaque: none of the plaintext is readable in it.
	if strings.Contains(sealed, "photos") || strings.Contains(sealed, "summer") {
		t.Errorf("the path is readable in the sealed value: %s", sealed)
	}
	// Only the key version travels in the clear, which the opener needs to
	// pick a key before it can open anything.
	head, _, _ := strings.Cut(sealed, ".")
	if head != strconv.FormatUint(uint64(k.Version), 10) {
		t.Errorf("the clear part is %q", head)
	}
}

// A thumbnail claim cannot be opened as a download. The purpose is in the
// AAD, so it fails to open rather than opening and being rejected after.
func TestAPurposeCannotBeCrossed(t *testing.T) {
	k := claimKey(1)

	thumb, err := SealClaim(k, aThumb(), claimNow)
	if err != nil {
		t.Fatalf("SealClaim: %v", err)
	}
	if _, oerr := OpenClaim(keyring(k), PurposeDownload, thumb, claimNow); !errors.Is(oerr, ErrClaim) {
		t.Errorf("a thumbnail claim opened as a download: %v", oerr)
	}

	download, err := SealClaim(k, Claim{Purpose: PurposeDownload, UserID: 7, Path: "a.txt"}, claimNow)
	if err != nil {
		t.Fatalf("SealClaim: %v", err)
	}
	if _, oerr := OpenClaim(keyring(k), PurposeThumb, download, claimNow); !errors.Is(oerr, ErrClaim) {
		t.Errorf("a download claim opened as a thumbnail: %v", oerr)
	}
	// Each still opens for what it was made for.
	if _, oerr := OpenClaim(keyring(k), PurposeDownload, download, claimNow); oerr != nil {
		t.Errorf("a download claim did not open as a download: %v", oerr)
	}
}

// A claim expires, and the bound is the one this server mints.
func TestAClaimExpires(t *testing.T) {
	k := claimKey(1)
	sealed, err := SealClaim(k, aThumb(), claimNow)
	if err != nil {
		t.Fatalf("SealClaim: %v", err)
	}

	justBefore := claimNow + int64(ClaimLifetimeBound) - 1
	if _, oerr := OpenClaim(keyring(k), PurposeThumb, sealed, justBefore); oerr != nil {
		t.Fatalf("a claim inside its lifetime was refused: %v", oerr)
	}
	atExpiry := claimNow + int64(ClaimLifetimeBound)
	if _, oerr := OpenClaim(keyring(k), PurposeThumb, sealed, atExpiry); !errors.Is(oerr, ErrClaim) {
		t.Errorf("a claim at its expiry opened: %v", oerr)
	}
}

// The unauthenticated purpose keeps the short lifetime. It stands alone as a
// credential, so a copy in a log has to stop working quickly.
func TestAnUnboundClaimExpiresSooner(t *testing.T) {
	k := claimKey(1)
	c := aThumb()
	c.Purpose = PurposeDownload
	sealed, err := SealClaim(k, c, claimNow)
	if err != nil {
		t.Fatalf("SealClaim: %v", err)
	}

	got, oerr := OpenClaim(keyring(k), PurposeDownload, sealed, claimNow)
	if oerr != nil {
		t.Fatalf("OpenClaim: %v", oerr)
	}
	if got.ExpiresNs-got.IssuedNs != int64(ClaimLifetime) {
		t.Errorf("an unbound claim lives %v", time.Duration(got.ExpiresNs-got.IssuedNs))
	}
	atExpiry := claimNow + int64(ClaimLifetime)
	if _, err := OpenClaim(keyring(k), PurposeDownload, sealed, atExpiry); !errors.Is(err, ErrClaim) {
		t.Errorf("an unbound claim at its expiry opened: %v", err)
	}
}

// A purpose is bound into the seal, so a claim minted for one route cannot be
// spent on another.
func TestAClaimDoesNotCrossPurposes(t *testing.T) {
	k := claimKey(1)
	c := aThumb()
	c.Purpose = PurposeContent
	sealed, err := SealClaim(k, c, claimNow)
	if err != nil {
		t.Fatalf("SealClaim: %v", err)
	}

	for _, other := range []ClaimPurpose{PurposeThumb, PurposeDownload} {
		if _, oerr := OpenClaim(keyring(k), other, sealed, claimNow); !errors.Is(oerr, ErrClaim) {
			t.Errorf("a content claim opened as %s: %v", other, oerr)
		}
	}
	if _, oerr := OpenClaim(keyring(k), PurposeContent, sealed, claimNow); oerr != nil {
		t.Errorf("a content claim did not open for its own purpose: %v", oerr)
	}
}

// A rotation keeps opening claims minted under the key it replaced. A
// five-minute claim outlives the rotation, and a page loading its thumbnails
// should not see every one fail.
func TestARotationKeepsOpeningRecentClaims(t *testing.T) {
	old := ClaimKey{Version: 1, Key: make([]byte, 32)}
	for i := range old.Key {
		old.Key[i] = 0xA1
	}
	sealed, err := SealClaim(old, aThumb(), claimNow)
	if err != nil {
		t.Fatalf("SealClaim: %v", err)
	}

	// The new key is live and the old one is retained.
	newKey := ClaimKey{Version: 2, Key: make([]byte, 32)}
	for i := range newKey.Key {
		newKey.Key[i] = 0xB2
	}
	both := map[uint32][]byte{old.Version: old.Key, newKey.Version: newKey.Key}

	if _, oerr := OpenClaim(both, PurposeThumb, sealed, claimNow); oerr != nil {
		t.Errorf("a claim under the retired key was refused: %v", oerr)
	}

	// Once the old key is dropped it stops opening, which is what retiring a
	// key means.
	only := map[uint32][]byte{newKey.Version: newKey.Key}
	if _, oerr := OpenClaim(only, PurposeThumb, sealed, claimNow); !errors.Is(oerr, ErrClaim) {
		t.Errorf("a claim under a dropped key still opened: %v", oerr)
	}
}

// Everything this server will not open answers the same, because telling the
// reasons apart tells a forger which part to fix.
func TestEveryRefusalIsTheSameAnswer(t *testing.T) {
	k := claimKey(1)
	sealed, err := SealClaim(k, aThumb(), claimNow)
	if err != nil {
		t.Fatalf("SealClaim: %v", err)
	}

	for _, c := range []struct{ what, value string }{
		{"empty", ""},
		{"no version", "abcdef"},
		{"a version that is not a number", "x." + strings.SplitN(sealed, ".", 2)[1]},
		{"an unknown version", "99." + strings.SplitN(sealed, ".", 2)[1]},
		{"a body that is not base64", "1.!!!!"},
		{"a truncated body", sealed[:len(sealed)-4]},
		{"one character changed", flipLastChar(sealed)},
		{"only a version", "1."},
		{"only a dot", "."},
	} {
		_, oerr := OpenClaim(keyring(k), PurposeThumb, c.value, claimNow)
		if !errors.Is(oerr, ErrClaim) {
			t.Errorf("%s returned %v", c.what, oerr)
		}
	}

	// A different key is the same answer, not a distinguishable one.
	other := claimKey(9)
	if _, oerr := OpenClaim(keyring(other), PurposeThumb, sealed, claimNow); !errors.Is(oerr, ErrClaim) {
		t.Errorf("a claim under the wrong key returned %v", oerr)
	}
}

func flipLastChar(s string) string {
	if s == "" {
		return s
	}
	last := s[len(s)-1]
	if last == 'A' {
		return s[:len(s)-1] + "B"
	}
	return s[:len(s)-1] + "A"
}

// Two seals of the same claim differ, so a claim is not a stable identifier
// for the file it names.
func TestTwoSealsOfOneClaimDiffer(t *testing.T) {
	k := claimKey(1)
	first, err := SealClaim(k, aThumb(), claimNow)
	if err != nil {
		t.Fatalf("SealClaim: %v", err)
	}
	second, err := SealClaim(k, aThumb(), claimNow)
	if err != nil {
		t.Fatalf("SealClaim: %v", err)
	}
	if first == second {
		t.Error("two seals of the same claim are identical, so the value is a stable identifier")
	}
}

// A key of the wrong size is refused rather than used, since a short key is a
// configuration mistake that would otherwise seal everything weakly.
func TestAWrongSizedKeyIsRefused(t *testing.T) {
	for _, n := range []int{0, 16, 31, 33} {
		k := ClaimKey{Version: 1, Key: make([]byte, n)}
		if _, err := SealClaim(k, aThumb(), claimNow); !errors.Is(err, ErrClaim) {
			t.Errorf("a %d-byte key sealed: %v", n, err)
		}
	}
}

// A purpose the server does not know is refused at seal time, so no claim
// exists that the opener would have to decide about.
func TestAnUnknownPurposeIsNotSealed(t *testing.T) {
	k := claimKey(1)
	_, err := SealClaim(k, Claim{Purpose: "delete", UserID: 7, Path: "a.txt"}, claimNow)
	if !errors.Is(err, ErrClaim) {
		t.Errorf("an unknown purpose sealed: %v", err)
	}
}
