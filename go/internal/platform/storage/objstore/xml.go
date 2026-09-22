package objstore

import (
	"fmt"
	"strings"

	"github.com/heavycaffeiner/stowcloud/go/internal/platform/storage/vfs"
)

// listBucketResult is the validated product projection of the SDK list output.
// It carries no XML parsing responsibility.
type listBucketResult struct {
	IsTruncated           bool
	NextContinuationToken string
	Contents              []listObject
	CommonPrefixes        []listCommonPrefix
}

type listObject struct {
	Key, LastModified, ETag string
	Size                    uint64
}

type listCommonPrefix struct{ Prefix string }

func validateListedKey(k, p string) (string, error) {
	if len(k) > maxObjectKeyBytes {
		return "", fmt.Errorf("objstore: listed key exceeds limit")
	}
	if !strings.HasPrefix(k, p) {
		return "", fmt.Errorf("objstore: listed key escapes prefix")
	}
	rest := strings.TrimPrefix(k, p)
	if x := strings.TrimSuffix(rest, "/"); x != "" {
		if _, e := vfs.ParseSafePath(x); e != nil {
			return "", e
		}
	}
	return rest, nil
}
