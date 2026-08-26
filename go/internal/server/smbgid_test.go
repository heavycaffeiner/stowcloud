// Linux only, because what it tests is.
//go:build linux

package server

import (
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/internal/runtimecfg"
)

// The SMB service group.
//
// It used to be a constant in the publisher, which meant a bare-metal install
// whose service account lived in another group had no way to say so. The
// failure was at least loud, because the agent refuses a sync naming the group
// it could not find, but the only fix was a rebuild.

// An unsaved key takes the image's group, so a stock container deployment does
// not have to name a number that is already true of it.
func TestAnUnsetServiceGIDTakesTheImagesGroup(t *testing.T) {
	cfg := FromValues("/tmp/x", runtimecfg.Defaults(), "")
	if cfg.SMB.ServiceGID != runtimecfg.DefaultSMBServiceGID {
		t.Fatalf("the group is %d, want the image's %d",
			cfg.SMB.ServiceGID, runtimecfg.DefaultSMBServiceGID)
	}
}

func TestAConfiguredServiceGIDIsCarried(t *testing.T) {
	v := runtimecfg.Defaults()
	v.SMB.ServiceGID = 2001
	cfg := FromValues("/tmp/x", v, "")
	if cfg.SMB.ServiceGID != 2001 {
		t.Fatalf("the group is %d, want 2001", cfg.SMB.ServiceGID)
	}
}

// Zero is root's group, which no service account may belong to. It is refused
// where an administrator is watching, so the bound is what proves it here: the
// stored value is clamped away from zero rather than reaching the renderer.
func TestZeroIsNotAServiceGroup(t *testing.T) {
	if b := runtimecfg.BoundServiceGID(); b.Min < 1 {
		t.Fatalf("the service group bound admits %d, which is root's group", b.Min)
	}
}
