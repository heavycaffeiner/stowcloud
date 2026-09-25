// Linux only, matching the package under test.
//go:build linux

package handler

import (
	"testing"

	"github.com/heavycaffeiner/stowcloud/backend/internal/feature/auth"
)

func TestDescribeUserAgentUsesSpecificBrowserPrecedence(t *testing.T) {
	cases := []struct {
		name string
		ua   string
		want string
	}{
		{
			name: "edge before chrome",
			ua:   "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/120.0 Safari/537.36 Edg/120.0",
			want: "Windows - Edge",
		},
		{
			name: "headless before chrome",
			ua:   "Mozilla/5.0 (Windows NT 10.0) AppleWebKit/537.36 HeadlessChrome/150.0 Safari/537.36",
			want: "Windows - Chrome (headless)",
		},
		{
			name: "ios before macos",
			ua:   "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 Version/17.0 Mobile/15E148 Safari/604.1",
			want: "iOS - Safari",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DescribeUserAgent(tc.ua); got != tc.want {
				t.Fatalf("DescribeUserAgent() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDescribeUserAgentPreservesUnknownAndEmptyFallbacks(t *testing.T) {
	if got := DescribeUserAgent("rclone/v1.66.0"); got != "rclone/v1.66.0" {
		t.Fatalf("unknown UA = %q, want raw UA", got)
	}
	if got := SessionOf(auth.SessionRow{IDHash: []byte("session"), UA: ""}, nil).UADisplay; got != "" {
		t.Fatalf("empty UA display = %q, want empty for client fallback", got)
	}
}
