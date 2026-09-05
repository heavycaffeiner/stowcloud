package objstore

import (
	"encoding/json"
	"strings"
	"testing"
)

func validConfigJSON(t *testing.T, overrides map[string]any) []byte {
	t.Helper()
	fields := map[string]any{
		"endpoint":      "https://minio.example:9000",
		"region":        "us-east-1",
		"bucket":        "photos",
		"prefix":        "team",
		"access_key_id": "AKIAEXAMPLE",
		"path_style":    true,
	}
	for k, v := range overrides {
		fields[k] = v
	}
	b, err := json.Marshal(fields)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	return b
}

func TestParseConfigAcceptsAValidConfig(t *testing.T) {
	cfg, err := ParseConfig(validConfigJSON(t, nil))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if cfg.Bucket != "photos" || cfg.Prefix != "team" || cfg.Region != "us-east-1" {
		t.Fatalf("cfg = %+v", cfg)
	}
	if got, want := cfg.Describe(), "s3://photos/team at https://minio.example:9000"; got != want {
		t.Fatalf("Describe() = %q, want %q", got, want)
	}
}

func TestParseConfigTrimsATrailingSlashFromThePrefix(t *testing.T) {
	cfg, err := ParseConfig(validConfigJSON(t, map[string]any{"prefix": "team/"}))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if cfg.Prefix != "team" {
		t.Fatalf("Prefix = %q, want %q", cfg.Prefix, "team")
	}
}

func TestParseConfigEmptyPrefixIsTheBucketRoot(t *testing.T) {
	cfg, err := ParseConfig(validConfigJSON(t, map[string]any{"prefix": ""}))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if got, want := cfg.Describe(), "s3://photos at https://minio.example:9000"; got != want {
		t.Fatalf("Describe() = %q, want %q", got, want)
	}
}

func TestParseConfigRefusals(t *testing.T) {
	cases := []struct {
		name      string
		overrides map[string]any
	}{
		{"empty bucket", map[string]any{"bucket": ""}},
		{"endpoint not an absolute URL", map[string]any{"endpoint": "minio.example:9000"}},
		{"endpoint with a non-http scheme", map[string]any{"endpoint": "ftp://minio.example"}},
		{"endpoint with a path", map[string]any{"endpoint": "https://minio.example:9000/v1"}},
		{"endpoint with a query", map[string]any{"endpoint": "https://minio.example:9000?x=1"}},
		{"endpoint with a fragment", map[string]any{"endpoint": "https://minio.example:9000#frag"}},
		{"bucket with a .. component", map[string]any{"bucket": "a/../b"}},
		{"bucket with a leading slash", map[string]any{"bucket": "/photos"}},
		{"bucket with a control character", map[string]any{"bucket": "photos\x01"}},
		{"bucket component ParseSafePath refuses", map[string]any{"bucket": "team/./x"}},
		{"prefix with a .. component", map[string]any{"prefix": "a/../b"}},
		{"prefix with a leading slash", map[string]any{"prefix": "/team"}},
		{"prefix with an embedded NUL", map[string]any{"prefix": "a\x00b"}},
		{"prefix with a control character", map[string]any{"prefix": "team\tfiles"}},
		{"prefix longer than 512 bytes", map[string]any{"prefix": strings.Repeat("a", 513)}},
		{"region with uppercase letters", map[string]any{"region": "US-EAST-1"}},
		{"region with an underscore", map[string]any{"region": "us_east_1"}},
		{"region empty", map[string]any{"region": ""}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseConfig(validConfigJSON(t, tc.overrides)); err == nil {
				t.Fatalf("ParseConfig accepted %+v", tc.overrides)
			}
		})
	}
}

func TestConfigMarshalRoundTrips(t *testing.T) {
	cfg, err := ParseConfig(validConfigJSON(t, nil))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	b, err := cfg.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got, err := ParseConfig(b)
	if err != nil {
		t.Fatalf("ParseConfig(Marshal()): %v", err)
	}
	if got != cfg {
		t.Fatalf("round trip = %+v, want %+v", got, cfg)
	}
}
