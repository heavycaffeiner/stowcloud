//go:build !linux

package main

import "io"

// printCaps has nothing to probe off Linux. It refuses rather than printing an
// empty report, because a report full of "unavailable" reads like a broken
// kernel instead of a host this product does not run on.
func printCaps(w io.Writer) int {
	say(w, "stowcloud: the capability probe reads Linux kernel interfaces and this is not Linux\n")
	return exitNoAnswer
}
