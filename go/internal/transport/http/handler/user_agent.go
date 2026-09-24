// Linux only, matching the account handler package.
//go:build linux

package handler

import "strings"

// DescribeUserAgent turns a raw User-Agent header into a short display label.
// It is deliberately best-effort and display-only: callers must retain and
// display the raw value separately, and must not use this label for identity
// or authorization.
func DescribeUserAgent(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}

	os := detectUserAgentOS(raw)
	browser := detectUserAgentBrowser(raw)
	switch {
	case os != "" && browser != "":
		return os + " - " + browser
	case os != "":
		return os
	case browser != "":
		return browser
	default:
		return raw
	}
}

func detectUserAgentOS(ua string) string {
	switch {
	case strings.Contains(strings.ToLower(ua), "windows"):
		return "Windows"
	case strings.Contains(strings.ToLower(ua), "iphone") || strings.Contains(strings.ToLower(ua), "ipad") || strings.Contains(strings.ToLower(ua), "ipod"):
		return "iOS"
	case strings.Contains(strings.ToLower(ua), "android"):
		return "Android"
	case strings.Contains(strings.ToLower(ua), "mac os x") || strings.Contains(strings.ToLower(ua), "macintosh"):
		return "macOS"
	case strings.Contains(strings.ToLower(ua), "cros"):
		return "ChromeOS"
	case strings.Contains(strings.ToLower(ua), "linux"):
		return "Linux"
	default:
		return ""
	}
}

func detectUserAgentBrowser(ua string) string {
	lower := strings.ToLower(ua)
	switch {
	case strings.Contains(lower, "edg/"):
		return "Edge"
	case strings.Contains(lower, "opr/") || strings.Contains(lower, "opera"):
		return "Opera"
	case strings.Contains(lower, "headlesschrome"):
		return "Chrome (headless)"
	case strings.Contains(lower, "chrome/"):
		return "Chrome"
	case strings.Contains(lower, "firefox/"):
		return "Firefox"
	case strings.Contains(lower, "safari/"):
		return "Safari"
	default:
		return ""
	}
}
