package index

import (
	"fmt"
	"testing"
)

// What a merge costs, and what a query pays while one runs.
//
// A merge reads every entry in the base segment, sorts, re-compresses and
// writes a new one. It used to hold the write lock for all of that. The numbers
// below are why it no longer does, measured on a 13th-generation mobile i5:
//
//	                        merge      query
//	10,000 entries          37 ms      16 us
//	100,000 entries        386 ms      21 us
//	1,000,000 entries      4.4 s       87 us
//
// A merge on a million entries is fifty thousand queries' worth of work, and
// the updater's timer decides when it happens rather than an administrator. So
// the same run, with a query loop against it:
//
//	                       queries completed   worst query
//	lock across the build          3             3.58 s
//	lock for the swap only        44,378         3.1 ms
//
// These are benchmarks rather than tests: there is no threshold here that a
// machine-independent assertion could hold. What they produce is the number
// the decision needs, and the decision is in Merge.

// corpus builds a realistic-looking tree: a few files per directory, nested,
// with names that share prefixes the way real ones do.
func corpus(n int) []Entry {
	out := make([]Entry, 0, n)
	for i := range n {
		// Three levels, so the tree order has something to do and the block
		// compression has shared prefixes to find.
		// The share is one of four, taken through a signed intermediate so the
		// conversion has a bound the compiler can see rather than an exception.
		share := i % 4
		out = append(out, Entry{
			Share: uint32(share),
			Path: fmt.Sprintf("dept-%02d/project-%03d/report-%04d.txt",
				i%16, (i/16)%512, i),
		})
	}
	return out
}

// BenchmarkMerge is the whole rebuild, at four corpus sizes.
//
// The sizes bracket what a deployment plausibly holds: ten thousand is a home
// share, a million is the ceiling a build stops at.
func BenchmarkMerge(b *testing.B) {
	for _, size := range []int{10_000, 100_000, 1_000_000} {
		b.Run(fmt.Sprintf("%d-entries", size), func(b *testing.B) {
			entries := corpus(size)
			for b.Loop() {
				b.StopTimer()
				ix, err := Open(b.TempDir(), DefaultConfig())
				if err != nil {
					b.Fatal(err)
				}
				if aerr := ix.Append(entries); aerr != nil {
					b.Fatal(aerr)
				}
				b.StartTimer()

				if merr := ix.Merge(b.Context(), nil); merr != nil {
					b.Fatal(merr)
				}
			}
		})
	}
}

// BenchmarkQueryDuringSteadyState is what a query costs with the overlay at the
// size the merge gate allows.
//
// The comparison that matters is against the merge above: if a merge is a
// second and a query is a millisecond, the merge is a thousand queries' worth
// of pause, and that is the number that decides whether the write lock is
// acceptable.
func BenchmarkQuery(b *testing.B) {
	for _, size := range []int{10_000, 100_000, 1_000_000} {
		b.Run(fmt.Sprintf("%d-entries", size), func(b *testing.B) {
			ix, err := Open(b.TempDir(), DefaultConfig())
			if err != nil {
				b.Fatal(err)
			}
			if aerr := ix.Append(corpus(size)); aerr != nil {
				b.Fatal(aerr)
			}
			if merr := ix.Merge(b.Context(), nil); merr != nil {
				b.Fatal(merr)
			}

			b.ResetTimer()
			for b.Loop() {
				if _, qerr := ix.Query([]byte("report-0042"), 50); qerr != nil {
					b.Fatal(qerr)
				}
			}
		})
	}
}

// BenchmarkChildrenOf is what one incremental update's read costs.
//
// It runs on every watcher event, so it is the one on this path that happens
// constantly rather than occasionally. A linear scan of the base segment would
// show up here as a number that grows with the corpus.
func BenchmarkChildrenOf(b *testing.B) {
	for _, size := range []int{10_000, 100_000, 1_000_000} {
		b.Run(fmt.Sprintf("%d-entries", size), func(b *testing.B) {
			ix, err := Open(b.TempDir(), DefaultConfig())
			if err != nil {
				b.Fatal(err)
			}
			if aerr := ix.Append(corpus(size)); aerr != nil {
				b.Fatal(aerr)
			}
			if merr := ix.Merge(b.Context(), nil); merr != nil {
				b.Fatal(merr)
			}

			b.ResetTimer()
			for b.Loop() {
				if _, cerr := ix.ChildrenOf(1, "dept-01/project-001"); cerr != nil {
					b.Fatal(cerr)
				}
			}
		})
	}
}
