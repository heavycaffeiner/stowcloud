//go:build linux

package core

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/internal/store/state"
	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
)

// A share's symlink policy reaches the share.
//
// The type, its three modes and a resolver that branches on all three all
// existed, and nothing carried the operator's choice to the registration:
// every share got Deny whatever was stored, and nothing said so.

func TestAShareCarriesItsSymlinkPolicy(t *testing.T) {
	for _, c := range []struct {
		stored string
		want   vfs.SymlinkPolicy
	}{
		{"deny", vfs.SymlinkDeny},
		{"within_share", vfs.SymlinkWithinShare},
		{"follow", vfs.SymlinkFollow},
		// An empty one is the restrictive policy, which is what a row written
		// before the column existed carries.
		{"", vfs.SymlinkDeny},
		// So is a word this build does not have: an operator who believes a
		// share follows links it does not is worse served by a guess.
		{"sometimes", vfs.SymlinkDeny},
	} {
		core, s, _ := testCore(t)
		host := filepath.Join(t.TempDir(), "share")
		if err := os.MkdirAll(host, 0o775); err != nil {
			t.Fatalf("creating the share: %v", err)
		}
		if _, err := s.State().InsertShare(ctx(), state.ShareRow{
			Name: "linked", Host: host, SymlinkPolicy: c.stored,
		}, 0); err != nil {
			t.Fatalf("storing the share: %v", err)
		}
		if _, err := core.ReloadPersistedShares(ctx()); err != nil {
			t.Fatalf("registering: %v", err)
		}
		def, ok := shareNamed(core, "linked")
		if !ok {
			t.Fatalf("the share did not register")
		}
		if got := def.Policy.Symlink; got != c.want {
			t.Errorf("stored policy %q became %v, want %v", c.stored, got, c.want)
		}
	}
}

func shareNamed(c *Core, name string) (ShareDef, bool) {
	for _, def := range c.Shares() {
		if def.Name == name {
			return def, true
		}
	}
	return ShareDef{}, false
}
