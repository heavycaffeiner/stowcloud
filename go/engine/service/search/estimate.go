package search

import (
	"fmt"
	"math"
)

// Sizing the index before one is built.
//
// The decision turns on how many distinct trigrams the corpus contains, which is
// what the sketch measures. A CJK corpus holds far more distinct trigrams than a
// Latin one at the same file count, and misjudging that is how a search that
// should have used the index ends up performing a full walk.

// Per-entry costs the format fixes in place.
const (
	// EntryOverheadBytes covers the varint share id and name length a block
	// record carries in addition to the name.
	EntryOverheadBytes = 3
	// BlockDirEntryBytes and DictEntryBytes give the format's own widths.
	BlockDirEntryBytes = 16
	DictEntryBytes     = 12
	// HeaderBytes gives the fixed segment header's size.
	HeaderBytes = 128
)

// fallbackPruneRetention is the survival fraction the analytic model assumes for
// pruning when no sample measured it. The model cannot represent pruning
// properly because it treats every trigram as equally frequent, which is
// precisely what pruning targets.
const fallbackPruneRetention = 0.55

// Confidence indicates how much weight the estimate deserves.
type Confidence int

const (
	// ConfidenceMeasured means the posting term derives from sampled blocks.
	ConfidenceMeasured Confidence = iota
	// ConfidenceModelled means it derives from the analytic occupancy model,
	// which cannot represent pruning.
	ConfidenceModelled
)

func (c Confidence) String() string {
	if c == ConfidenceMeasured {
		return "measured"
	}
	return "modelled"
}

// CorpusStats holds what a sampling scan measured.
type CorpusStats struct {
	Files uint64
	// NameBytesTotal sums the name lengths.
	NameBytesTotal uint64
	// DistinctTrigramsEst carries the sketch's answer, the term distinguishing a
	// CJK corpus from a Latin one.
	DistinctTrigramsEst uint64
	// SampleCompressRatio gives compressed over raw as measured on sampled
	// blocks.
	SampleCompressRatio float32
	// PostingBytesPerBlock is measured across sampled blocks after pruning. Zero
	// indicates nothing measured it, so the analytic model applies instead.
	PostingBytesPerBlock float64
}

// IndexEstimate reports what building the index would cost.
type IndexEstimate struct {
	IndexBytes uint64
	Confidence Confidence
	// BuildSeconds is processor time, not wall clock: a build runs only while
	// the server is otherwise idle, so it finishes later than this.
	BuildSeconds float64
	// RateMeasured says BuildSeconds came from what the last completed build
	// on this deployment actually measured, rather than the compiled-in
	// guess. A caller with somewhere to show it can then tell an operator
	// which kind of number they are planning against.
	RateMeasured bool
	// Formula records the term-by-term derivation, so a wrong estimate reveals
	// which term was wrong. An operator verifying the arithmetic needs the
	// terms, while everyone else only wanted a size.
	Formula string
}

// indexFilesPerSecond is the compiled-in guess for what one core gets through
// when building the name index, used until a real build on this deployment has
// measured the actual rate.
//
// A round number from the shape of the work rather than a benchmark: each
// file costs a name read and its trigrams, and the build is bounded by that
// rather than by the disk. Deliberately conservative, so the figure an
// operator plans against is not one the build overruns.
const indexFilesPerSecond = 20_000

// EstimateNameIndex computes the index size for a corpus. measuredRate is the
// entries-per-second the last completed build on this deployment measured, or
// zero when none has completed; zero falls back to the compiled-in guess.
//
//	blocks         = ceil(files / block_size)
//	block_bytes   ~= (name_bytes_total + 3 x files) x sample_compress_ratio
//	blockdir       = 16 x blocks
//	dict_bytes     = 12 x distinct_trigrams
//	posting_bytes ~= sum of df x varint width across unpruned trigrams
//	index_bytes   ~= header + blockdir + dict + postings + blocks
func EstimateNameIndex(stats CorpusStats, blockSize uint32, measuredRate uint64) IndexEstimate {
	bs := uint64(blockSize)
	if bs == 0 {
		bs = 1
	}
	blocks := (stats.Files + bs - 1) / bs
	blocksF := math.Max(float64(blocks), 1)

	rawNames := stats.NameBytesTotal + stats.Files*EntryOverheadBytes
	ratio := float64(stats.SampleCompressRatio)
	if ratio < 0.01 {
		ratio = 0.01
	}
	if ratio > 1 {
		ratio = 1
	}
	blockBytes := uint64(float64(rawNames) * ratio)
	blockDirBytes := blocks * BlockDirEntryBytes

	d := math.Max(float64(stats.DistinctTrigramsEst), 1)
	dictBytes := stats.DistinctTrigramsEst * DictEntryBytes

	var postingBytes uint64
	var postingNote string
	confidence := ConfidenceMeasured

	if stats.PostingBytesPerBlock > 0 {
		// The preferred path. Sampled blocks form a uniform sample of all
		// blocks, so per-block bytes measured across them estimate per-block
		// bytes over the corpus without bias, and pruning is applied using the
		// sample's own frequency distribution instead of being assumed away.
		postingBytes = uint64(blocksF * stats.PostingBytesPerBlock)
		postingNote = fmt.Sprintf("%.1f B/block measured on sampled blocks, after pruning",
			stats.PostingBytesPerBlock)
	} else {
		// Given occ trigram occurrences distributed across d distinct values, a
		// block containing occ/blocks of them covers
		// d x (1 - e^(-occ_per_block/d)) distinct values, the standard
		// occupancy expression.
		confidence = ConfidenceModelled
		occ := float64(saturatingSub(stats.NameBytesTotal, 2*stats.Files))
		distinctPerBlock := d * (1 - math.Exp(-(occ/blocksF)/d))
		retained := blocksF * distinctPerBlock * fallbackPruneRetention
		avgDF := math.Max(retained/d, 1)
		avgGap := math.Max(blocksF/avgDF, 1)
		first := d * float64(VarintLen(blocks/2))
		rest := math.Max(retained-d, 0) * float64(VarintLen(uint64(avgGap)))
		postingBytes = uint64(first + rest)
		postingNote = "analytic occupancy model, not sampled: treat as a lower-confidence bound"
	}

	rate := float64(indexFilesPerSecond)
	rateMeasured := false
	if measuredRate > 0 {
		rate = float64(measuredRate)
		rateMeasured = true
	}

	total := uint64(HeaderBytes) + blockDirBytes + dictBytes + postingBytes + blockBytes
	return IndexEstimate{
		IndexBytes:   total,
		Confidence:   confidence,
		BuildSeconds: float64(stats.Files) / rate,
		RateMeasured: rateMeasured,
		Formula: fmt.Sprintf(
			"%d files in %d blocks; blocks %d B (%d raw x %.2f); blockdir %d B; "+
				"dict %d B (%d trigrams x %d); postings %d B (%s); total %d B",
			stats.Files, blocks, blockBytes, rawNames, ratio, blockDirBytes,
			dictBytes, stats.DistinctTrigramsEst, DictEntryBytes,
			postingBytes, postingNote, total),
	}
}

func saturatingSub(a, b uint64) uint64 {
	if b > a {
		return 0
	}
	return a - b
}
