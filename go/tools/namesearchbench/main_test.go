//go:build linux

package main

import (
	"slices"
	"testing"

	"github.com/stowcloud/namesearch/index"
)

func TestGenerateIsDeterministic(t *testing.T) {
	shape := Shape{Count: 100, Depth: 3, Fanout: 7, Namespaces: 3}
	a, aq := Generate(42, shape)
	b, bq := Generate(42, shape)
	if !slices.Equal(a, b) || !slices.Equal(aq[0].Exact, bq[0].Exact) || ContentHash(a) != ContentHash(b) {
		t.Fatal("same seed and shape produced different corpus or queries")
	}
	c, _ := Generate(43, shape)
	if ContentHash(a) == ContentHash(c) {
		t.Fatal("different seeds produced the same content hash")
	}
}

func TestQueryKindsHaveExpectedCorpusAnswers(t *testing.T) {
	entries, queries := Generate(7, Shape{Count: 1000, Depth: 2, Fanout: 8, Namespaces: 2})
	for _, q := range queries {
		if q.Kind == "short" {
			continue
		}
		got := matching(entries, q.Text)
		if !slices.Equal(got, q.Exact) {
			t.Fatalf("%s query expected %d answers, got %d", q.Kind, len(q.Exact), len(got))
		}
	}
}

func BenchmarkGenerate10K(b *testing.B) {
	shape := Shape{Count: 10_000, Depth: 3, Fanout: 32, Namespaces: 2}
	b.ReportAllocs()
	for b.Loop() {
		Generate(1, shape)
	}
}

func BenchmarkContentHash10K(b *testing.B) {
	entries, _ := Generate(1, Shape{Count: 10_000, Depth: 3, Fanout: 32, Namespaces: 2})
	b.ReportAllocs()
	for b.Loop() {
		_ = ContentHash(entries)
	}
}

func BenchmarkIndexOperations10K(b *testing.B) {
	entries, _ := Generate(1, Shape{Count: 10_000, Depth: 3, Fanout: 32, Namespaces: 2})
	idx := toIndex(entries)
	b.ReportAllocs()
	for b.Loop() {
		dir := b.TempDir()
		ix, err := index.Open(dir, index.DefaultConfig())
		if err != nil {
			b.Fatal(err)
		}
		if appendErr := ix.Append(idx); appendErr != nil {
			b.Fatal(appendErr)
		}
		if mergeErr := ix.Merge(b.Context(), nil); mergeErr != nil {
			b.Fatal(mergeErr)
		}
		if _, queryErr := ix.Query([]byte("report"), 0); queryErr != nil {
			b.Fatal(queryErr)
		}
	}
}
