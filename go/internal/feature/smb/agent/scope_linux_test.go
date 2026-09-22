//go:build linux

package agent

import (
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func addrs(t *testing.T, in ...string) []netip.Addr {
	t.Helper()
	out := make([]netip.Addr, 0, len(in))
	for _, s := range in {
		a, err := netip.ParseAddr(s)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, a)
	}
	return out
}

func hosts(s Scope) []string  { return strings.Fields(s.HostsAllow) }
func ifaces(s Scope) []string { return strings.Fields(s.Interfaces) }

func has(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// The admission line receives the block enclosing an address, never the address
// itself. A client arrives from somewhere on that network rather than from the
// server's own address, so admitting only the address admits nobody.
func TestAdmissionIsTheEnclosingBlockNotTheAddress(t *testing.T) {
	scope := Compute([]Device{
		{Name: "eth0", Addrs: addrs(t, "192.168.1.10")},
	}, false)

	if !scope.Detected {
		t.Fatal("a private address on a real device was not detected")
	}
	if !has(hosts(scope), "192.168.0.0/16") {
		t.Errorf("the enclosing block is missing: %q", scope.HostsAllow)
	}
	if has(hosts(scope), "192.168.1.10") {
		t.Errorf("the address itself was admitted, which admits no client: %q", scope.HostsAllow)
	}
	// The device name is preferred over the address, because the name survives
	// a lease change.
	if !has(ifaces(scope), "eth0") {
		t.Errorf("the device was not bound: %q", scope.Interfaces)
	}
}

// Inside a container namespace the only visible subnet is the bridge's, while
// clients arrive translated carrying their own addresses. Private space is
// admitted wholesale rather than refusing them.
func TestAVethNamespaceAdmitsPrivateSpaceWholesale(t *testing.T) {
	scope := Compute([]Device{
		{Name: "eth0", Addrs: addrs(t, "172.17.0.2"), Veth: true},
	}, false)

	for _, block := range []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"} {
		if !has(hosts(scope), block) {
			t.Errorf("%s is not admitted inside a container namespace: %q", block, scope.HostsAllow)
		}
	}
	// Binding is unaffected: only the virtual device exists to bind.
	if !has(ifaces(scope), "eth0") {
		t.Errorf("the virtual device was not bound: %q", scope.Interfaces)
	}
}

// One real device alongside a virtual one is not a container namespace, so the
// wholesale admission does not apply.
func TestOneRealDeviceIsNotAVethNamespace(t *testing.T) {
	scope := Compute([]Device{
		{Name: "eth0", Addrs: addrs(t, "192.168.1.10")},
		{Name: "veth0", Addrs: addrs(t, "172.17.0.2"), Veth: true},
	}, false)

	if has(hosts(scope), "10.0.0.0/8") {
		t.Errorf("private space was admitted wholesale outside a container namespace: %q", scope.HostsAllow)
	}
}

// A globally routable address stays out until the operator opts in, and the
// opt-in says what it means rather than naming a subnet that would understate
// it.
func TestAPublicAddressNeedsTheOptIn(t *testing.T) {
	devices := []Device{{Name: "eth0", Addrs: addrs(t, "203.0.113.7")}}

	closed := Compute(devices, false)
	if closed.Detected {
		t.Errorf("a public address was bound without the opt-in: %+v", closed)
	}
	if has(hosts(closed), "ALL") {
		t.Errorf("a public address was admitted without the opt-in: %q", closed.HostsAllow)
	}

	opted := Compute(devices, true)
	if !opted.Detected {
		t.Error("the opt-in did not admit the public address")
	}
	if !has(hosts(opted), "ALL") {
		t.Errorf("the opt-in did not say what it means: %q", opted.HostsAllow)
	}
}

// A device carrying both a private and a public address, without the opt-in,
// binds the private address rather than the device name: naming the device
// would bind the public one too.
func TestAMixedDeviceBindsTheAddressNotTheName(t *testing.T) {
	scope := Compute([]Device{
		{Name: "eth0", Addrs: addrs(t, "192.168.1.10", "203.0.113.7")},
	}, false)

	if has(ifaces(scope), "eth0") {
		t.Errorf("the device name was bound, which binds the public address too: %q", scope.Interfaces)
	}
	if !has(ifaces(scope), "192.168.1.10") {
		t.Errorf("the private address was not bound: %q", scope.Interfaces)
	}
	if has(hosts(scope), "ALL") {
		t.Errorf("the public address was admitted: %q", scope.HostsAllow)
	}
}

// A device whose every address is link-local is not a network anyone reaches
// this server on. Under host networking every container that starts brings one
// up, and binding it moves the bind line, which costs a restart.
func TestALinkLocalOnlyDeviceIsNotBound(t *testing.T) {
	scope := Compute([]Device{
		{Name: "veth9a2", Addrs: addrs(t, "fe80::1")},
		{Name: "docker0", Addrs: addrs(t, "169.254.1.1")},
	}, false)

	if scope.Detected {
		t.Errorf("a link-local-only machine reported a detected scope: %+v", scope)
	}
	for _, name := range []string{"veth9a2", "docker0"} {
		if has(ifaces(scope), name) {
			t.Errorf("%s was bound: %q", name, scope.Interfaces)
		}
	}
}

// A real interface carrying a link-local address beside its own is unaffected.
func TestARealDeviceWithALinkLocalAddressIsStillBound(t *testing.T) {
	scope := Compute([]Device{
		{Name: "eth0", Addrs: addrs(t, "fe80::1", "192.168.1.10")},
	}, false)

	if !scope.Detected {
		t.Fatal("a real device was dropped for carrying a link-local address")
	}
	if !has(ifaces(scope), "eth0") {
		t.Errorf("the device was not bound: %q", scope.Interfaces)
	}
}

// Nothing to bind still promotes the closed configuration, which is safe, and
// still reports, because it is never what the operator wanted.
func TestNothingToBindIsReportedRatherThanSilent(t *testing.T) {
	scope := Compute(nil, false)

	if scope.Detected {
		t.Error("an empty machine reported a detected scope")
	}
	// The closed configuration is still what gets promoted.
	if !has(ifaces(scope), "lo") {
		t.Errorf("loopback is missing from the closed scope: %q", scope.Interfaces)
	}
	if !has(hosts(scope), "127.0.0.0/8") || !has(hosts(scope), "::1/128") {
		t.Errorf("the closed admission line is wrong: %q", scope.HostsAllow)
	}
}

// The same devices in any order render the same lines, or an unchanged
// republish reads as a changed bind line and costs a restart.
func TestTheScopeIsOrderIndependent(t *testing.T) {
	a := []Device{
		{Name: "eth0", Addrs: addrs(t, "192.168.1.10")},
		{Name: "eth1", Addrs: addrs(t, "10.1.2.3")},
		{Name: "wlan0", Addrs: addrs(t, "172.16.5.5")},
	}
	b := []Device{a[2], a[0], a[1]}

	first, second := Compute(a, false), Compute(b, false)
	if first.Interfaces != second.Interfaces {
		t.Errorf("the bind line depends on device order:\n%q\n%q", first.Interfaces, second.Interfaces)
	}
	if first.HostsAllow != second.HostsAllow {
		t.Errorf("the admission line depends on device order:\n%q\n%q", first.HostsAllow, second.HostsAllow)
	}
}

// A mapped address describes an IPv4 network wearing an IPv6 spelling, and
// classifying it as IPv6 would put a private network outside every private
// block.
func TestAMappedAddressIsJudgedAsTheFamilyItIs(t *testing.T) {
	mapped := netip.AddrFrom16(netip.MustParseAddr("::ffff:192.168.1.10").As16())
	scope := Compute([]Device{{Name: "eth0", Addrs: []netip.Addr{mapped}}}, false)

	if !scope.Detected {
		t.Fatal("a mapped private address was not detected")
	}
	if !has(hosts(scope), "192.168.0.0/16") {
		t.Errorf("a mapped address was not judged as IPv4: %q", scope.HostsAllow)
	}
}

// Loopback leads both lines, so a local listing and the health check keep
// working whatever follows.
func TestLoopbackLeadsBothLines(t *testing.T) {
	scope := Compute([]Device{{Name: "eth0", Addrs: addrs(t, "192.168.1.10")}}, false)

	if got := ifaces(scope); len(got) == 0 || got[0] != "lo" {
		t.Errorf("loopback does not lead the bind line: %q", scope.Interfaces)
	}
	if got := hosts(scope); len(got) == 0 || got[0] != "127.0.0.0/8" {
		t.Errorf("loopback does not lead the admission line: %q", scope.HostsAllow)
	}
}

// A missing or unreadable policy file reads closed. Neither flag is assumed
// permissive because a file did not arrive.
func TestAMissingPolicyReadsClosed(t *testing.T) {
	got := ReadPolicy(filepath.Join(t.TempDir(), "absent.policy"))
	if got.AllowPublicBind || got.PinnedInterfaces {
		t.Errorf("a missing policy read as %+v, want both flags off", got)
	}

	// A directory where a file belongs is the unreadable case.
	dir := filepath.Join(t.TempDir(), "network.policy")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := ReadPolicy(dir); got.AllowPublicBind || got.PinnedInterfaces {
		t.Errorf("an unreadable policy read as %+v, want both flags off", got)
	}
}

func TestThePolicyFlagsAreRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "network.policy")
	if err := os.WriteFile(path, []byte("allow_public_bind=1\npinned_interfaces=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := ReadPolicy(path)
	if !got.AllowPublicBind || !got.PinnedInterfaces {
		t.Errorf("the flags read as %+v", got)
	}

	// A value that is not the exact flag is not the flag.
	if err := os.WriteFile(path, []byte("allow_public_bind=0\nallow_public_bind = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ReadPolicy(path); got.AllowPublicBind {
		t.Errorf("a near-miss line set the flag: %+v", got)
	}
}
