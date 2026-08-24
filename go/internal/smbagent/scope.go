// Package smbagent is the privileged half of SMB publishing.
//
// The server renders the configuration, the credential file and the account
// file into a shared directory as an unprivileged user, and renders the closed
// case for the network: loopback and nothing else. It has to, because it sits
// in a different network namespace and cannot see the host's devices.
//
// This runs in the namespace that can. It reads what the server rendered,
// decides which addresses may be bound, validates the result, promotes it,
// imports the credentials and signals the daemon.
package smbagent

import (
	"net/netip"
	"os"
	"strings"

	"github.com/heavycaffeiner/stowcloud/go/internal/smb"
)

// Device is one network device and the addresses configured on it.
type Device struct {
	Name  string
	Addrs []netip.Addr
	// Veth marks one end of a virtual pair, which means this process is inside
	// a container's network namespace and the address describes the bridge
	// rather than any network a client arrives from.
	Veth bool
}

// Scope is the two lines that replace what the server rendered closed.
type Scope struct {
	Interfaces string
	HostsAllow string
	// Detected is false when nothing was found to bind. The configuration is
	// still the closed one, which is safe, and it is never what the operator
	// wanted, so the caller reports it rather than promoting it in silence.
	Detected bool
}

// Compute folds devices into the two lines.
//
// The rule: a network this machine is attached to is admitted with no
// configuration. The bind line gets the device, and the admission line gets the
// well-known private block enclosing its address.
//
// A globally routable address is left out unless the operator opted in, and
// none of this runs at all when they pinned the addresses themselves, because
// then the server already rendered the final answer.
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

	detected := false
	for _, dev := range devices {
		var accepted []string
		var blocks []string
		rejected := false

		for _, ip := range dev.Addrs {
			block := smb.EnclosingPrivateRange(ip)
			switch {
			case block != "":
				accepted = append(accepted, ip.String())
				blocks = append(blocks, block)
			case allowPublic:
				// The opt-in means "SMB is on the internet". Naming a subnet
				// here would understate that.
				accepted = append(accepted, ip.String())
				blocks = append(blocks, "ALL")
			default:
				rejected = true
			}
		}

		// A device whose every address is link-local is not a network anyone
		// reaches this server on: a client would need a scope identifier to
		// use one, and nothing routes there. Binding it buys nothing and costs
		// real service, because on host networking every container that starts
		// brings up a virtual device carrying exactly one such address, which
		// moves the bind line, which the daemon can only apply by restarting,
		// which drops whatever transfer was in flight.
		//
		// A real interface is unaffected: it carries its own address beside
		// the link-local one.
		if len(accepted) == 0 || allLinkLocal(blocks) {
			continue
		}
		for _, b := range blocks {
			addHost(b)
		}
		detected = true

		// The device name survives a lease change and an individual address
		// does not, so the name is preferred. The addresses are used only when
		// the device also carries one being refused, because naming the device
		// there would bind that one too.
		if rejected {
			ifaces = append(ifaces, accepted...)
		} else {
			ifaces = append(ifaces, dev.Name)
		}
	}

	// Inside a container namespace the only subnet visible is the bridge's,
	// and clients on the LAN arrive through address translation carrying their
	// own addresses, which that subnet does not cover. Detection cannot see
	// their networks from here, so private space is admitted wholesale rather
	// than those clients being refused. Binding is unaffected: only the
	// virtual device exists to bind.
	if len(devices) > 0 && allVeth(devices) {
		for _, b := range smb.PrivateCIDRs() {
			addHost(b)
		}
	}

	return Scope{
		Interfaces: strings.Join(ifaces, " "),
		HostsAllow: strings.Join(hosts, " "),
		Detected:   detected,
	}
}

// isLinkLocal reports a block reachable only from the same link, and only by a
// client that names the scope. Not a network this server is on.
func isLinkLocal(block string) bool {
	return block == "fe80::/10" || block == "169.254.0.0/16"
}

func allLinkLocal(blocks []string) bool {
	for _, b := range blocks {
		if !isLinkLocal(b) {
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

// isVeth reports whether a device is one end of a virtual pair.
//
// A pair's two ends point at each other, so the two indices differ; a real
// interface, a bridge and a tunnel all point at themselves. Unreadable means
// "not one" rather than an error: this only decides how wide the admission
// line gets, and the narrow answer is the safe one.
func isVeth(name string) bool {
	link, lerr := os.ReadFile("/sys/class/net/" + name + "/iflink")   //nolint:gosec // G304 reads the variable: the name came from the kernel's own device list.
	index, ierr := os.ReadFile("/sys/class/net/" + name + "/ifindex") //nolint:gosec // G304 reads the variable: the same.
	if lerr != nil || ierr != nil {
		return false
	}
	return strings.TrimSpace(string(link)) != strings.TrimSpace(string(index))
}
