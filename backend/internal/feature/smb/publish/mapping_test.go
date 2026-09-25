//go:build linux

package publish

import (
	"log/slog"
	"testing"

	core "github.com/heavycaffeiner/stowcloud/backend/internal/feature/files"
	"github.com/heavycaffeiner/stowcloud/backend/internal/feature/shares/acl"
	"github.com/heavycaffeiner/stowcloud/backend/internal/storage/vfs"
	"github.com/heavycaffeiner/stowcloud/backend/internal/store/state"
)

func TestSharesOfFiltersBrokenEncryptedAndNonLocal(t *testing.T) {
	defs := []core.ShareDef{
		{ID: 1, Name: "documents", Host: "/srv/documents"},
		{ID: 2, Name: "archive", Host: "/srv/archive", BrokenReason: "not_found"},
		{ID: 3, Name: "bucket", Backend: core.BackendS3},
		{ID: 4, Name: "secrets", Host: "/srv/secrets"},
	}
	got := SharesOf(defs, map[core.ShareID]bool{4: true}, slog.Default())
	if len(got) != 1 || got[0].Name != "documents" {
		t.Fatalf("got %+v, want only documents", got)
	}
}

func TestSharesOfCarriesModesAndPath(t *testing.T) {
	got := SharesOf([]core.ShareDef{{ID: 1, Name: "documents", Host: "/srv/documents", Policy: vfs.SharePolicy{ModeFile: 0o640, ModeDir: 0o750}, SharedExternally: true}}, nil, slog.Default())
	if len(got) != 1 || got[0].ModeFile != 0o640 || got[0].ModeDir != 0o750 || !got[0].SharedExternally || got[0].Path != "/srv/documents" {
		t.Fatalf("got %+v", got)
	}
}

func TestGrantsOfCollapsesPermissionsAndGroups(t *testing.T) {
	user := int64(7)
	group := int64(3)
	got := GrantsOf([]state.GrantRow{
		{ID: 1, User: &user, Share: 1, Allow: uint16(acl.Read)},
		{ID: 2, User: &user, Share: 2, Subpath: "reports", Allow: uint16(acl.Read | acl.Download)},
		{ID: 3, Group: &group, Share: 3, Allow: uint16(acl.Read | acl.Download), Deny: uint16(acl.Write)},
	}, []state.MembershipRow{{Group: group, User: 9}})
	if len(got) != 3 {
		t.Fatalf("got %d grants, want 3: %+v", len(got), got)
	}
	if got[0].AllowRead || !got[1].AllowRead || got[1].WholeShare || !got[2].Denies || got[2].User != 9 {
		t.Fatalf("unexpected grant mapping: %+v", got)
	}
}
