package consumers_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	durablefs "github.com/stowcloud/durablefs"
	search "github.com/stowcloud/namesearch"
	"github.com/stowcloud/namesearch/index"
	sandboxworker "github.com/stowcloud/sandbox-worker"
	storage "github.com/stowcloud/storage"
	"github.com/stowcloud/transfer"
	"github.com/stowcloud/veracrypt"
)

func TestDurableFSReleasedContract(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state")
	if err := os.WriteFile(path, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := durablefs.ReplaceFileDurable(path, 0o600, func(f *os.File) error { _, e := f.WriteString("new\n"); return e })
	if err != nil || result.Outcome != durablefs.Published {
		t.Fatalf("replace result = %#v, %v", result, err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "new\n" {
		t.Fatalf("published content = %q, %v", got, err)
	}
	wantErr := errors.New("writer failed")
	result, err = durablefs.ReplaceFileDurable(path, 0o600, func(f *os.File) error {
		if _, e := f.WriteString("partial\n"); e != nil {
			return e
		}
		return wantErr
	})
	if !errors.Is(err, wantErr) || result.Outcome != durablefs.NotPublished {
		t.Fatalf("failed replace = %#v, %v", result, err)
	}
	got, err = os.ReadFile(path)
	if err != nil || string(got) != "new\n" {
		t.Fatalf("failed replace changed content = %q, %v", got, err)
	}
}

type memoryReader struct {
	entries map[string][]search.Entry
	stats   map[string]search.Stat
}

func (r memoryReader) ReadDir(path string, visit func(search.Entry) bool) error {
	for _, entry := range r.entries[path] {
		if !visit(entry) {
			break
		}
	}
	return nil
}

func (r memoryReader) Stat(path string) (search.Stat, error) {
	stat, ok := r.stats[path]
	if !ok {
		return search.Stat{}, os.ErrNotExist
	}
	return stat, nil
}

func TestNameSearchReleasedContract(t *testing.T) {
	dir := t.TempDir()
	ix, err := index.Open(dir, index.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := ix.Append([]index.Entry{{Namespace: 7, Path: "docs/report.pdf"}, {Namespace: 7, Path: "notes.txt"}}); err != nil {
		t.Fatal(err)
	}
	iterator, plan, err := ix.Candidates(index.CandidateQuery{Needle: []byte("report")})
	if err != nil || iterator != nil || plan.Fallback != index.FallbackIncomplete {
		t.Fatalf("incomplete candidate plan = %v, %+v, %v", iterator, plan, err)
	}
	token := ix.BeginCoverage()
	if !ix.CompleteCoverage(token) {
		t.Fatal("coverage completion failed")
	}
	iterator, plan, err = ix.Candidates(index.CandidateQuery{Needle: []byte("report")})
	if err != nil || iterator == nil || plan.Fallback != index.FallbackNone {
		t.Fatalf("complete candidate plan = %v, %+v, %v", iterator, plan, err)
	}
	candidate, err := iterator.Next(context.Background())
	if err != nil || candidate.Path != "docs/report.pdf" {
		t.Fatalf("candidate = %+v, %v", candidate, err)
	}
	if candidate.Name != "report.pdf" {
		t.Fatalf("candidate name = %q", candidate.Name)
	}

	if err := ix.Merge(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	reopened, err := index.Open(dir, index.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	result, err := reopened.Query([]byte("report"), 0)
	if err != nil || result.MustFallBack() || len(result.Hits) != 1 || result.Hits[0].Path != "docs/report.pdf" {
		t.Fatalf("reopened query = %+v, %v", result, err)
	}
	base := filepath.Join(dir, "base.idx")
	data, err := os.ReadFile(base)
	if err != nil {
		t.Fatal(err)
	}
	data[0] ^= 0xff
	if err := os.WriteFile(base, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := index.Open(dir, index.DefaultConfig()); !errors.Is(err, index.ErrIndexCorrupt) {
		t.Fatalf("corrupt index error = %v", err)
	}
}

type backend struct{}

func (backend) Stat(context.Context, storage.Path) (storage.Entry, error) {
	return storage.Entry{Path: storage.RootPath(), Kind: storage.KindDirectory}, nil
}
func (backend) ReadDir(context.Context, storage.Path) ([]storage.Entry, error) { return nil, nil }
func (backend) OpenRead(context.Context, storage.Path) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(nil)), nil
}

func TestStorageReleasedContract(t *testing.T) {
	path, err := storage.ParsePath("photos/2026/image.jpg")
	if err != nil || path.Name() != "image.jpg" || !path.Under(path.Parent()) {
		t.Fatalf("path = %q, %v", path, err)
	}
	if _, err := storage.ParsePath("x/../y"); !errors.Is(err, storage.ErrInvalidComponent) {
		t.Fatalf("invalid path error = %v", err)
	}
	var hierarchy storage.ReadHierarchy = backend{}
	if _, err := hierarchy.Stat(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	var releaseCalls int
	lease := storage.NewMaterialized(bytes.NewReader([]byte("abc")), 3, func() error { releaseCalls++; return nil })
	if _, err := lease.ReadAt(make([]byte, 1), 0); err != nil {
		t.Fatal(err)
	}
	if err := lease.Release(); err != nil || lease.Release() != nil || releaseCalls != 1 {
		t.Fatalf("lease release = %v, calls=%d", err, releaseCalls)
	}
}

type byteReader struct{ b []byte }

func (r *byteReader) Read(p []byte) (int, error) {
	if len(r.b) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.b)
	r.b = r.b[n:]
	return n, nil
}

func TestTransferReleasedContract(t *testing.T) {
	set := transfer.NewIntervalSet(transfer.WithMaxRuns(2))
	if err := set.Insert(10, 20); err != nil {
		t.Fatal(err)
	}
	if err := set.Insert(0, 10); err != nil || set.ContiguousPrefix() != 20 {
		t.Fatalf("interval prefix = %d, %v", set.ContiguousPrefix(), err)
	}
	intent := transfer.Intent{Operation: "consumer-op", Destination: "bucket/key", Content: "consumer-content"}
	fake := transfer.NewS3Fake()
	uploadID, err := fake.BeginMultipart(context.Background(), intent, 5, "")
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("hello")
	sum := sha256.Sum256(content)
	digest := hex.EncodeToString(sum[:])
	part, err := fake.UploadPart(context.Background(), intent, uploadID, 1, &byteReader{b: append([]byte(nil), content...)}, uint64(len(content)), digest)
	if err != nil {
		t.Fatal(err)
	}
	result, receipt, err := fake.CompleteMultipart(context.Background(), intent, uploadID, []transfer.S3Part{part}, digest)
	if err != nil || result.Outcome != transfer.Published || receipt.Destination != intent.Destination {
		t.Fatalf("completion = %#v, %#v, %v", result, receipt, err)
	}
	if err := transfer.VerifyWholeFile(content, digest); err != nil {
		t.Fatal(err)
	}
	if err := transfer.VerifyWholeFile(content, "bad"); err == nil {
		t.Fatal("accepted wrong whole-file digest")
	}
	id, err := transfer.NewSessionID()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := transfer.ParseSessionID(id.String())
	if err != nil || parsed != id {
		t.Fatalf("session ID roundtrip = %v, %v", parsed, err)
	}
	fromDisk, err := transfer.SessionIDFromBytes(id.Bytes())
	if err != nil || fromDisk != id {
		t.Fatalf("persisted session ID = %v, %v", fromDisk, err)
	}
	if _, err := transfer.ParseSessionID(id.String() + "="); err == nil {
		t.Fatal("accepted padded session ID")
	}
	state, err := transfer.Transition(transfer.StateReceiving, transfer.StateFinalizing)
	if err != nil || state != transfer.StateFinalizing {
		t.Fatalf("publication transition = %v, %v", state, err)
	}
	if _, err := transfer.Transition(transfer.StateDone, transfer.StateReceiving); err == nil {
		t.Fatal("resumed a published session")
	}
}

func TestVeraCryptReleasedContract(t *testing.T) {
	path := filepath.Join(t.TempDir(), "volume.hc")
	if err := veracrypt.Create(path, 16, []byte("consumer password")); err != nil {
		t.Fatal(err)
	}
	container, size, err := veracrypt.Open(path, []byte("consumer password"), 0, "sha512")
	if err != nil {
		t.Fatal(err)
	}
	defer container.Close()
	if size == 0 {
		t.Fatal("zero data size")
	}
	if _, _, err := veracrypt.Open(path, []byte("wrong password"), 0, "sha512"); !errors.Is(err, veracrypt.ErrWrongPassword) {
		t.Fatalf("wrong password error = %v", err)
	}
}

func TestSandboxWorkerReleasedContract(t *testing.T) {
	left, right, err := sandboxworker.SocketPair()
	if err != nil {
		t.Fatal(err)
	}
	defer left.Close()
	defer right.Close()
	want := []byte("consumer frame")
	if err := sandboxworker.SendMessage(left, want); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, sandboxworker.MaxMessageSize)
	n, files, err := sandboxworker.RecvMessage(right, buf, 0)
	if err != nil {
		t.Fatal(err)
	}
	sandboxworker.CloseFiles(files)
	if !bytes.Equal(buf[:n], want) {
		t.Fatalf("payload = %q, want %q", buf[:n], want)
	}
	if err := sandboxworker.SendMessage(left, make([]byte, sandboxworker.MaxMessageSize+1)); err == nil {
		t.Fatal("accepted oversized message")
	}
}
