// Package unixprobe names every syscall wrapper the rest of the design
// depends on, so that one going missing or changing shape upstream is a
// build failure here instead of a surprise in the phase that needed it.
//
// Nothing in this tree calls it. Its only job is to compile, on every
// architecture the product ships.
package unixprobe
