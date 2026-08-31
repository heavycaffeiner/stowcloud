package index

import "sort"

// Tree order compares component by component, keeping everything beneath one
// directory contiguous.
//
// A plain byte comparison gets this subtly wrong. Since '.' is 0x2E and '/' is
// 0x2F, "a.txt" falls between "a" and "a/b", scattering siblings and costing
// real compression. Mapping the separator below every other byte corrects it.
//
// This is not cosmetic. Block compression is the entire reason the index stays
// small, and it only pays off when adjacent names share a prefix.

// TreeCompare compares two paths under tree order.
func TreeCompare(a, b string) int {
	// The key widens to int rather than staying a byte: shifting every
	// non-separator up by one is what puts '/' below them, and in byte
	// arithmetic 0xff has nowhere to go, so it and 0xfe would collide and make
	// two different paths compare equal.
	key := func(c byte) int {
		if c == '/' {
			return 0
		}
		return int(c) + 1
	}
	n := min(len(a), len(b))
	for i := range n {
		ka, kb := key(a[i]), key(b[i])
		if ka != kb {
			if ka < kb {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(a) < len(b):
		return -1
	case len(a) > len(b):
		return 1
	}
	return 0
}

// TreeOrder arranges entries as a base segment requires, by share first and then
// by path in tree order.
func TreeOrder(entries []Entry) {
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Share != entries[j].Share {
			return entries[i].Share < entries[j].Share
		}
		return TreeCompare(entries[i].Path, entries[j].Path) < 0
	})
}
