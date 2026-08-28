package search

import (
	"fmt"
	"math"
)

// Sizing the index before building one.
//
// The decision depends on how many distinct trigrams the corpus has, which is
// what the sketch is for. A CJK corpus has vastly more distinct trigrams than
// a Latin one at the same file count, and getting that wrong is how a search
// that should have taken the index takes a full walk instead.

// Per-entry costs the format fixes.
const (
	// EntryOverheadBytes is the varint share id and name length a block record
	// carries beyond the name itself.
	EntryOverheadBytes = 3
	// BlockDirEntryBytes and DictEntryBytes are the format's own widths.
	BlockDirEntryBytes = 16
	DictEntryBytes     = 12
	// HeaderBytes is the fixed segment header.
	HeaderBytes = 128
)

// fallbackPruneRetention is what the analytic model assumes survives pruning
// when no sample measured it. It cannot model pruning properly, because it
// treats every trigram as equally common, which is exactly what pruning is
// about.
const fallbackPruneRetention = 0.55

// Confidence says how much the estimate is worth.
type Confidence int

const (
	// ConfidenceMeasured means the posting term came from sampled blocks.
	ConfidenceMeasured Confidence = iota
	// ConfidenceModelled means it came from the analytic occupancy model,
	// which cannot account for pruning.
	ConfidenceModelled
)

func (c Confidence) String() string {
	if c == ConfidenceMeasured {
		return "measured"
	}
	return "modelled"
}

// CorpusStats is what a sampling scan measured.
type CorpusStats struct {
	Files uint64
	// NameBytesTotal is the sum of the name lengths.
	NameBytesTotal uint64
	// DistinctTrigramsEst is the sketch's answer, and the term that separates
	// a CJK corpus from a Latin one.
	DistinctTrigramsEst uint64
	// SampleCompressRatio is compressed over raw, measured on sampled blocks.
	SampleCompressRatio float32
	// PostingBytesPerBlock is measured on sampled blocks after pruning. Zero
	// means nothing measured it and the analytic model is used instead.
	PostingBytesPerBlock float64
}

// IndexEstimate is what building the index would cost.
type IndexEstimate struct {
	IndexBytes uint64
	Confidence Confidence
	// Formula is the term-by-term derivation, so that when the estimate is
	// wrong it is visible which term was wrong. An operator checking the
	// arithmetic needs the terms; everyone else needed a size.
	Formula string
}

// EstimateNameIndex sizes the index for a corpus.
//
//	blocks        = ceil(files / block_size)
//	block_bytes  ~= (name_bytes_total + files x 3) x sample_compress_ratio
//	blockdir      = blocks x 16
//	dict_bytes    = distinct_trigrams x 12
//	posting_bytes ~= sum over trigrams of df x varint width, pruned excluded
//	index_bytes  ~= header + blockdir + dict + postings + blocks
func EstimateNameIndex(stats CorpusStats, blockSize uint32) IndexEstimate {
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
		// The preferred path. Sampled blocks are a uniform sample of all
		// blocks, so per-block bytes measured on them estimate per-block bytes
		// over the corpus without bias, and pruning is applied using the
		// sample's own frequency distribution rather than assumed away.
		postingBytes = uint64(blocksF * stats.PostingBytesPerBlock)
		postingNote = fmt.Sprintf("%.1f B/block measured on sampled blocks, after pruning",
			stats.PostingBytesPerBlock)
	} else {
		// With occ trigram occurrences spread over d distinct values, a block
		// holding occ/blocks of them covers d x (1 - e^(-occ_per_block/d))
		// distinct values, which is the standard occupancy expression.
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

	total := uint64(HeaderBytes) + blockDirBytes + dictBytes + postingBytes + blockBytes
	return IndexEstimate{
		IndexBytes: total,
		Confidence: confidence,
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
