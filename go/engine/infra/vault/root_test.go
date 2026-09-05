package vault

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/secret"
)

func openTestRoot(t *testing.T, containerPath string, create bool, sizeMiB uint64) *Root {
	t.Helper()
	scratchDir := t.TempDir()
	root, err := Open(context.Background(), Options{
		Share:      vfs.ShareID(1),
		Config:     Config{Container: containerPath, CreateSizeMiB: sizeMiB},
		Password:   secret.New([]byte("end to end test password")),
		Create:     create,
		ScratchDir: scratchDir,
		Policy:     vfs.DefaultSharePolicy(),
	})
	if err != nil {
		t.Fatalf("Open(create=%v): %v", create, err)
	}
	return root
}

func TestRootEndToEnd(t *testing.T) {
	dir := t.TempDir()
	containerPath := filepath.Join(dir, "share.hc")

	root := openTestRoot(t, containerPath, true, minContainerDataMiB)

	if root.ID() != vfs.ShareID(1) {
		t.Fatalf("ID() = %v, want 1", root.ID())
	}
	if root.IsScratch() {
		t.Fatalf("IsScratch() = true, want false")
	}
	if root.HasBtime() {
		t.Fatalf("HasBtime() = true, want false")
	}
	if err := root.Alive(); err != nil {
		t.Fatalf("Alive: %v", err)
	}

	docsDir, err := vfs.RootPath().Join("docs")
	if err != nil {
		t.Fatalf("Join docs: %v", err)
	}
	if err := root.Mkdir(docsDir); err != nil {
		t.Fatalf("Mkdir docs: %v", err)
	}
	filePath, err := docsDir.Join("report.txt")
	if err != nil {
		t.Fatalf("Join report.txt: %v", err)
	}
	content := []byte("quarterly report content, written through vfs.Root")
	durable, err := root.WriteDurable(filePath, vfs.DurableOpts{Mode: 0o664}, func(f *vfs.File) error {
		_, err := f.WriteAt(content, 0)
		return err
	})
	if err != nil {
		t.Fatalf("WriteDurable: %v", err)
	}
	if durable.Replaced {
		t.Fatalf("first write reported Replaced = true")
	}

	st, err := root.Stat(filePath)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if st.Size != uint64(len(content)) {
		t.Fatalf("Stat size = %d, want %d", st.Size, len(content))
	}
	if st.Kind != vfs.KindFile {
		t.Fatalf("Stat kind = %v, want file", st.Kind)
	}
	if st.BtimeNs != nil {
		t.Fatalf("Stat BtimeNs = %v, want nil", st.BtimeNs)
	}
	if st.Dev != root.Dev() {
		t.Fatalf("Stat Dev = %d, want Root.Dev() = %d", st.Dev, root.Dev())
	}

	readHandle, err := root.OpenRead(filePath, vfs.IntentRead)
	if err != nil {
		t.Fatalf("OpenRead: %v", err)
	}
	gotContent := make([]byte, len(content))
	if _, err := io.ReadFull(readHandle.OSFile(), gotContent); err != nil {
		t.Fatalf("read materialized file: %v", err)
	}
	if err := readHandle.Close(); err != nil {
		t.Fatalf("close materialized file: %v", err)
	}
	if !bytes.Equal(gotContent, content) {
		t.Fatalf("materialized content = %q, want %q", gotContent, content)
	}

	entries, err := root.ReadDir(docsDir, vfs.HideReserved)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "report.txt" {
		t.Fatalf("ReadDir docs = %+v, want exactly report.txt", entries)
	}

	if err := root.SetTimes(filePath, 1_600_000_000_000_000_000); err != nil {
		t.Fatalf("SetTimes: %v", err)
	}
	st, err = root.Stat(filePath)
	if err != nil {
		t.Fatalf("Stat after SetTimes: %v", err)
	}
	// FAT keeps 2-second resolution local time, so the round trip is bounded,
	// not exact.
	if diff := st.MtimeNs - 1_600_000_000_000_000_000; diff < -2_000_000_000 || diff > 2_000_000_000 {
		t.Fatalf("MtimeNs after SetTimes = %d, too far from requested value", st.MtimeNs)
	}

	movedPath, err := vfs.RootPath().Join("moved.txt")
	if err != nil {
		t.Fatalf("Join moved.txt: %v", err)
	}
	if err := root.Rename(filePath, movedPath, true); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if _, err := root.Stat(filePath); !errors.Is(err, vfs.ErrNotFound) {
		t.Fatalf("Stat(old path) after rename = %v, want ErrNotFound", err)
	}

	// CreatePart / PublishPart: the upload engine's two-phase write.
	partDest, err := vfs.RootPath().Join("uploaded.bin")
	if err != nil {
		t.Fatalf("Join uploaded.bin: %v", err)
	}
	partHandle, err := root.CreatePart(partDest)
	if err != nil {
		t.Fatalf("CreatePart: %v", err)
	}
	partContent := bytes.Repeat([]byte("part"), 4096)
	if _, err := partHandle.WriteAt(partContent, 0); err != nil {
		t.Fatalf("write part: %v", err)
	}
	if err := partHandle.SyncData(); err != nil {
		t.Fatalf("sync part: %v", err)
	}
	if err := partHandle.Close(); err != nil {
		t.Fatalf("close part: %v", err)
	}
	if _, err := root.PublishPart(partDest, partDest, false); err != nil {
		t.Fatalf("PublishPart: %v", err)
	}
	published, err := root.OpenRead(partDest, vfs.IntentRead)
	if err != nil {
		t.Fatalf("OpenRead published part: %v", err)
	}
	gotPart := make([]byte, len(partContent))
	if _, err := io.ReadFull(published.OSFile(), gotPart); err != nil {
		t.Fatalf("read published part: %v", err)
	}
	if err := published.Close(); err != nil {
		t.Fatalf("close published part: %v", err)
	}
	if !bytes.Equal(gotPart, partContent) {
		t.Fatalf("published part content mismatch")
	}

	space, err := root.Space(vfs.RootPath())
	if err != nil {
		t.Fatalf("Space: %v", err)
	}
	if space.Total == 0 || space.Free > space.Total {
		t.Fatalf("Space = %+v, out of range", space)
	}

	if err := root.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Reopening without Create must find the same content still there.
	root2 := openTestRoot(t, containerPath, false, 0)
	defer func() { _ = root2.Close() }()
	st2, err := root2.Stat(movedPath)
	if err != nil {
		t.Fatalf("Stat(movedPath) after reopen: %v", err)
	}
	if st2.Size != uint64(len(content)) {
		t.Fatalf("size after reopen = %d, want %d", st2.Size, len(content))
	}
}

func TestOpenWrongPasswordRefused(t *testing.T) {
	dir := t.TempDir()
	containerPath := filepath.Join(dir, "share.hc")
	root := openTestRoot(t, containerPath, true, minContainerDataMiB)
	if err := root.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	_, err := Open(context.Background(), Options{
		Share:      vfs.ShareID(1),
		Config:     Config{Container: containerPath},
		Password:   secret.New([]byte("not the right password")),
		Create:     false,
		ScratchDir: t.TempDir(),
		Policy:     vfs.DefaultSharePolicy(),
	})
	if !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("Open with wrong password = %v, want ErrWrongPassword", err)
	}
}
