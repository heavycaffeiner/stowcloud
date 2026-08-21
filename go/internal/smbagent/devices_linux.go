//go:build linux

package smbagent

import (
	"fmt"
	"net"
	"net/netip"
	"sort"
)

// Reading the machine's own devices.
//
// This used to shell out to a command and swallow the failure. On an image
// without that command the scan came back empty, the closed configuration
// survived into the promoted file, validation accepted it, and SMB answered on
// loopback and nowhere else with nothing logged anywhere. Reading the
// interfaces directly removes the dependency, and an empty answer is something
// the caller has to report rather than something that looks like a decision.

// Devices returns every device that is up, is not loopback, and carries at
// least one address.
//
// It is the only part of this file that asks the operating system anything.
// What it returns is folded by Compute, which is a pure function, so the
// decision that determines who can reach SMB is testable on any machine rather
// than only on one with the right interfaces.
func Devices() ([]Device, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("reading this machine's network devices: %w", err)
	}

	var out []Device
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, aerr := ifc.Addrs()
		if aerr != nil {
			// One device that cannot be read is not a reason to report none:
			// the others are still what this machine is attached to.
			continue
		}

		var parsed []netip.Addr
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip, ok := netip.AddrFromSlice(ipnet.IP)
			if !ok {
				continue
			}
			// An address of one family carried inside the other is judged as
			// the family it actually is, the same rule the classifier uses.
			parsed = append(parsed, ip.Unmap())
		}
		if len(parsed) == 0 {
			continue
		}
		out = append(out, Device{Name: ifc.Name, Addrs: parsed, Veth: isVeth(ifc.Name)})
	}

	// Sorted, so the same machine renders the same file and a republish that
	// changed nothing is byte identical.
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Detect reads the devices and folds them.
//
// An error is the caller's to report: it means this machine's own interfaces
// could not be read, which is not a reason to promote a configuration that
// says loopback and call it a scope.
func Detect(allowPublic bool) (Scope, error) {
	devices, err := Devices()
	if err != nil {
		return Scope{}, err
	}
	return Compute(devices, allowPublic), nil
}
