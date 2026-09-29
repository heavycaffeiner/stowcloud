package core

import (
	"testing"

	"github.com/heavycaffeiner/stowcloud/backend/internal/storage/vfs"
)

// ShareID is an alias rather than a defined type, so a vfs.ShareID passes
// where a core.ShareID is wanted with no conversion. A defined type would
// refuse both calls below, which is what the alias exists to avoid.
func TestShareIDIsTheVFSShareID(t *testing.T) {
	t.Parallel()
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
