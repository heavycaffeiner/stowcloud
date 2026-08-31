//go:build linux

package agent

import (
	"net/netip"
	"os"
	"sort"
	"strings"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/netzone"
)

// Deciding which addresses the daemon may bind.
//
// The server renders the closed case, loopback alone, because it sits in a
// different network namespace and cannot see this machine's devices. The agent
// runs where they are visible, and its task is to widen that closed rendering to
// exactly the networks this machine is attached to and no further.
//
// Devices() is the only call that asks the operating system anything. Compute is
// a pure fold over what it returned, so the decision governing who can reach SMB
// is testable on any machine.

// Device is one network device together with the addresses configured on it.
type Device struct {
	Name  string
	Addrs []netip.Addr

	// Veth marks one end of a virtual pair, which means this process sits
	// inside a container's network namespace and the address describes the
	// bridge rather than any network a client arrives from.
	Veth bool
}

// Scope is the pair of lines that replace what the server rendered closed.
type Scope struct {
	Interfaces string
	HostsAllow string

	// Detected is false when nothing was found to bind. The configuration
	// remains the closed one, which is safe, and it is never what the operator
	// intended, so the caller reports it rather than promoting it in silence.
	Detected bool
}

// Compute reduces a device list to the two configuration lines.
//
// A network this machine is attached to is admitted without configuration. The
// bind line receives the device, while the admission line receives the
// well-known private block enclosing its address rather than the address
// itself, because a client arrives from somewhere on that network rather than
// from the server's own address.
//
// Globally routable addresses stay out unless the operator opted in. None of
// this runs when they pinned the addresses themselves, since the server then
// rendered the final answer already.
func Compute(devices []Device, allowPublic bool) Scope {
	ifaces := []string{"lo"}
	hosts := []string{"127.0.0.0/8", "::1/128"}

	addHost := func(block string) {
		for _, h := range hosts {
			if h == block {
				return
			}
		}
		hosts = append(hosts, block)
	}

	// Sorted, so one machine renders one file whatever order the kernel listed
	// its devices in. An unchanged republish that reordered the bind line would
	// read as a changed one, and a changed bind line costs a restart.
	ordered := make([]Device, len(devices))
	copy(ordered, devices)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Name < ordered[j].Name })

	detected := false
	for _, dev := range ordered {
		accepted, blocks, rejected := classify(dev, allowPublic)

		// Devices carrying only link-local addresses are not networks anyone
		// reaches this server through: using one requires a scope identifier
		// and nothing routes there. Binding such a device gains nothing while
		// costing real service, since under host networking each container
		// that starts raises a virtual device holding exactly one such address.
		// That shifts the bind line, which the daemon applies only by
		// restarting, dropping whatever transfer was in progress.
		//
		// A real interface is unaffected, carrying its own address beside the
		// link-local one.
		if len(accepted) == 0 || allLinkLocal(blocks) {
			continue
		}
		for _, b := range blocks {
			addHost(b)
		}
		detected = true

		// A device name survives a lease change where an individual address
		// does not, so the name is preferred. Addresses are used only when the
		// device also carries one being refused, since naming the device there
		// would bind that address too.
		if rejected {
			ifaces = append(ifaces, accepted...)
		} else {
			ifaces = append(ifaces, dev.Name)
		}
	}

	// Within a container namespace the only visible subnet is the bridge's, and
	// clients on the LAN arrive through address translation carrying their own
	// addresses, which that subnet does not cover. Detection cannot see their
	// networks from here, so private space is admitted wholesale rather than
	// refusing those clients. Binding is unaffected, since only the virtual
	// device exists to bind.
	if len(ordered) > 0 && allVeth(ordered) {
		for _, b := range netzone.PrivateCIDRs() {
			addHost(b)
		}
	}

	return Scope{
		Interfaces: strings.Join(ifaces, " "),
		HostsAllow: strings.Join(hosts, " "),
		Detected:   detected,
	}
}

// classify sorts one device's addresses into what is admitted, which blocks
// admit them, and whether anything was refused.
func classify(dev Device, allowPublic bool) (accepted, blocks []string, rejected bool) {
	for _, ip := range dev.Addrs {
		// Judged as the family it actually is. A mapped address describes an
		// IPv4 network wearing an IPv6 spelling, and classifying it as IPv6
		// would put a private network outside every private block.
		block := netzone.EnclosingPrivateRange(ip.Unmap())
		switch {
		case block != "":
			accepted = append(accepted, ip.String())
			blocks = append(blocks, block)
		case allowPublic:
			// The opt-in means SMB is on the internet. Naming a subnet here
			// would understate that.
			accepted = append(accepted, ip.String())
			blocks = append(blocks, "ALL")
		default:
			rejected = true
		}
	}
	return accepted, blocks, rejected
}

// linkLocal reports a block reachable only from the same link, and only by a
// client naming the scope. Not a network this server is on.
func linkLocal(block string) bool {
	return block == "fe80::/10" || block == "169.254.0.0/16"
}

func allLinkLocal(blocks []string) bool {
	for _, b := range blocks {
		if !linkLocal(b) {
			return false
		}
	}
	return true
}

func allVeth(devices []Device) bool {
	for _, d := range devices {
		if !d.Veth {
			return false
		}
	}
	return true
}

// veth reports whether a device is one end of a virtual pair.
//
// The two ends of a pair point at each other, so their indices differ, while a
// real interface, a bridge and a tunnel all point at themselves. Unreadable
// counts as not one rather than as an error: this decides only how wide the
// admission line becomes, and the narrow answer is the safe one.
func veth(name string) bool {
	link, lerr := os.ReadFile("/sys/class/net/" + name + "/iflink")   //nolint:gosec // G304: the name came from the kernel's own device list.
	index, ierr := os.ReadFile("/sys/class/net/" + name + "/ifindex") //nolint:gosec // G304: as above.
	if lerr != nil || ierr != nil {
		return false
	}
	return strings.TrimSpace(string(link)) != strings.TrimSpace(string(index))
}
