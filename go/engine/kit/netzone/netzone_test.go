package netzone

import (
	"errors"
	"net/netip"
	"testing"
)

func TestEnclosingPrivateRange(t *testing.T) {
	cases := []struct {
		name string
		addr string
		want string
	}{
		{"rfc1918 class A", "10.1.2.3", "10.0.0.0/8"},
		{"rfc1918 class B low", "172.16.0.1", "172.16.0.0/12"},
		{"rfc1918 class B high", "172.31.255.254", "172.16.0.0/12"},
		{"rfc1918 class B below range", "172.15.0.1", ""},
		{"rfc1918 class B above range", "172.32.0.1", ""},
		{"rfc1918 class C", "192.168.5.9", "192.168.0.0/16"},
		{"ipv4 loopback", "127.0.0.1", "127.0.0.0/8"},
		{"ipv4 link-local", "169.254.1.1", "169.254.0.0/16"},
		{"carrier-nat low", "100.64.0.1", "100.64.0.0/10"},
		{"carrier-nat high", "100.127.255.255", "100.64.0.0/10"},
		{"carrier-nat below range", "100.63.255.255", ""},
		{"carrier-nat above range", "100.128.0.1", ""},
		{"ipv4 public", "8.8.8.8", ""},
		{"ipv6 loopback", "::1", "::1/128"},
		{"ipv6 ula fc", "fc00::1", "fc00::/7"},
		{"ipv6 ula fd", "fd12:3456::1", "fc00::/7"},
		{"ipv6 link-local", "fe80::1", "fe80::/10"},
		{"ipv6 public", "2001:db8::1", ""},
		{"ipv4-mapped private", "::ffff:10.0.0.1", "10.0.0.0/8"},
		{"ipv4-mapped public", "::ffff:8.8.8.8", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			addr := netip.MustParseAddr(tc.addr)
			got := EnclosingPrivateRange(addr)
			if got != tc.want {
				t.Errorf("EnclosingPrivateRange(%s) = %q, want %q", tc.addr, got, tc.want)
			}
			if want := tc.want != ""; IsPrivate(addr) != want {
				t.Errorf("IsPrivate(%s) = %v, want %v", tc.addr, !want, want)
			}
		})
	}
}

func TestPrivateCIDRsReturnsFreshSlice(t *testing.T) {
	first := PrivateCIDRs()
	if len(first) == 0 {
		t.Fatal("PrivateCIDRs returned an empty slice")
	}
	first[0] = "mutated"

	second := PrivateCIDRs()
	if second[0] == "mutated" {
		t.Fatal("PrivateCIDRs shares backing storage across calls")
	}
}

func TestParseAddrSpecAcceptsPlainForms(t *testing.T) {
	got, err := ParseAddrSpec("192.168.1.1")
	if err != nil {
		t.Fatalf("ParseAddrSpec bare address: %v", err)
	}
	if want := netip.MustParseAddr("192.168.1.1"); got != want {
		t.Errorf("got %v, want %v", got, want)
	}

	got, err = ParseAddrSpec("192.168.1.0/24")
	if err != nil {
		t.Fatalf("ParseAddrSpec with prefix: %v", err)
	}
	if want := netip.MustParseAddr("192.168.1.0"); got != want {
		t.Errorf("got %v, want %v", got, want)
	}

	got, err = ParseAddrSpec("fe80::1/64")
	if err != nil {
		t.Fatalf("ParseAddrSpec ipv6 with prefix: %v", err)
	}
	if want := netip.MustParseAddr("fe80::1"); got != want {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseAddrSpecRefusesGarbage(t *testing.T) {
	cases := []struct {
		name string
		spec string
	}{
		{"not an address", "eth0"},
		{"not a number prefix", "192.168.1.0/abc"},
		{"prefix too long for ipv4", "192.168.1.0/33"},
		{"prefix too long for ipv6", "fe80::1/129"},
		{"negative prefix", "192.168.1.0/-1"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseAddrSpec(tc.spec)
			if err == nil {
				t.Fatalf("ParseAddrSpec(%q) succeeded, want error", tc.spec)
			}
			if !errors.Is(err, ErrInvalidAddrSpec) {
				t.Errorf("errors.Is(err, ErrInvalidAddrSpec) = false for %q", tc.spec)
			}
			var specErr *AddrSpecError
			if !errors.As(err, &specErr) {
				t.Fatalf("errors.As(err, *AddrSpecError) failed for %q", tc.spec)
			}
			if specErr.Spec != tc.spec {
				t.Errorf("Spec = %q, want %q", specErr.Spec, tc.spec)
			}
		})
	}
}

func TestParseAddrSpecRefusalReasons(t *testing.T) {
	cases := []struct {
		spec       string
		wantReason string
	}{
		{"eth0", "not an IP address or CIDR block"},
		{"192.168.1.0/abc", "prefix length is not a number"},
		{"192.168.1.0/33", "prefix length is too long for the address family"},
	}

	for _, tc := range cases {
		_, err := ParseAddrSpec(tc.spec)
		var specErr *AddrSpecError
		if !errors.As(err, &specErr) {
			t.Fatalf("errors.As failed for %q", tc.spec)
		}
		if specErr.Reason != tc.wantReason {
			t.Errorf("Reason for %q = %q, want %q", tc.spec, specErr.Reason, tc.wantReason)
		}
	}
}
