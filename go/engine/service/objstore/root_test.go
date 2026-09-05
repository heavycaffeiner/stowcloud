//go:build linux

package objstore

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/clock"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/secret"
)

// fakeBucket is a minimal, in-memory S3-compatible server: just enough of
// ListObjectsV2, HeadObject, GetObject, PutObject, DeleteObject and
// CopyObject to drive every vfs.Root method this package implements,
// without a live network dependency.
type fakeBucket struct {
	mu      sync.Mutex
	objects map[string][]byte
	mtimes  map[string]time.Time
	clk     clock.Clock
}

func newFakeBucket() *fakeBucket {
	return &fakeBucket{objects: map[string][]byte{}, mtimes: map[string]time.Time{}, clk: fixedTestClock()}
}

// fixedTestClock pins the fake bucket's write timestamps and the Root's own
// signing clock to one instant: these tests read a modification time back
// out of the bucket and compare it against a value they set, and a real
// wall clock would make that comparison depend on what second the test
// happened to run in.
func fixedTestClock() clock.Clock {
	return clock.Fixed(time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))
}

func newFakeS3Server(t *testing.T, bucket string) (*httptest.Server, *fakeBucket) {
	t.Helper()
	fb := newFakeBucket()
	srv := httptest.NewServer(fb.handler(bucket))
	t.Cleanup(srv.Close)
	return srv, fb
}

func (b *fakeBucket) handler(bucket string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			http.Error(w, "missing Authorization", http.StatusForbidden)
			return
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=") {
			http.Error(w, "malformed Authorization", http.StatusForbidden)
			return
		}
		bucketPrefix := "/" + bucket
		if r.URL.Path != bucketPrefix && !strings.HasPrefix(r.URL.Path, bucketPrefix+"/") {
			http.Error(w, "unknown bucket", http.StatusNotFound)
			return
		}
		key := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, bucketPrefix), "/")

		if r.Method == http.MethodGet && r.URL.Query().Get("list-type") == "2" {
			b.handleList(w, r)
			return
		}
		if r.Method == http.MethodPut && r.Header.Get("x-amz-copy-source") != "" {
			b.handleCopy(w, r, key)
			return
		}
		switch r.Method {
		case http.MethodHead:
			b.handleHead(w, key)
		case http.MethodGet:
			b.handleGet(w, key)
		case http.MethodPut:
			b.handlePut(w, r, key)
		case http.MethodDelete:
			b.handleDelete(w, key)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

func (b *fakeBucket) handleList(w http.ResponseWriter, r *http.Request) {
	b.mu.Lock()
	defer b.mu.Unlock()

	q := r.URL.Query()
	prefix := q.Get("prefix")
	delimiter := q.Get("delimiter")
	maxKeys := -1
	if v := q.Get("max-keys"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			maxKeys = n
		}
	}

	keys := make([]string, 0, len(b.objects))
	for k := range b.objects {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)

	var contents []string
	commonPrefixSet := map[string]bool{}
	for _, k := range keys {
		rest := strings.TrimPrefix(k, prefix)
		if delimiter != "" {
			if idx := strings.Index(rest, delimiter); idx >= 0 {
				commonPrefixSet[prefix+rest[:idx+len(delimiter)]] = true
				continue
			}
		}
		contents = append(contents, k)
	}
	commonPrefixes := make([]string, 0, len(commonPrefixSet))
	for cp := range commonPrefixSet {
		commonPrefixes = append(commonPrefixes, cp)
	}
	sort.Strings(commonPrefixes)

	if maxKeys == 0 {
		contents = nil
		commonPrefixes = nil
	}

	var body strings.Builder
	if _, err := body.WriteString(`<?xml version="1.0" encoding="UTF-8"?><ListBucketResult><IsTruncated>false</IsTruncated>`); err != nil {
		panicInfallibleWrite(err)
	}
	for _, k := range contents {
		if _, err := fmt.Fprintf(&body, "<Contents><Key>%s</Key><LastModified>%s</LastModified><Size>%d</Size><ETag>&quot;x&quot;</ETag></Contents>",
			xmlEscape(k), b.mtimes[k].UTC().Format(time.RFC3339), len(b.objects[k])); err != nil {
			panicInfallibleWrite(err)
		}
	}
	for _, cp := range commonPrefixes {
		if _, err := fmt.Fprintf(&body, "<CommonPrefixes><Prefix>%s</Prefix></CommonPrefixes>", xmlEscape(cp)); err != nil {
			panicInfallibleWrite(err)
		}
	}
	if _, err := body.WriteString(`</ListBucketResult>`); err != nil {
		panicInfallibleWrite(err)
	}

	w.Header().Set("Content-Type", "application/xml")
	mustWriteTestResponse(w, []byte(body.String()))
}

func xmlEscape(s string) string {
	var b strings.Builder
	if err := xml.EscapeText(&b, []byte(s)); err != nil {
		panicInfallibleWrite(err)
	}
	return b.String()
}

// mustWriteTestResponse panics if a write to a fake S3 response fails. This
// server runs entirely in process over a loopback connection, so a failed
// write means the test transport itself broke, not something a scenario
// under test can trigger, and a silently truncated fixture response would
// otherwise be far harder for a failing test to explain.
func mustWriteTestResponse(w io.Writer, p []byte) {
	if _, err := w.Write(p); err != nil {
		panic("objstore: fakeBucket: writing test response: " + err.Error())
	}
}

func (b *fakeBucket) handleHead(w http.ResponseWriter, key string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	content, ok := b.objects[key]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(content)))
	w.Header().Set("Last-Modified", b.mtimes[key].UTC().Format(http.TimeFormat))
	w.WriteHeader(http.StatusOK)
}

func (b *fakeBucket) handleGet(w http.ResponseWriter, key string) {
	b.mu.Lock()
	content, ok := b.objects[key]
	b.mu.Unlock()
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		mustWriteTestResponse(w, []byte(`<Error><Code>NoSuchKey</Code><Message>not found</Message></Error>`))
		return
	}
	mustWriteTestResponse(w, content)
}

func (b *fakeBucket) handlePut(w http.ResponseWriter, r *http.Request, key string) {
	data, err := io.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	b.mu.Lock()
	b.objects[key] = data
	b.mtimes[key] = b.clk.Now()
	b.mu.Unlock()
	w.Header().Set("ETag", `"x"`)
	w.WriteHeader(http.StatusOK)
}

func (b *fakeBucket) handleDelete(w http.ResponseWriter, key string) {
	b.mu.Lock()
	delete(b.objects, key)
	delete(b.mtimes, key)
	b.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (b *fakeBucket) handleCopy(w http.ResponseWriter, r *http.Request, destKey string) {
	src := r.Header.Get("x-amz-copy-source")
	parts := strings.SplitN(strings.TrimPrefix(src, "/"), "/", 2)
	if len(parts) != 2 {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	srcKey, err := url.PathUnescape(parts[1])
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	b.mu.Lock()
	content, ok := b.objects[srcKey]
	if ok {
		b.objects[destKey] = content
		b.mtimes[destKey] = b.clk.Now()
	}
	b.mu.Unlock()
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		mustWriteTestResponse(w, []byte(`<Error><Code>NoSuchKey</Code><Message>not found</Message></Error>`))
		return
	}
	w.Header().Set("Content-Type", "application/xml")
	mustWriteTestResponse(w, []byte(`<CopyObjectResult><ETag>"x"</ETag></CopyObjectResult>`))
}

func openTestRoot(t *testing.T, endpoint, bucket, prefix string) *Root {
	t.Helper()
	root, err := Open(context.Background(), Options{
		Share: vfs.ShareID(7),
		Config: Config{
			Endpoint:  endpoint,
			Region:    "us-east-1",
			Bucket:    bucket,
			Prefix:    prefix,
			AccessKey: "AKIAEXAMPLE",
			PathStyle: true,
		},
		Secret:     secret.New([]byte("supersecretkey")),
		ScratchDir: t.TempDir(),
		Policy:     vfs.DefaultSharePolicy(),
		Clock:      fixedTestClock(),
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if cerr := root.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	})
	return root
}

func mustSafePath(t *testing.T, s string) vfs.SafePath {
	t.Helper()
	p, err := vfs.ParseSafePath(s)
	if err != nil {
		t.Fatalf("ParseSafePath(%q): %v", s, err)
	}
	return p
}

// TestRootEndToEnd drives the whole vfs.Root surface against a fake bucket
// in process: write, read back, list, stat, rename, unlink, and the
// directory operations, all through one Root over a configured prefix.
func TestRootEndToEnd(t *testing.T) {
	srv, _ := newFakeS3Server(t, "testbucket")
	root := openTestRoot(t, srv.URL, "testbucket", "team")

	filePath := mustSafePath(t, "hello.txt")
	durable, err := root.WriteDurable(filePath, vfs.DurableOpts{Mode: 0o640}, func(f *vfs.File) error {
		_, werr := f.WriteAt([]byte("hello world"), 0)
		return werr
	})
	if err != nil {
		t.Fatalf("WriteDurable: %v", err)
	}
	if durable.Replaced {
		t.Fatal("Replaced should be false for a brand new file")
	}

	rf, err := root.OpenRead(filePath, vfs.IntentRead)
	if err != nil {
		t.Fatalf("OpenRead: %v", err)
	}
	got := make([]byte, 64)
	n, err := rf.ReadAt(got, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("ReadAt: %v", err)
	}
	if string(got[:n]) != "hello world" {
		t.Fatalf("content = %q, want %q", got[:n], "hello world")
	}
	if err = rf.Close(); err != nil {
		t.Fatalf("close read handle: %v", err)
	}

	entries, err := root.ReadDir(vfs.RootPath(), vfs.HideReserved)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if !hasEntry(entries, "hello.txt", vfs.KindFile) {
		t.Fatalf("ReadDir did not list hello.txt: %+v", entries)
	}

	st, err := root.Stat(filePath)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if st.Size != uint64(len("hello world")) {
		t.Fatalf("Size = %d, want %d", st.Size, len("hello world"))
	}
	if st.Kind != vfs.KindFile {
		t.Fatalf("Kind = %v, want KindFile", st.Kind)
	}

	destPath := mustSafePath(t, "renamed.txt")
	if err := root.Rename(filePath, destPath, false); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if _, err := root.Stat(filePath); !errors.Is(err, vfs.ErrNotFound) {
		t.Fatalf("Stat(old path) = %v, want ErrNotFound", err)
	}
	if st, err := root.Stat(destPath); err != nil || st.Size != uint64(len("hello world")) {
		t.Fatalf("Stat(new path) = %+v, %v", st, err)
	}

	if err := root.Unlink(destPath); err != nil {
		t.Fatalf("Unlink: %v", err)
	}
	if _, err := root.Stat(destPath); !errors.Is(err, vfs.ErrNotFound) {
		t.Fatalf("Stat(after unlink) = %v, want ErrNotFound", err)
	}

	dirPath := mustSafePath(t, "sub")
	if err := root.Mkdir(dirPath); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if st, err := root.Stat(dirPath); err != nil || st.Kind != vfs.KindDir {
		t.Fatalf("Stat(dir) = %+v, %v, want KindDir", st, err)
	}
	if err := root.Mkdir(dirPath); !errors.Is(err, vfs.ErrExists) {
		t.Fatalf("Mkdir(existing) = %v, want ErrExists", err)
	}
	if err := root.Rmdir(dirPath); err != nil {
		t.Fatalf("Rmdir: %v", err)
	}
	if _, err := root.Stat(dirPath); !errors.Is(err, vfs.ErrNotFound) {
		t.Fatalf("Stat(after rmdir) = %v, want ErrNotFound", err)
	}

	if err := root.Alive(); err != nil {
		t.Fatalf("Alive: %v", err)
	}
}

func TestRootRenameDirectoryMovesEveryChild(t *testing.T) {
	srv, _ := newFakeS3Server(t, "bucket-dir")
	root := openTestRoot(t, srv.URL, "bucket-dir", "")

	for _, name := range []string{"docs/a.txt", "docs/b.txt", "docs/sub/c.txt"} {
		p := mustSafePath(t, name)
		if _, err := root.WriteDurable(p, vfs.DurableOpts{}, func(f *vfs.File) error {
			_, err := f.WriteAt([]byte(name), 0)
			return err
		}); err != nil {
			t.Fatalf("WriteDurable(%q): %v", name, err)
		}
	}

	if err := root.Rename(mustSafePath(t, "docs"), mustSafePath(t, "archive"), false); err != nil {
		t.Fatalf("Rename directory: %v", err)
	}
	for _, name := range []string{"archive/a.txt", "archive/b.txt", "archive/sub/c.txt"} {
		if _, err := root.Stat(mustSafePath(t, name)); err != nil {
			t.Fatalf("Stat(%q) after rename: %v", name, err)
		}
	}
	if _, err := root.Stat(mustSafePath(t, "docs")); !errors.Is(err, vfs.ErrNotFound) {
		t.Fatalf("Stat(old dir) = %v, want ErrNotFound", err)
	}
}

func TestRootRmdirRefusesANonEmptyDirectory(t *testing.T) {
	srv, _ := newFakeS3Server(t, "bucket-rmdir")
	root := openTestRoot(t, srv.URL, "bucket-rmdir", "")

	p := mustSafePath(t, "docs/a.txt")
	if _, err := root.WriteDurable(p, vfs.DurableOpts{}, func(f *vfs.File) error {
		_, err := f.WriteAt([]byte("x"), 0)
		return err
	}); err != nil {
		t.Fatalf("WriteDurable: %v", err)
	}
	if err := root.Rmdir(mustSafePath(t, "docs")); !errors.Is(err, vfs.ErrNotEmpty) {
		t.Fatalf("Rmdir(non-empty) = %v, want ErrNotEmpty", err)
	}
}

// TestReadDirListsAPrefixWithChildrenButNoMarker proves the directory model
// documented on this package: another tool that wrote a key under "a/"
// without ever creating the "a/" marker object still makes "a" list as a
// directory.
func TestReadDirListsAPrefixWithChildrenButNoMarker(t *testing.T) {
	srv, fb := newFakeS3Server(t, "bucket-nomarker")
	fb.objects["a/b.txt"] = []byte("x")
	fb.mtimes["a/b.txt"] = fb.clk.Now()

	root := openTestRoot(t, srv.URL, "bucket-nomarker", "")
	entries, err := root.ReadDir(vfs.RootPath(), vfs.HideReserved)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if !hasEntry(entries, "a", vfs.KindDir) {
		t.Fatalf("ReadDir did not list the markerless directory %q: %+v", "a", entries)
	}
}

// TestReadDirHidesADirectoryMarkerFromItsParent proves the zero-byte marker
// object never shows up as a file entry beside its own directory.
func TestReadDirHidesADirectoryMarkerFromItsParent(t *testing.T) {
	srv, fb := newFakeS3Server(t, "bucket-marker")
	fb.objects["a/"] = []byte{}
	fb.mtimes["a/"] = fb.clk.Now()
	fb.objects["a/child.txt"] = []byte("x")
	fb.mtimes["a/child.txt"] = fb.clk.Now()

	root := openTestRoot(t, srv.URL, "bucket-marker", "")
	entries, err := root.ReadDir(vfs.RootPath(), vfs.HideReserved)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if hasEntry(entries, "a", vfs.KindFile) {
		t.Fatalf("ReadDir showed the directory marker as a file: %+v", entries)
	}
	if !hasEntry(entries, "a", vfs.KindDir) {
		t.Fatalf("ReadDir did not list %q as a directory: %+v", "a", entries)
	}

	childEntries, err := root.ReadDir(mustSafePath(t, "a"), vfs.HideReserved)
	if err != nil {
		t.Fatalf("ReadDir(a): %v", err)
	}
	if hasEntry(childEntries, "", vfs.KindFile) {
		t.Fatalf("ReadDir(a) exposed its own marker: %+v", childEntries)
	}
	if !hasEntry(childEntries, "child.txt", vfs.KindFile) {
		t.Fatalf("ReadDir(a) did not list child.txt: %+v", childEntries)
	}
}

// A directory this server created has a marker object, and the marker's own
// modification time is the directory's: without it every folder in an S3
// share renders as the epoch, which reads as a broken row rather than as
// "object storage has no directory timestamps". A prefix that exists only
// because something wrote a file beneath it genuinely has no time of its
// own, and inventing one would be worse than reporting none.
func TestStatReportsADirectoryTimeFromItsMarkerAndNoneWithout(t *testing.T) {
	srv, fb := newFakeS3Server(t, "bucket-dirtime")
	marked := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	fb.objects["marked/"] = []byte{}
	fb.mtimes["marked/"] = marked
	fb.objects["bare/file.txt"] = []byte("x")
	fb.mtimes["bare/file.txt"] = fb.clk.Now()

	root := openTestRoot(t, srv.URL, "bucket-dirtime", "")

	st, err := root.Stat(mustSafePath(t, "marked"))
	if err != nil {
		t.Fatalf("Stat(marked): %v", err)
	}
	if !st.Kind.IsDir() {
		t.Fatalf("Stat(marked) reported kind %v", st.Kind)
	}
	if st.MtimeNs != marked.UnixNano() {
		t.Errorf("Stat(marked) reported mtime %d, want the marker's %d", st.MtimeNs, marked.UnixNano())
	}

	bare, err := root.Stat(mustSafePath(t, "bare"))
	if err != nil {
		t.Fatalf("Stat(bare): %v", err)
	}
	if !bare.Kind.IsDir() {
		t.Fatalf("Stat(bare) reported kind %v", bare.Kind)
	}
	if bare.MtimeNs != 0 {
		t.Errorf("Stat(bare) invented mtime %d for a markerless prefix", bare.MtimeNs)
	}
}

// The upload engine syncs and closes the descriptor CreatePart handed it
// before it publishes, which is exactly what a local share wants: a publish
// there is a rename by name. Reading the staged bytes back through that
// closed descriptor answered "bad file descriptor" and lost every upload
// into a bucket, with the client told the transfer had succeeded.
func TestPublishPartWorksAfterTheCallerClosesItsHandle(t *testing.T) {
	srv, fb := newFakeS3Server(t, "bucket-publish")
	root := openTestRoot(t, srv.URL, "bucket-publish", "team")

	part := mustSafePath(t, ".sc-upload-part")
	f, err := root.CreatePart(part)
	if err != nil {
		t.Fatalf("CreatePart: %v", err)
	}
	body := []byte("staged bytes")
	if _, err := f.WriteAt(body, 0); err != nil {
		t.Fatalf("WriteAt: %v", err)
	}
	if err := f.SyncData(); err != nil {
		t.Fatalf("SyncData: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	dest := mustSafePath(t, "published.txt")
	if _, err := root.PublishPart(part, dest, false); err != nil {
		t.Fatalf("PublishPart after the handle closed: %v", err)
	}

	fb.mu.Lock()
	stored, ok := fb.objects["team/published.txt"]
	fb.mu.Unlock()
	if !ok {
		t.Fatalf("the published object is not in the bucket")
	}
	if string(stored) != string(body) {
		t.Errorf("the published object is %q, want %q", stored, body)
	}
}

func hasEntry(entries []vfs.DirEntry, name string, kind vfs.Kind) bool {
	for _, e := range entries {
		if e.Name == name && e.Kind == kind {
			return true
		}
	}
	return false
}
