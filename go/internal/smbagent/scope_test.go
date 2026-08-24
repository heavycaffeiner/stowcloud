package smbagent

import (
	"net/netip"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/internal/smb"
)

// Who can reach SMB is decided here, so every rule has a test and every test
// says which rule it is. These are ported from the implementation this
// replaced, because each one records something that went wrong once.

func dev(name string, veth bool, addrs ...string) Device {
	d := Device{Name: name, Veth: veth}
	for _, a := range addrs {
		d.Addrs = append(d.Addrs, netip.MustParseAddr(a))
	}
	return d
}

// Nothing found is the closed case, and it says so rather than looking like a
// decision. The configuration is safe either way; it is never what the
// operator wanted, so the caller has to report it.
func TestNothingFoundIsTheClosedCaseAndSaysSo(t *testing.T) {
	s := Compute(nil, false)
	if s.Interfaces != "lo" {
		t.Errorf("interfaces = %q, want loopback only", s.Interfaces)
	}
	if s.HostsAllow != "127.0.0.0/8 ::1/128" {
		t.Errorf("hosts allow = %q, want loopback only", s.HostsAllow)
	}
	if s.Detected {
		t.Error("an empty scan reported as detected, which reads as a decision")
	}
}

func TestALanDeviceIsNamedAndItsBlockAdmitted(t *testing.T) {
	s := Compute([]Device{dev("eth0", false, "192.168.1.10")}, false)
	if s.Interfaces != "lo eth0" {
		t.Errorf("interfaces = %q", s.Interfaces)
	}
	if !strings.Contains(s.HostsAllow, "192.168.0.0/16") {
		t.Errorf("hosts allow = %q, missing the enclosing block", s.HostsAllow)
	}
	if !s.Detected {
		t.Error("a real device was not reported as detected")
	}
}

// The enclosing block rather than the address: an internal network is
// routinely several subnets behind a router, and a tailnet address carries a
// single-host prefix whose own subnet admits nobody.
func TestAdmissionIsTheEnclosingBlockNotTheAddress(t *testing.T) {
	s := Compute([]Device{dev("tailscale0", false, "100.90.1.1")}, false)
	if !strings.Contains(s.HostsAllow, "100.64.0.0/10") {
		t.Fatalf("hosts allow = %q, want the block that admits the rest of the tailnet", s.HostsAllow)
	}
}

// A globally routable address is left out unless the operator opted in.
func TestAPublicAddressIsLeftOutWithoutTheOptIn(t *testing.T) {
	s := Compute([]Device{dev("eth0", false, "203.0.113.5")}, false)
	if s.Interfaces != "lo" {
		t.Errorf("interfaces = %q, want the public address left out", s.Interfaces)
	}
	if s.Detected {
		t.Error("a device with nothing admissible reported as detected")
	}
	if strings.Contains(s.HostsAllow, "ALL") {
		t.Error("the admission line was widened without the opt-in")
	}
}

// The opt-in means SMB is on the internet. Naming a subnet would understate
// that, so the admission is the widest value the format has.
func TestTheOptInTakesThePublicAddressAndSaysEverything(t *testing.T) {
	s := Compute([]Device{dev("eth0", false, "203.0.113.5")}, true)
	if s.Interfaces != "lo eth0" {
		t.Errorf("interfaces = %q", s.Interfaces)
	}
	if !strings.Contains(s.HostsAllow, "ALL") {
		t.Errorf("hosts allow = %q, want the widest value", s.HostsAllow)
	}
	if !s.Detected {
		t.Error("the opt-in did not report a detection")
	}
}

// A device carrying both a private and a public address falls back to naming
// the addresses, because naming the device would bind the public one too.
func TestAMixedDeviceFallsBackToItsPrivateAddresses(t *testing.T) {
	s := Compute([]Device{dev("eth0", false, "192.168.1.10", "203.0.113.5")}, false)
	if s.Interfaces != "lo 192.168.1.10" {
		t.Fatalf("interfaces = %q, want the private address rather than the device", s.Interfaces)
	}
	if !s.Detected {
		t.Error("a mixed device was not reported as detected")
	}
}

// Inside a container namespace the only subnet visible is the bridge's, and
// clients arrive through address translation carrying their own addresses,
// which that subnet does not cover. Private space is admitted wholesale rather
// than those clients being refused.
func TestAContainerNamespaceAdmitsPrivateSpaceWholesale(t *testing.T) {
	s := Compute([]Device{dev("eth0", true, "172.18.0.2")}, false)
	if s.Interfaces != "lo eth0" {
		t.Errorf("interfaces = %q", s.Interfaces)
	}
	for _, block := range smb.PrivateCIDRs() {
		if !strings.Contains(s.HostsAllow, block) {
			t.Errorf("hosts allow is missing %s: %q", block, s.HostsAllow)
		}
	}
}

// Host networking does not widen. The wholesale admission is for the case
// where nothing but a bridge is visible, and a real interface means the scan
// can see the networks it needs to.
func TestHostNetworkingDoesNotWiden(t *testing.T) {
	s := Compute([]Device{
		dev("eth0", false, "192.168.1.10"),
		dev("veth123", true, "172.18.0.2"),
	}, false)
	if strings.Contains(s.HostsAllow, "10.0.0.0/8") {
		t.Fatalf("hosts allow = %q, widened with a real interface present", s.HostsAllow)
	}
}

// A device whose every address is link-local is not bound.
//
// Nothing routes there and a client would need a scope identifier to use one.
// Binding costs real service: on host networking every container that starts
// brings up a virtual device carrying exactly one such address, which moves
// the bind line, which the daemon can only apply by restarting, which drops
// whatever transfer was in flight.
func TestALinkLocalOnlyDeviceIsNotBound(t *testing.T) {
	s := Compute([]Device{
		dev("enp6s0", false, "192.168.1.10", "fe80::1"),
		dev("veth9f2", false, "fe80::2"),
	}, false)

	if s.Interfaces != "lo enp6s0" {
		t.Fatalf("interfaces = %q, want the link-local-only device left out", s.Interfaces)
	}
	// The real interface's own link-local block is still admitted, because it
	// carries a routable address beside it.
	if !strings.Contains(s.HostsAllow, "192.168.0.0/16") {
		t.Errorf("hosts allow is missing the real network: %q", s.HostsAllow)
	}
	if !strings.Contains(s.HostsAllow, "fe80::/10") {
		t.Errorf("hosts allow is missing the real device's link-local block: %q", s.HostsAllow)
	}
}

// Loopback leads the bind line either way, so a local listing and the health
// check keep working whichever list follows it.
func TestLoopbackLeadsWhateverFollows(t *testing.T) {
	for _, devices := range [][]Device{
		nil,
		{dev("eth0", false, "192.168.1.10")},
		{dev("eth0", true, "172.18.0.2")},
	} {
		s := Compute(devices, false)
		if !strings.HasPrefix(s.Interfaces, "lo") {
			t.Errorf("interfaces = %q, want loopback first", s.Interfaces)
		}
		if !strings.HasPrefix(s.HostsAllow, "127.0.0.0/8") {
			t.Errorf("hosts allow = %q, want loopback first", s.HostsAllow)
		}
	}
}

// The same devices produce the same lines, so a republish that changed nothing
// is byte identical and a diff shows only real changes.
func TestTheScopeIsDeterministic(t *testing.T) {
	devices := []Device{
		dev("eth0", false, "192.168.1.10"),
		dev("wg0", false, "100.90.1.1"),
		dev("br0", false, "10.1.2.3"),
	}
	first := Compute(devices, false)
	for range 8 {
		if got := Compute(devices, false); got != first {
			t.Fatal("the same devices produced different lines")
		}
	}
}
