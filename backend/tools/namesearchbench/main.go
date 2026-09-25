//go:build linux

// namesearchbench generates a deterministic filename corpus and measures the
// existing namesearch index in separate build, open, query, update, and merge
// phases. It is an exploratory harness, not a release performance claim.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"runtime"
	"sort"
	"strings"

	"github.com/heavycaffeiner/stowcloud/backend/internal/platform/clock"
	search "github.com/stowcloud/namesearch"
	"github.com/stowcloud/namesearch/index"
)

type Entry struct {
	Namespace uint32 `json:"namespace"`
	Path      string `json:"path"`
}

type Query struct {
	Kind  string   `json:"kind"`
	Text  string   `json:"text"`
	Exact []string `json:"-"`
}

type Shape struct {
	Count      int `json:"count"`
	Depth      int `json:"depth"`
	Fanout     int `json:"fanout"`
	Namespaces int `json:"namespaces"`
}

type Config struct {
	Seed        int64  `json:"seed"`
	Shape       Shape  `json:"shape"`
	SampleLimit int    `json:"sample_limit"`
	Concurrency int    `json:"concurrency"`
	Cache       string `json:"cache"`
	Filesystem  string `json:"filesystem"`
	Hardware    string `json:"hardware"`
}

type Metadata struct {
	Toolchain   string `json:"toolchain"`
	GoVersion   string `json:"go_version"`
	GOOS        string `json:"goos"`
	GOARCH      string `json:"goarch"`
	CPUs        int    `json:"cpus"`
	GOMAXPROCS  int    `json:"gomaxprocs"`
	Source      string `json:"source"`
	Hardware    string `json:"hardware"`
	Filesystem  string `json:"filesystem"`
	Cache       string `json:"cache"`
	Concurrency int    `json:"concurrency"`
}

type Measurement struct {
	Operation  string `json:"operation"`
	Query      string `json:"query,omitempty"`
	Kind       string `json:"kind,omitempty"`
	DurationNs int64  `json:"duration_ns"`
	Count      int    `json:"count"`
	Fallback   string `json:"fallback,omitempty"`
}

type Check struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

type Report struct {
	Claim        string        `json:"claim"`
	Config       Config        `json:"config"`
	Metadata     Metadata      `json:"metadata"`
	CorpusCount  int           `json:"corpus_count"`
	ContentHash  string        `json:"content_hash"`
	Measurements []Measurement `json:"measurements"`
	Checks       []Check       `json:"checks"`
}

func main() {
	var cfg Config
	flag.Int64Var(&cfg.Seed, "seed", 1, "deterministic corpus seed")
	flag.IntVar(&cfg.Shape.Count, "count", 10_000, "number of entries (10k, 100k, or configurable 1M)")
	flag.IntVar(&cfg.Shape.Depth, "depth", 3, "directory depth")
	flag.IntVar(&cfg.Shape.Fanout, "fanout", 32, "directory fanout")
	flag.IntVar(&cfg.Shape.Namespaces, "namespaces", 2, "number of uint32 namespaces")
	flag.IntVar(&cfg.SampleLimit, "sample", 8, "number of paths retained in raw samples")
	flag.IntVar(&cfg.Concurrency, "concurrency", 1, "requested benchmark concurrency input")
	flag.StringVar(&cfg.Cache, "cache", "cold-unspecified", "cache state input (for metadata only)")
	flag.StringVar(&cfg.Filesystem, "filesystem", "unspecified", "filesystem input (for metadata only)")
	flag.StringVar(&cfg.Hardware, "hardware", "auto", "hardware input (auto uses /proc/cpuinfo when available)")
	jsonOut := flag.String("out", "", "write aggregate JSON report to this path")
	rawJSON := flag.String("raw-json", "", "write raw sample JSON rows to this path")
	rawCSV := flag.String("raw-csv", "", "write raw sample CSV rows to this path")
	flag.Parse()
	if cfg.Shape.Count < 1 || cfg.Shape.Depth < 1 || cfg.Shape.Fanout < 1 || cfg.Shape.Namespaces < 1 || cfg.SampleLimit < 0 || cfg.Concurrency < 1 {
		fatalf("count, depth, fanout, namespaces, sample, and concurrency must be positive (sample may be zero)")
	}

	clk := clock.System()
	start := clk.Now()
	entries, queries := Generate(cfg.Seed, cfg.Shape)
	report := Report{
		Claim:       "This is a local exploratory measurement, not a release performance claim.",
		Config:      cfg,
		Metadata:    metadata(cfg),
		CorpusCount: len(entries),
		ContentHash: ContentHash(entries),
	}
	report.Measurements = append(report.Measurements, Measurement{Operation: "generate", DurationNs: clk.Since(start).Nanoseconds(), Count: len(entries)})

	dir, err := os.MkdirTemp("", "namesearchbench-")
	if err != nil {
		fatalf("temp directory: %v", err)
	}
	defer func() {
		if removeErr := os.RemoveAll(dir); removeErr != nil {
			log.Printf("remove temp directory %s: %v", dir, removeErr)
		}
	}()
	ix, err := index.Open(dir, index.DefaultConfig())
	if err != nil {
		fatalf("open: %v", err)
	}

	start = clk.Now()
	idxEntries := toIndex(entries)
	if appendErr := ix.Append(idxEntries); appendErr != nil {
		fatalf("build append: %v", appendErr)
	}
	report.Measurements = append(report.Measurements, Measurement{Operation: "build", DurationNs: clk.Since(start).Nanoseconds(), Count: len(entries)})

	start = clk.Now()
	if mergeErr := ix.Merge(context.Background(), nil); mergeErr != nil {
		fatalf("merge: %v", mergeErr)
	}
	report.Measurements = append(report.Measurements, Measurement{Operation: "merge", DurationNs: clk.Since(start).Nanoseconds(), Count: len(entries)})

	start = clk.Now()
	ix, err = index.Open(dir, index.DefaultConfig())
	if err != nil {
		fatalf("reopen: %v", err)
	}
	report.Measurements = append(report.Measurements, Measurement{Operation: "open", DurationNs: clk.Since(start).Nanoseconds(), Count: len(entries)})

	for _, q := range queries {
		start = clk.Now()
		got, err := ix.Query([]byte(q.Text), 0)
		if err != nil {
			fatalf("query %s: %v", q.Kind, err)
		}
		m := Measurement{Operation: "query", Query: q.Text, Kind: q.Kind, DurationNs: clk.Since(start).Nanoseconds(), Count: len(got.Hits), Fallback: got.Fallback.String()}
		report.Measurements = append(report.Measurements, m)
		report.Checks = append(report.Checks, checkQuery(q, got))
	}

	updates := toIndex(entries[len(entries)-min(100, len(entries)):])
	start = clk.Now()
	if err := ix.Append(updates); err != nil {
		fatalf("update: %v", err)
	}
	report.Measurements = append(report.Measurements, Measurement{Operation: "update", DurationNs: clk.Since(start).Nanoseconds(), Count: len(updates)})
	start = clk.Now()
	if err := ix.Merge(context.Background(), nil); err != nil {
		fatalf("post-update merge: %v", err)
	}
	report.Measurements = append(report.Measurements, Measurement{Operation: "merge", Query: "post-update", DurationNs: clk.Since(start).Nanoseconds(), Count: len(updates)})
	report.Checks = append(report.Checks, Check{Name: "content hash is non-empty", OK: report.ContentHash != ""})
	report.Checks = append(report.Checks, Check{Name: "all correctness checks pass", OK: allChecks(report.Checks)})

	if *rawJSON != "" || *rawCSV != "" {
		rows := makeRawRows(report, cfg.SampleLimit)
		if *rawJSON != "" {
			writeJSON(*rawJSON, rows)
		}
		if *rawCSV != "" {
			writeCSV(*rawCSV, rows)
		}
	}
	if *jsonOut != "" {
		writeJSON(*jsonOut, report)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(report); err != nil {
		fatalf("encode report: %v", err)
	}
}

func Generate(seed int64, shape Shape) ([]Entry, []Query) {
	if shape.Count < 1 {
		return nil, nil
	}
	entries := make([]Entry, 0, shape.Count)
	for i := 0; i < shape.Count; i++ {
		ns := uint32(i % shape.Namespaces)
		parts := make([]string, 0, shape.Depth+1)
		v := uint64(seed) ^ uint64(i)*0x9e3779b97f4a7c15
		for d := 0; d < shape.Depth; d++ {
			v = splitmix(v)
			parts = append(parts, fmt.Sprintf("dir-%02d-%03d", d, int(v%uint64(shape.Fanout))))
		}
		kind := "file"
		switch {
		case i%997 == 0:
			kind = "東京"
		case i%17 == 0:
			kind = "common-report"
		case i%11 == 0:
			kind = "report-prefix"
		case i%13 == 0:
			kind = "annual-substring"
		}
		ext := []string{"txt", "pdf", "jpg", "bin"}[i%4]
		parts = append(parts, fmt.Sprintf("%s-%08d.%s", kind, i, ext))
		entries = append(entries, Entry{Namespace: ns, Path: strings.Join(parts, "/")})
	}
	queries := makeQueries(entries)
	return entries, queries
}

func makeQueries(entries []Entry) []Query {
	if len(entries) == 0 {
		return nil
	}
	first := entries[0].Path
	if i := strings.LastIndexByte(first, '/'); i >= 0 {
		first = first[i+1:]
	}
	return []Query{
		{Kind: "exact", Text: first, Exact: matching(entries, first)},
		{Kind: "prefix", Text: "report-prefix", Exact: matching(entries, "report-prefix")},
		{Kind: "substring", Text: "annual-substring", Exact: matching(entries, "annual-substring")},
		{Kind: "common", Text: "common", Exact: matching(entries, "common")},
		{Kind: "no-hit", Text: "no-such-name-9f4e", Exact: nil},
		{Kind: "unicode", Text: "東京", Exact: matching(entries, "東京")},
		{Kind: "path", Text: "file-", Exact: matching(entries, "file-")},
		{Kind: "short", Text: "ab", Exact: nil},
	}
}

func matching(entries []Entry, needle string) []string {
	out := make([]string, 0)
	folded := search.FoldString(needle)
	for _, e := range entries {
		name := e.Path
		if i := strings.LastIndexByte(name, '/'); i >= 0 {
			name = name[i+1:]
		}
		if search.Contains(search.FoldString(name), folded) {
			out = append(out, e.Path)
		}
	}
	sort.Strings(out)
	return out
}
func checkQuery(q Query, got index.Result) Check {
	if q.Kind == "short" {
		ok := got.Fallback == index.FallbackQueryTooShort && len(got.Hits) == 0
		return Check{Name: "query/" + q.Kind, OK: ok, Detail: got.Fallback.String()}
	}
	if got.MustFallBack() {
		return Check{Name: "query/" + q.Kind, OK: len(got.Hits) == 0, Detail: "accepted index fallback: " + got.Fallback.String()}
	}
	paths := make([]string, 0, len(got.Hits))
	for _, h := range got.Hits {
		paths = append(paths, h.Path)
	}
	sort.Strings(paths)
	ok := got.Fallback == index.FallbackNone && equalStrings(paths, q.Exact)
	return Check{Name: "query/" + q.Kind, OK: ok, Detail: fmt.Sprintf("got=%d want=%d", len(paths), len(q.Exact))}
}

func ContentHash(entries []Entry) string {
	h := sha256.New()
	for _, e := range entries {
		if _, err := fmt.Fprintf(h, "%d\t%s\n", e.Namespace, e.Path); err != nil {
			panic(err)
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

func toIndex(entries []Entry) []index.Entry {
	out := make([]index.Entry, len(entries))
	for i, e := range entries {
		out[i] = index.Entry{Namespace: index.Namespace(e.Namespace), Path: e.Path}
	}
	return out
}

func metadata(cfg Config) Metadata {
	hardware := cfg.Hardware
	if hardware == "auto" {
		hardware = "unknown"
		if b, err := os.ReadFile("/proc/cpuinfo"); err == nil {
			for _, line := range strings.Split(string(b), "\n") {
				if strings.HasPrefix(line, "model name") {
					hardware = strings.TrimSpace(strings.SplitN(line, ":", 2)[1])
					break
				}
			}
		}
	}
	return Metadata{Toolchain: runtime.Compiler, GoVersion: runtime.Version(), GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, CPUs: runtime.NumCPU(), GOMAXPROCS: runtime.GOMAXPROCS(0), Source: "github.com/heavycaffeiner/stowcloud/backend/tools/namesearchbench", Hardware: hardware, Filesystem: cfg.Filesystem, Cache: cfg.Cache, Concurrency: cfg.Concurrency}
}

func makeRawRows(r Report, limit int) []map[string]any {
	rows := make([]map[string]any, 0, len(r.Measurements))
	for i, m := range r.Measurements {
		if limit > 0 && i >= limit && m.Operation == "query" {
			continue
		}
		rows = append(rows, map[string]any{"operation": m.Operation, "query": m.Query, "kind": m.Kind, "duration_ns": m.DurationNs, "count": m.Count, "fallback": m.Fallback})
	}
	return rows
}

func writeJSON(path string, v any) {
	f, err := os.Create(path)
	if err != nil {
		fatalf("create %s: %v", path, err)
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		if closeErr := f.Close(); closeErr != nil {
			fatalf("write %s: %v (close: %v)", path, err, closeErr)
		}
		fatalf("write %s: %v", path, err)
	}
	if err := f.Close(); err != nil {
		fatalf("close %s: %v", path, err)
	}
}

func writeCSV(path string, rows []map[string]any) {
	f, err := os.Create(path)
	if err != nil {
		fatalf("create %s: %v", path, err)
	}
	w := csv.NewWriter(f)
	if err := w.Write([]string{"operation", "query", "kind", "duration_ns", "count", "fallback"}); err != nil {
		if closeErr := f.Close(); closeErr != nil {
			fatalf("write %s: %v (close: %v)", path, err, closeErr)
		}
		fatalf("write %s: %v", path, err)
	}
	for _, row := range rows {
		if err := w.Write([]string{fmt.Sprint(row["operation"]), fmt.Sprint(row["query"]), fmt.Sprint(row["kind"]), fmt.Sprint(row["duration_ns"]), fmt.Sprint(row["count"]), fmt.Sprint(row["fallback"])}); err != nil {
			if closeErr := f.Close(); closeErr != nil {
				fatalf("write %s: %v (close: %v)", path, err, closeErr)
			}
			fatalf("write %s: %v", path, err)
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		if closeErr := f.Close(); closeErr != nil {
			fatalf("write %s: %v (close: %v)", path, err, closeErr)
		}
		fatalf("write %s: %v", path, err)
	}
	if err := f.Close(); err != nil {
		fatalf("close %s: %v", path, err)
	}
}

func allChecks(checks []Check) bool {
	for _, c := range checks {
		if !c.OK {
			return false
		}
	}
	return true
}
func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
func splitmix(x uint64) uint64 {
	x += 0x9e3779b97f4a7c15
	x = (x ^ (x >> 30)) * 0xbf58476d1ce4e5b9
	x = (x ^ (x >> 27)) * 0x94d049bb133111eb
	return x ^ (x >> 31)
}
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
func fatalf(format string, args ...any) {
	if _, err := fmt.Fprintf(os.Stderr, format+"\n", args...); err != nil {
		os.Exit(2)
	}
	os.Exit(2)
}
