//go:build linux && compat_nc

package nc

import (
	"context"
	"errors"

	"github.com/heavycaffeiner/stowcloud/go/internal/compat/ncport"
)

// The direct-URL endpoint: a URL an external media player can open.
//
// Both apps hand the result to a separate player process so a video streams
// rather than downloading whole first. That process carries none of the
// caller's credentials, which is both why the endpoint exists and why it is
// the sharpest thing in this layer.
//
// Four rules, all applied when the URL is minted rather than when it is
// fetched:
//
//  1. It names one file, resolved and permission-checked here, under the
//     calling principal. The URL itself carries no identity.
//  2. It lives minutes rather than the hours a share link gets, because the
//     client uses it immediately.
//  3. It is GET and read only. It is not a general capability for that file.
//  4. It is served from the content origin, so a URL that leaks cannot reach
//     an app-origin route and has no session cookie to borrow.
//
// Rules two through four belong to the signing mechanism, which mints the same
// kind of claim a preview URL carries. Rule one is here.

// DirectPort mints the signed URL.
type DirectPort interface {
	// Locate resolves a file id to a path under the calling user, or reports
	// that the caller cannot see it.
	Locate(ctx context.Context, user ncport.UserID, id FileID) (string, error)
	// SignedDownloadURL mints a short-lived, read-only, content-origin URL.
	// It reports no URL when there is no content origin configured, which is
	// a legitimate deployment rather than an error.
	SignedDownloadURL(ctx context.Context, user ncport.UserID, path string) (string, bool, error)
}

// MintDirectURL answers the endpoint.
//
// Every failure is not-found, deliberately. A file id the caller cannot read
// must not be distinguishable from one that does not exist: a forbidden would
// confirm that the id names something, which is the existence rule holding on
// this mount as it does everywhere else.
func MintDirectURL(ctx context.Context, port DirectPort, user ncport.UserID, id FileID) (Val, *OCSError) {
	notFound := NotFound("File not found")

	path, err := port.Locate(ctx, user, id)
	if err != nil {
		return Val{}, notFound
	}

	url, ok, err := port.SignedDownloadURL(ctx, user, path)
	if err != nil || !ok || url == "" {
		// No content origin, or nothing to sign over. Either way there is no
		// URL to hand out, and saying so beats handing back one that cannot
		// work: the player would fail with a network error the user reads as
		// a broken file.
		return Val{}, notFound
	}
	return VMap(F("url", VStr(url))), nil
}

// ParseFileID reads the file id a direct request names.
func ParseFileID(raw string) (FileID, error) {
	if raw == "" {
		return 0, errors.New("nc: no fileId")
	}
	var n uint64
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if c < '0' || c > '9' {
			return 0, errors.New("nc: fileId is not a number")
		}
		// Bounded as it is built, so a long digit string cannot wrap into a
		// small id that names somebody else's file.
		next := n*10 + uint64(c-'0')
		if next < n {
			return 0, errors.New("nc: fileId is out of range")
		}
		n = next
	}
	return FileID(n), nil
}
