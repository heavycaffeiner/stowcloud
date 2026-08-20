package index

import "sort"

// Tree order: component by component, so everything under one directory is
// contiguous.
//
// A plain byte comparison gets this subtly wrong. '.' is 0x2E and '/' is 0x2F,
// so "a.txt" lands between "a" and "a/b", which scatters siblings and costs
// real compression. Mapping the separator below every other byte fixes it.
//
// This is not cosmetic. Block compression is the whole reason the index is
// small, and it only pays when adjacent names share a prefix.

// TreeCompare orders two paths in tree order.
func TreeCompare(a, b string) int {
	key := func(c byte) byte {
		if c == '/' {
			return 0
		}
		if c == 0xff {
			return 0xff
		}
		return c + 1
	}
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
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

// TreeOrder sorts entries into the order a base segment requires: by share,
// then by path in tree order.
func TreeOrder(entries []Entry) {
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Share != entries[j].Share {
			return entries[i].Share < entries[j].Share
		}
		return TreeCompare(entries[i].Path, entries[j].Path) < 0
	})
}
