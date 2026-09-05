//go:build linux

package lifecycle

import (
	"log/slog"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/state"
)

// A share whose backing did not open is not rendered.
//
// Rendering one publishes a network name whose path does not resolve where the
// daemon runs. To a client that is a share which exists and refuses, which is
// harder to understand than a share that is simply absent while its disk is.
func TestABrokenShareIsNotRendered(t *testing.T) {
	defs := []core.ShareDef{
		{ID: 1, Name: "documents", Host: "/srv/documents"},
		{ID: 2, Name: "archive", Host: "/srv/archive", BrokenReason: "not_found"},
	}

	got := publishShares(defs, slog.Default())
	if len(got) != 1 {
		t.Fatalf("got %d shares, want only the servable one: %+v", len(got), got)
	}
	if got[0].Name != "documents" {
		t.Errorf("the rendered share is %q, want documents", got[0].Name)
	}
}

// A non-local share has no host path for this format to render into a share
// stanza, so it is excluded the same way a broken share is.
func TestANonLocalShareIsNotRendered(t *testing.T) {
	defs := []core.ShareDef{
		{ID: 1, Name: "documents", Host: "/srv/documents"},
		{ID: 2, Name: "bucket", Backend: core.BackendS3},
	}

	got := publishShares(defs, slog.Default())
	if len(got) != 1 {
		t.Fatalf("got %d shares, want only the local one: %+v", len(got), got)
	}
	if got[0].Name != "documents" {
		t.Errorf("the rendered share is %q, want documents", got[0].Name)
	}
}

// The share's creation modes travel with it, since the daemon applies them to
// what it creates and a zero there would write files nobody can read.
func TestARenderedShareCarriesItsModes(t *testing.T) {
	got := publishShares([]core.ShareDef{{
		ID: 1, Name: "documents", Host: "/srv/documents",
		Policy:           vfs.SharePolicy{ModeFile: 0o640, ModeDir: 0o750},
		SharedExternally: true,
	}}, slog.Default())
	if len(got) != 1 {
		t.Fatalf("got %d shares, want 1", len(got))
	}
	if got[0].ModeFile != 0o640 || got[0].ModeDir != 0o750 {
		t.Errorf("the modes are %o and %o", got[0].ModeFile, got[0].ModeDir)
	}
	if !got[0].SharedExternally {
		t.Error("the externally shared flag did not travel")
	}
	if got[0].Path != "/srv/documents" {
		t.Errorf("the path is %q", got[0].Path)
	}
}

// Reading over this protocol delivers the bytes, so a grant admitting a look
// without a download admits neither.
//
// The protocol has no view-only mode. Rendering such a grant as readable would
// hand over the file the web interface refuses to send.
func TestAViewOnlyGrantIsNotReadableOverTheProtocol(t *testing.T) {
	user := int64(7)
	rows := []state.GrantRow{
		{ID: 1, User: &user, Share: 1, Allow: uint16(acl.Read)},
		{ID: 2, User: &user, Share: 2, Allow: uint16(acl.Read | acl.Download)},
	}

	got := grantsOf(rows)
	if len(got) != 2 {
		t.Fatalf("got %d grants, want 2", len(got))
	}
	if got[0].AllowRead {
		t.Error("a grant without the download bit was rendered as readable")
	}
	if !got[1].AllowRead {
		t.Error("a grant carrying both bits was not rendered as readable")
	}
}

// A grant beginning partway down a tree is not whole-share.
//
// The format has no way to express a permission that starts below the root, so
// rendering one as whole-share would grant the entire share.
func TestASubpathGrantIsNotWholeShare(t *testing.T) {
	user := int64(7)
	got := grantsOf([]state.GrantRow{
		{ID: 1, User: &user, Share: 1, Subpath: "", Allow: uint16(acl.Read | acl.Download)},
		{ID: 2, User: &user, Share: 2, Subpath: "reports", Allow: uint16(acl.Read | acl.Download)},
	})
	if !got[0].WholeShare {
		t.Error("a grant at the root was not whole-share")
	}
	if got[1].WholeShare {
		t.Error("a grant on a subpath was rendered as whole-share")
	}
}

// Any deny bit marks the grant, whatever it covers.
//
// The renderer drops such a user from that share entirely, because this format
// is additive and cannot express a denial that survives the other lists.
func TestAnyDenyBitMarksTheGrant(t *testing.T) {
	user := int64(7)
	got := grantsOf([]state.GrantRow{
		{ID: 1, User: &user, Share: 1, Allow: uint16(acl.Read | acl.Download)},
		{ID: 2, User: &user, Share: 2, Allow: uint16(acl.Read | acl.Download), Deny: uint16(acl.Write)},
	})
	if got[0].Denies {
		t.Error("a grant carrying no deny bit was marked as denying")
	}
	if !got[1].Denies {
		t.Error("a grant carrying a deny bit was not marked")
	}
}

// A group grant carries no account, which this format cannot express.
func TestAGroupGrantNamesNoAccount(t *testing.T) {
	group := int64(3)
	got := grantsOf([]state.GrantRow{
		{ID: 1, Group: &group, Share: 1, Allow: uint16(acl.Read | acl.Download)},
	})
	if got[0].User != 0 {
		t.Errorf("a group grant named account %d", got[0].User)
	}
}
