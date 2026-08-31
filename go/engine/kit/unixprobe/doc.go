// Package unixprobe names every syscall wrapper the rest of the design
// depends on, so that one going missing or changing shape upstream is a
// build failure here instead of a surprise in the phase that needed it.
//
// Nothing in this tree calls it. Its only job is to compile, on every
// architecture the product ships.
//
// It carries no test on purpose. The assertion is the compilation: a test
// could only call the two functions and look at values it already knows,
// which asserts nothing the build has not already proved.
package unixprobe
