package objstore

import (
	"encoding/xml"
	"fmt"
	"strings"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
)

// listBucketResult is the subset of a ListObjectsV2 response this package
// reads. Every field on it arrived over HTTPS from whatever answers at the
// configured endpoint, which is untrusted the same way a request body is.
type listBucketResult struct {
	XMLName               xml.Name           `xml:"ListBucketResult"`
	IsTruncated           bool               `xml:"IsTruncated"`
	NextContinuationToken string             `xml:"NextContinuationToken"`
	Contents              []listObject       `xml:"Contents"`
	CommonPrefixes        []listCommonPrefix `xml:"CommonPrefixes"`
}

type listObject struct {
	Key          string `xml:"Key"`
	LastModified string `xml:"LastModified"`
	Size         uint64 `xml:"Size"`
	ETag         string `xml:"ETag"`
}

type listCommonPrefix struct {
	Prefix string `xml:"Prefix"`
}

// parseListBucketResult decodes one page and validates every key and common
// prefix it names against expectedPrefix before anything downstream ever
// joins them into a path: a key outside the prefix, too long, or carrying a
// component ParseSafePath itself would refuse is a refusal here, not a
// value this package materializes and hopes for the best about.
func parseListBucketResult(body []byte, expectedPrefix string) (*listBucketResult, error) {
	var result listBucketResult
	if err := xml.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("objstore: parse list response: %w", err)
	}
	if len(result.Contents)+len(result.CommonPrefixes) > maxListEntries {
		return nil, fmt.Errorf("objstore: list response carries %d entries, more than %d",
			len(result.Contents)+len(result.CommonPrefixes), maxListEntries)
	}
	for _, c := range result.Contents {
		if _, err := validateListedKey(c.Key, expectedPrefix); err != nil {
			return nil, err
		}
	}
	for _, p := range result.CommonPrefixes {
		if _, err := validateListedKey(p.Prefix, expectedPrefix); err != nil {
			return nil, err
		}
	}
	return &result, nil
}

// validateListedKey refuses a key or common prefix the endpoint returned
// that could not have come from an honest listing of expectedPrefix: one
// longer than S3 itself allows, one that does not even carry the prefix
// asked for, or one whose components, once the prefix is stripped and any
// single trailing directory-marker slash removed, ParseSafePath refuses.
// It returns the part of the key after expectedPrefix, which every caller
// needs next and would otherwise recompute.
func validateListedKey(key, expectedPrefix string) (string, error) {
	if len(key) > maxObjectKeyBytes {
		return "", fmt.Errorf("objstore: listed key exceeds %d bytes", maxObjectKeyBytes)
	}
	if !strings.HasPrefix(key, expectedPrefix) {
		return "", fmt.Errorf("objstore: listed key %q escapes its prefix %q", key, expectedPrefix)
	}
	rest := strings.TrimPrefix(key, expectedPrefix)
	if check := strings.TrimSuffix(rest, "/"); check != "" {
		if _, err := vfs.ParseSafePath(check); err != nil {
			return "", fmt.Errorf("objstore: listed key %q: %w", key, err)
		}
	}
	return rest, nil
}
