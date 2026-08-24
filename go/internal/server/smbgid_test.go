// Linux only, because what it tests is.
//go:build linux

package server

import "testing"

// The SMB service group.
//
// It used to be a constant in the publisher, which meant a bare-metal install
// whose service account lived in another group had no way to say so. The
// failure was at least loud, because the agent refuses a sync naming the group
// it could not find, but the only fix was a rebuild.

func smbRaw(t *testing.T) raw {
	t.Helper()
	var r raw
	r.Server.DataDir = "/tmp/x"
	r.HTTP.AppHosts = []string{"nas.local"}
	r.SMB.Enabled = true
	r.SMB.ServiceUser = "scsvc"
	return r
}

// An unset key takes the image's group, so a stock container deployment does
// not have to name a number that is already true of it.
func TestAnUnsetServiceGIDTakesTheImagesGroup(t *testing.T) {
	cfg, err := Validate(smbRaw(t))
	if err != nil {
		t.Fatalf("a config with SMB on and no group: %v", err)
	}
	if cfg.SMB.ServiceGID != defaultServiceGID {
		t.Fatalf("the group is %d, want the image's %d", cfg.SMB.ServiceGID, defaultServiceGID)
	}
}

func TestAConfiguredServiceGIDIsCarried(t *testing.T) {
	r := smbRaw(t)
	want := uint32(2001)
	r.SMB.ServiceGID = &want

	cfg, err := Validate(r)
	if err != nil {
		t.Fatalf("a config naming its own group: %v", err)
	}
	if cfg.SMB.ServiceGID != want {
		t.Fatalf("the group is %d, want %d", cfg.SMB.ServiceGID, want)
	}
}

// Zero is root's group. The agent runs as root, so an account file putting
// every SMB account in it would be applied rather than refused, which is why
// this is a startup refusal and not a warning.
func TestGroupZeroIsRefused(t *testing.T) {
	r := smbRaw(t)
	zero := uint32(0)
	r.SMB.ServiceGID = &zero

	if _, err := Validate(r); err == nil {
		t.Fatal("root's group was accepted as the SMB service group")
	}
}
