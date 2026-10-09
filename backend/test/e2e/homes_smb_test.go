//go:build linux

package e2e_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/heavycaffeiner/stowcloud/backend/internal/files"
	"github.com/heavycaffeiner/stowcloud/backend/internal/shares/acl"
	"github.com/heavycaffeiner/stowcloud/backend/internal/smb/agent"
)

// This stand-in reports which configuration the real control channel received.
// Actual SMB authentication and file operations are exercised against Samba
// separately; these tests cover application lifecycle and owner-only projection.
func homeSMBAgent(t *testing.T, configDir string, missing *atomic.Bool, missingPath string) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "sc-home-agent-")
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "agent.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := listener.Close(); err != nil {
			t.Error(err)
		}
		if err := os.RemoveAll(dir); err != nil {
			t.Error(err)
		}
	})
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			var request agent.Request
			if json.NewDecoder(conn).Decode(&request) == nil {
				body, err := os.ReadFile(filepath.Join(configDir, "smb.conf"))
				report := agent.Report{OK: true, Smbd: agent.ActionStarted}
				if os.IsNotExist(err) {
					report.Smbd = agent.ActionStopped
				}
				for _, section := range agent.Sections(string(body)) {
					report.Shares = append(report.Shares, section.Name)
				}
				if missing.Load() {
					report.OK = false
					report.MissingPaths = []string{missingPath}
				}
				if err := json.NewEncoder(conn).Encode(report); err != nil {
					t.Error(err)
				}
			}
			if err := conn.Close(); err != nil {
				t.Error(err)
			}
		}
	}()
	return socket
}

func TestRecreatedAccountCannotInheritCachedHomeOrSharedFolderPermissions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := openEngine(t)
	root := t.TempDir()
	if err := e.Core.EnableHomes(ctx, root); err != nil {
		t.Fatal(err)
	}
	oldID, err := e.Auth.CreateUser(ctx, "bob", "", pwOf(loginPassword))
	if err != nil {
		t.Fatal(err)
	}
	oldHome, err := e.Core.HomeDirectory(ctx, files.UserID(oldID))
	if err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(oldHome, "secret.txt")
	if err = os.WriteFile(secret, []byte("the previous account's private files"), 0o600); err != nil {
		t.Fatal(err)
	}
	share, err := e.Core.CreateShare(ctx, files.ShareSpec{Name: "team", Host: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.Core.CreateGrant(ctx, files.GrantSpec{User: &oldID, Share: share.ID, Allow: acl.Read | acl.Download, Inherit: true}); err != nil {
		t.Fatal(err)
	}
	if len(e.Core.Roots(files.UserID(oldID))) != 2 {
		t.Fatal("the fixture did not load the old account's grants")
	}
	// Cleanup is best-effort. Keep the old directory to exercise a deletion
	// where filesystem cleanup failed, or Home was switched off.
	if err = e.Auth.DeleteUser(ctx, oldID); err != nil {
		t.Fatal(err)
	}
	newID, err := e.Auth.CreateUser(ctx, "bob", "", pwOf(loginPassword))
	if err != nil {
		t.Fatal(err)
	}
	if roots := e.Core.Roots(files.UserID(newID)); len(roots) != 0 {
		t.Fatalf("a recreated account inherited cached authority: %+v", roots)
	}
	if _, err := e.Core.HomeDirectory(ctx, files.UserID(newID)); err == nil {
		t.Fatal("old private storage was published for the recreated account")
	}
	if _, err := os.Stat(secret); err != nil {
		t.Fatal("refusing adoption destroyed the previous account's files")
	}
}

func enableHomesAndSMB(t *testing.T, base string, cookie *http.Cookie, csrf, root, configDir, socket string) {
	t.Helper()
	for section, body := range map[string]map[string]any{
		"homes": {"enabled": true, "root": root},
		"smb":   {"enabled": true, "config_dir": configDir, "agent_socket": socket},
	} {
		if status, result := mutate(t, http.MethodPatch, base+"/api/v1/admin/settings/"+section, cookie, csrf, body); status != http.StatusOK {
			t.Fatalf("enabling %s: %d %v", section, status, result)
		}
	}
}

func TestNewAccountsHavePrivateSMBHomesBeforeTheirFirstLogin(t *testing.T) {
	t.Parallel()
	base, cookie, csrf, _, _ := adminEngine(t)
	root := filepath.Join(t.TempDir(), "homes")
	configDir := t.TempDir()
	var missing atomic.Bool
	socket := homeSMBAgent(t, configDir, &missing, "")
	enableHomesAndSMB(t, base, cookie, csrf, root, configDir, socket)

	// Existing accounts are prepared without having to visit their file list.
	for _, login := range []string{"root", loginName} {
		if _, err := os.Stat(filepath.Join(root, login)); err != nil {
			t.Fatalf("existing account %s: %v", login, err)
		}
	}
	status, user := mutate(t, http.MethodPost, base+"/api/v1/admin/users", cookie, csrf,
		map[string]any{"login": "carol", "password": loginPassword})
	if status != http.StatusCreated {
		t.Fatalf("creating account: %d %v", status, user)
	}
	home, ok := user["home"].(map[string]any)
	if !ok || !boolField(home, "enabled") || !boolField(home, "ready") {
		t.Fatalf("new account's Home: %v", user)
	}
	if _, err := os.Stat(filepath.Join(root, "carol")); err != nil {
		t.Fatal(err)
	}
	checkPublished := func(wanted bool) {
		t.Helper()
		conf, err := os.ReadFile(filepath.Join(configDir, "smb.conf"))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(conf), "[Home-carol]") != wanted {
			t.Fatalf("Carol's private export has the wrong state: %s", conf)
		}
		if strings.Contains(string(conf), "[Home]\n") {
			t.Fatal("the common Home tree was published")
		}
	}
	checkPublished(true)
	id := stringField(user, "id")
	if status, result := mutate(t, http.MethodPatch, base+"/api/v1/admin/users/"+id, cookie, csrf, map[string]any{"disabled": true}); status != http.StatusOK {
		t.Fatalf("disable: %d %v", status, result)
	}
	checkPublished(false)
	if status, result := mutate(t, http.MethodPatch, base+"/api/v1/admin/users/"+id, cookie, csrf, map[string]any{"disabled": false}); status != http.StatusOK {
		t.Fatalf("enable: %d %v", status, result)
	}
	checkPublished(true)
	if status, result := mutate(t, http.MethodDelete, base+"/api/v1/admin/users/"+id, cookie, csrf, nil); status != http.StatusNoContent {
		t.Fatalf("delete: %d %v", status, result)
	}
	checkPublished(false)
	if _, err := os.Stat(filepath.Join(root, loginName)); err != nil {
		t.Fatal("deleting Carol affected another Home")
	}
	if status, result := mutate(t, http.MethodPatch, base+"/api/v1/admin/settings/homes", cookie, csrf, map[string]any{"enabled": false}); status != http.StatusOK {
		t.Fatalf("Home off: %d %v", status, result)
	}
	conf, err := os.ReadFile(filepath.Join(configDir, "smb.conf"))
	if err != nil || strings.Contains(string(conf), "[Home-") {
		t.Fatal("private SMB exports survived Home disablement")
	}
	if _, err := os.Stat(filepath.Join(root, loginName)); err != nil {
		t.Fatal("switching Home off removed its data")
	}
}

func TestSMBConnectionDetailsArePrivateAndReportMissingMounts(t *testing.T) {
	t.Parallel()
	base, cookie, csrf, ordinary, _ := adminEngine(t)
	root := filepath.Join(t.TempDir(), "homes")
	configDir := t.TempDir()
	var missing atomic.Bool
	socket := homeSMBAgent(t, configDir, &missing, filepath.Join(root, loginName))
	enableHomesAndSMB(t, base, cookie, csrf, root, configDir, socket)
	readFolder := func() map[string]any {
		t.Helper()
		status, body := withCookie(t, http.MethodGet, base+"/api/v1/account/smb", ordinary)
		if status != http.StatusOK {
			t.Fatalf("connections: %d %s", status, body)
		}
		if strings.Contains(string(body), root) || strings.Contains(string(body), "Home-root") {
			t.Fatalf("another account or a disk path escaped: %s", body)
		}
		var view map[string]any
		if err := json.Unmarshal(body, &view); err != nil {
			t.Fatal(err)
		}
		folders, ok := view["folders"].([]any)
		if !ok || len(folders) != 1 {
			t.Fatalf("unexpected folders: %v", view)
		}
		folder, ok := folders[0].(map[string]any)
		if !ok {
			t.Fatalf("unexpected folder: %v", folders[0])
		}
		return folder
	}
	folder := readFolder()
	if !boolField(folder, "available") || !boolField(folder, "personal") || stringField(folder, "share") != "Home-"+loginName {
		t.Fatalf("personal connection: %v", folder)
	}
	missing.Store(true)
	if status, result := mutate(t, http.MethodPost, base+"/api/v1/admin/smb/apply", cookie, csrf, nil); status != http.StatusOK {
		t.Fatalf("apply: %d %v", status, result)
	}
	folder = readFolder()
	if boolField(folder, "available") || stringField(folder, "reason") != "path_not_mounted" {
		t.Fatalf("a missing mount was described as usable: %v", folder)
	}

	// The managed tree is absent from the editable shared-folder resource.
	status, body := withCookie(t, http.MethodGet, base+"/api/v1/admin/shares", cookie)
	if status != http.StatusOK || strings.Contains(string(body), "999999") {
		t.Fatalf("managed Home appeared as an editable share: %d %s", status, body)
	}
}

func TestSMBSharePermissionsFollowGroupMembershipImmediately(t *testing.T) {
	t.Parallel()
	base, cookie, csrf, _, _ := adminEngine(t)
	configDir := t.TempDir()
	var missing atomic.Bool
	socket := homeSMBAgent(t, configDir, &missing, "")
	if status, result := mutate(t, http.MethodPatch, base+"/api/v1/admin/settings/smb", cookie, csrf,
		map[string]any{"enabled": true, "config_dir": configDir, "agent_socket": socket}); status != http.StatusOK {
		t.Fatalf("SMB: %d %v", status, result)
	}
	share := makeShare(t, base, cookie, csrf, "team")
	status, group := mutate(t, http.MethodPost, base+"/api/v1/admin/groups", cookie, csrf, map[string]any{"name": "team"})
	if status != http.StatusCreated {
		t.Fatalf("group: %d %v", status, group)
	}
	groupID := stringField(group, "id")
	if status, result := mutate(t, http.MethodPost, base+"/api/v1/admin/grants", cookie, csrf,
		map[string]any{"group": groupID, "share": share, "allow": []string{"read", "download"}, "inherit": true}); status != http.StatusCreated {
		t.Fatalf("grant: %d %v", status, result)
	}
	checkPublished := func(wanted bool) {
		t.Helper()
		conf, err := os.ReadFile(filepath.Join(configDir, "smb.conf"))
		if err != nil {
			t.Fatal(err)
		}
		for _, section := range agent.Sections(string(conf)) {
			if section.Name != "team" {
				continue
			}
			_, block, ok := strings.Cut(string(conf), "[team]\n")
			if !ok {
				t.Fatal("missing share block")
			}
			line := strings.SplitN(strings.SplitN(block, "valid users = ", 2)[1], "\n", 2)[0]
			if strings.Contains(line, loginName) != wanted {
				t.Fatalf("membership not reflected: %s", line)
			}
			return
		}
		t.Fatal("team share was not published")
	}
	user := accountID(t, base, cookie, loginName)
	checkPublished(false)
	if status, result := mutate(t, http.MethodPost, base+"/api/v1/admin/groups/"+groupID+"/members", cookie, csrf, map[string]any{"user": user}); status != http.StatusNoContent {
		t.Fatalf("add member: %d %v", status, result)
	}
	checkPublished(true)
	if status, result := mutate(t, http.MethodDelete, base+"/api/v1/admin/groups/"+groupID+"/members/"+user, cookie, csrf, nil); status != http.StatusNoContent {
		t.Fatalf("remove member: %d %v", status, result)
	}
	checkPublished(false)
}
