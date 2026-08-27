package core

import (
	"encoding/hex"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
)

func TestNewInstanceIDIs32HexCharacters(t *testing.T) {
	id, err := NewInstanceID()
	if err != nil {
		t.Fatalf("NewInstanceID: %v", err)
	}
	if len(id) != 2*instanceIDBytes {
		t.Fatalf("NewInstanceID() = %q, want %d characters", id, 2*instanceIDBytes)
	}
	if _, err := hex.DecodeString(id); err != nil {
		t.Fatalf("NewInstanceID() = %q, which is not hex: %v", id, err)
	}
	for _, r := range id {
		if r >= 'A' && r <= 'Z' {
			t.Fatalf("NewInstanceID() = %q, want lowercase hex", id)
		}
	}
}

func TestTwoInstanceIDsDiffer(t *testing.T) {
	a, err := NewInstanceID()
	if err != nil {
		t.Fatalf("NewInstanceID: %v", err)
	}
	b, err := NewInstanceID()
	if err != nil {
		t.Fatalf("NewInstanceID: %v", err)
	}
	if a == b {
		t.Fatalf("two calls both returned %q", a)
	}
}

// ShareID is an alias rather than a defined type, so a vfs.ShareID passes
// where a core.ShareID is wanted with no conversion. A defined type would
// refuse both calls below, which is what the alias exists to avoid.
func TestShareIDIsTheVFSShareID(t *testing.T) {
	takesCore := func(id ShareID) uint32 { return uint32(id) }
	takesVFS := func(id vfs.ShareID) uint32 { return uint32(id) }

	var fromVFS vfs.ShareID = 7
	var fromCore ShareID = 7

	if got := takesCore(fromVFS); got != 7 {
		t.Fatalf("a vfs.ShareID through a core.ShareID parameter = %d, want 7", got)
	}
	if got := takesVFS(fromCore); got != 7 {
		t.Fatalf("a core.ShareID through a vfs.ShareID parameter = %d, want 7", got)
	}
}
