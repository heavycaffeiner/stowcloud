package objstore

import (
	"errors"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
)

func TestParseListBucketResultOnePage(t *testing.T) {
	body := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<ListBucketResult>
  <IsTruncated>false</IsTruncated>
  <Contents>
    <Key>team/photo.jpg</Key>
    <LastModified>2024-01-02T03:04:05.000Z</LastModified>
    <Size>11</Size>
    <ETag>"abc"</ETag>
  </Contents>
  <CommonPrefixes>
    <Prefix>team/albums/</Prefix>
  </CommonPrefixes>
</ListBucketResult>`)

	result, err := parseListBucketResult(body, "team/")
	if err != nil {
		t.Fatalf("parseListBucketResult: %v", err)
	}
	if result.IsTruncated {
		t.Fatal("IsTruncated should be false")
	}
	if len(result.Contents) != 1 || result.Contents[0].Key != "team/photo.jpg" {
		t.Fatalf("Contents = %+v", result.Contents)
	}
	if len(result.CommonPrefixes) != 1 || result.CommonPrefixes[0].Prefix != "team/albums/" {
		t.Fatalf("CommonPrefixes = %+v", result.CommonPrefixes)
	}
}

func TestParseListBucketResultContinuationPage(t *testing.T) {
	body := []byte(`<ListBucketResult>
  <IsTruncated>true</IsTruncated>
  <NextContinuationToken>opaque-token==</NextContinuationToken>
  <Contents>
    <Key>team/a.txt</Key>
    <Size>1</Size>
  </Contents>
</ListBucketResult>`)

	result, err := parseListBucketResult(body, "team/")
	if err != nil {
		t.Fatalf("parseListBucketResult: %v", err)
	}
	if !result.IsTruncated {
		t.Fatal("IsTruncated should be true")
	}
	if result.NextContinuationToken != "opaque-token==" {
		t.Fatalf("NextContinuationToken = %q", result.NextContinuationToken)
	}
}

func TestParseListBucketResultRefusesHostileResponses(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		prefix string
	}{
		{
			name:   "huge key count",
			body:   hostileManyKeysXML(maxListEntries + 1),
			prefix: "",
		},
		{
			name:   "key escaping the configured prefix",
			body:   `<ListBucketResult><Contents><Key>other/leak.txt</Key></Contents></ListBucketResult>`,
			prefix: "team/",
		},
		{
			name:   "key with a .. component",
			body:   `<ListBucketResult><Contents><Key>team/../etc/passwd</Key></Contents></ListBucketResult>`,
			prefix: "team/",
		},
		{
			name:   "common prefix with a .. component",
			body:   `<ListBucketResult><CommonPrefixes><Prefix>team/../x/</Prefix></CommonPrefixes></ListBucketResult>`,
			prefix: "team/",
		},
		{
			name:   "key over the S3 length bound",
			body:   `<ListBucketResult><Contents><Key>` + strings.Repeat("a", maxObjectKeyBytes+1) + `</Key></Contents></ListBucketResult>`,
			prefix: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseListBucketResult([]byte(tc.body), tc.prefix); err == nil {
				t.Fatal("parseListBucketResult accepted a hostile response")
			}
		})
	}
}

func hostileManyKeysXML(n int) string {
	var b strings.Builder
	b.WriteString("<ListBucketResult>")
	for i := 0; i < n; i++ {
		b.WriteString("<Contents><Key>k</Key></Contents>")
	}
	b.WriteString("</ListBucketResult>")
	return b.String()
}

func TestValidateListedKeyAcceptsItsOwnDirectoryMarker(t *testing.T) {
	rest, err := validateListedKey("team/", "team/")
	if err != nil {
		t.Fatalf("validateListedKey: %v", err)
	}
	if rest != "" {
		t.Fatalf("rest = %q, want empty", rest)
	}
}

func TestValidateListedKeyRefusesADotDotComponent(t *testing.T) {
	_, err := validateListedKey("team/../secret", "team/")
	if err == nil {
		t.Fatal("validateListedKey accepted a .. component")
	}
	if !errors.Is(err, vfs.ErrInvalidName) {
		t.Fatalf("error = %v, want it to wrap vfs.ErrInvalidName", err)
	}
}
