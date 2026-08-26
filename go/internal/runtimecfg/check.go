package runtimecfg

import (
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"

	"github.com/heavycaffeiner/stowcloud/go/internal/smb"
)

// The rules a value has to satisfy, in one place because they are applied
// twice and must not drift.
//
// A save runs them and refuses, naming the field, with an administrator
// watching. The boot-time read runs the same ones and drops or clamps the
// value with a line in the log instead: refusing at boot makes a server
// unbootable over something saved weeks ago, which is the failure the
// emergency mode exists for and not one worth causing on purpose.

// CheckListen validates a bind address.
//
// The port is required. "0.0.0.0" alone parses as a host with no port and
// binds nothing, and the failure surfaces as a server that started and
// answers nowhere.
func CheckListen(v string) error {
	host, port, err := net.SplitHostPort(strings.TrimSpace(v))
	if err != nil {
		return fmt.Errorf("a bind address is host:port, and %q is not: %w", v, err)
	}
	if port == "" {
		return fmt.Errorf("a bind address needs a port, and %q has none", v)
	}
	n, perr := strconv.Atoi(port)
	if perr != nil || n < 1 || n > 65535 {
		return fmt.Errorf("%q is not a port between 1 and 65535", port)
	}
	// An empty host is every interface, which is what ":8443" means and is
	// allowed. A host that is not an address is refused: this binds a socket
	// and does not resolve names.
	if host == "" {
		return nil
	}
	if _, aerr := netip.ParseAddr(host); aerr != nil {
		return fmt.Errorf("%q is not an address this can bind; use an address or leave the host empty for every interface", host)
	}
	return nil
}

// CheckHost validates one entry of the app-host list. It is the name a request
// arrives under, so it carries no scheme, no path and no space.
func CheckHost(v string) error {
	h := strings.TrimSpace(v)
	if h == "" {
		return fmt.Errorf("a host must not be empty")
	}
	if strings.ContainsAny(h, " /\\") {
		return fmt.Errorf("%q is a name a request arrives under, not a URL", v)
	}
	return nil
}

// CheckCIDR validates one trusted-proxy range.
func CheckCIDR(v string) error {
	if _, err := netip.ParsePrefix(strings.TrimSpace(v)); err != nil {
		return fmt.Errorf("%q is not a CIDR range", v)
	}
	return nil
}

// ParsePrefixes turns the stored strings into ranges, dropping what will not
// parse. It is the boot-time half: the same entry is refused at save time.
func ParsePrefixes(in []string) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(in))
	for _, s := range in {
		p, err := netip.ParsePrefix(strings.TrimSpace(s))
		if err != nil {
			continue
		}
		out = append(out, p)
	}
	return out
}

// CheckSMBRender renders the configuration these settings would produce.
//
// The real renderer rather than a second copy of its rules, which is the only
// way this and the publish cannot disagree. That format has no escape
// surviving its own continuation and substitution rules, so the renderer
// refuses rather than escapes and this is where the refusal is heard.
func CheckSMBRender(s SMB) error {
	if s.ServiceUser == "" {
		return fmt.Errorf("every SMB connection runs as one account, and none is named")
	}
	_, err := smb.Render(smb.Config{
		Enabled:         true,
		Workgroup:       s.Workgroup,
		ServerName:      s.ServerName,
		Interfaces:      s.Interfaces,
		ServiceUser:     s.ServiceUser,
		AllowPublicBind: s.AllowPublicBind,
	}, nil)
	return err
}
