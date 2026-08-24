//go:build linux

package handler

import (
	"encoding/json"
	"testing"
)

// The admin screen sends chunk_default, which is also the name this route
// answers with. It decoded chunk_size, so the field arrived as absent on every
// save: the response reported success and the default chunk never moved.
func TestTheUploadPatchDecodesTheClientsFieldNames(t *testing.T) {
	// Exactly what web/src/lib/api/types.ts UploadSettingsReq sends.
	const body = `{"chunk_min":8388608,"chunk_default":16777216}`

	var req struct {
		ChunkMin     *uint64 `json:"chunk_min"`
		ChunkDefault *uint64 `json:"chunk_default"`
	}
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if req.ChunkMin == nil || *req.ChunkMin != 8388608 {
		t.Errorf("chunk_min decoded as %v", req.ChunkMin)
	}
	if req.ChunkDefault == nil {
		t.Fatal("chunk_default decoded as absent, so the save is a no-op")
	}
	if *req.ChunkDefault != 16777216 {
		t.Fatalf("chunk_default decoded as %d", *req.ChunkDefault)
	}
}

// The old name is not silently accepted as well. Honouring both would leave
// two spellings of one setting, and which one a client used would decide
// whether the save took.
func TestTheOldChunkSizeFieldNameNoLongerDecodes(t *testing.T) {
	var req struct {
		ChunkDefault *uint64 `json:"chunk_default"`
	}
	if err := json.Unmarshal([]byte(`{"chunk_size":16777216}`), &req); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if req.ChunkDefault != nil {
		t.Fatalf("the old spelling decoded as %d, want absent", *req.ChunkDefault)
	}
}
