// Package unixprobe names every syscall wrapper the design depends on, so that
// one going missing or changing shape is a build failure here rather than a
// surprise in the phase that needs it.
//
// Nothing calls it. Its whole job is to be compiled, for every architecture
// that ships.
package unixprobe
