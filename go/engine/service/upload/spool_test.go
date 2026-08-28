//go:build linux

package upload

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/limits"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/task"
)

// chunkOf is a body of n bytes whose content is derived from its offset, so
// an assembled file can be checked against what was actually sent.
func chunkOf(off uint64, n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte((off + uint64(i)) % 251)
	}
	return b
}

func TestChunksLandAtTheirOffsetsAndTheOffsetAdvances(t *testing.T) {
	f := newFixture(t)
	const chunk = limits.UploadChunkFloor
	total := uint64(chunk * 3)
	s := f.create(t, "three.bin", total, SessionSpec{})

	for i := 0; i < 3; i++ {
		off := uint64(i * chunk)
		if got := f.patch(t, s.ID, off, chunkOf(off, chunk)); got != off+chunk {
			t.Fatalf("the resumable offset after chunk %d is %d, want %d", i, got, off+chunk)
		}
	}
	got, err := f.engine.Get(context.Background(), s.ID, testUser)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Offset != total || got.Received != total {
		t.Fatalf("the session is %+v", got)
	}
}

// A session that is not random-access has an ordering rule, and the refusal
// carries the offset the client should have written at, so a resuming client
// does not need a second round trip to find out.
func TestAChunkOutOfOrderIsRefusedWithTheExpectedOffset(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	const chunk = limits.UploadChunkFloor
	s := f.create(t, "ordered.bin", uint64(chunk*3), SessionSpec{})
	f.patch(t, s.ID, 0, chunkOf(0, chunk))

	_, err := f.engine.PatchAt(ctx, f.root(t), s.ID, testUser, uint64(chunk*2),
		bytes.NewReader(chunkOf(uint64(chunk*2), chunk)), nil)
	var conflict *ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("an out-of-order chunk returned %v", err)
	}
	if conflict.Expected != chunk || conflict.Got != chunk*2 {
		t.Fatalf("the refusal reads %+v", conflict)
	}

	// A random-access session takes the same chunk, which is the whole
	// difference between the two.
	ra := f.create(t, "random.bin", uint64(chunk*3), SessionSpec{RandomAccess: true})
	if _, rerr := f.engine.PatchAt(ctx, f.root(t), ra.ID, testUser, uint64(chunk*2),
		bytes.NewReader(chunkOf(uint64(chunk*2), chunk)), nil); rerr != nil {
		t.Fatalf("a random-access chunk past the prefix returned %v", rerr)
	}
}

// Received and Offset diverge once a client writes past a hole: one is what a
// resume needs and the other is what a progress bar shows.
func TestReceivedAndOffsetDivergeAfterAHole(t *testing.T) {
	f := newFixture(t)
	const chunk = limits.UploadChunkFloor
	s := f.create(t, "holed.bin", uint64(chunk*3), SessionSpec{RandomAccess: true})

	f.patch(t, s.ID, uint64(chunk*2), chunkOf(uint64(chunk*2), chunk))
	got, err := f.engine.Get(context.Background(), s.ID, testUser)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Offset != 0 {
		t.Fatalf("the resumable offset is %d, want 0 with nothing at the front", got.Offset)
	}
	if got.Received != chunk {
		t.Fatalf("received is %d, want %d", got.Received, chunk)
	}
}

// The declared length is enforced as the bytes arrive rather than from a
// header, because a header is a claim and this is the stream.
func TestABodyPastTheDeclaredLengthIsRefused(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	s := f.create(t, "small.bin", 10, SessionSpec{})

	_, err := f.engine.PatchAt(ctx, f.root(t), s.ID, testUser, 0,
		bytes.NewReader(bytes.Repeat([]byte("a"), 100)), nil)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("a body past the declared length returned %v", err)
	}
	// A chunk starting past the end is refused too.
	if _, oerr := f.engine.PatchAt(ctx, f.root(t), s.ID, testUser, 50,
		bytes.NewReader([]byte("a")), nil); oerr == nil {
		t.Fatal("a chunk beginning past the declared length was accepted")
	}
}

// The floor exempts the last chunk and a whole file smaller than it: neither
// can be made bigger.
func TestTheFloorExemptsTheLastChunkAndASmallFile(t *testing.T) {
	f := newFixture(t)
	const chunk = limits.UploadChunkFloor

	s := f.create(t, "tail.bin", uint64(chunk)+10, SessionSpec{})
	f.patch(t, s.ID, 0, chunkOf(0, chunk))
	// Ten bytes, well under the floor, and the last of the file.
	f.patch(t, s.ID, uint64(chunk), chunkOf(uint64(chunk), 10))

	tiny := f.create(t, "tiny.bin", 3, SessionSpec{})
	f.patch(t, tiny.ID, 0, []byte("abc"))
}

// A chunk that fails its checksum leaves the set untouched, so the client
// resends the same range rather than resuming past a hole it believes is
// filled.
func TestAFailedChecksumLeavesTheIntervalSetUnrecorded(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	const chunk = limits.UploadChunkFloor
	s := f.create(t, "summed.bin", uint64(chunk*2), SessionSpec{})

	body := chunkOf(0, chunk)
	wrong, err := Sum(AlgoCRC32C, []byte("not these bytes"))
	if err != nil {
		t.Fatalf("Sum: %v", err)
	}
	_, err = f.engine.PatchAt(ctx, f.root(t), s.ID, testUser, 0,
		bytes.NewReader(body), &Checksum{Algo: AlgoCRC32C, Digest: wrong})
	if !errors.Is(err, ErrChecksum) {
		t.Fatalf("a wrong digest returned %v", err)
	}
	got, err := f.engine.Get(ctx, s.ID, testUser)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Offset != 0 || got.Received != 0 {
		t.Fatalf("a failed checksum recorded a range: %+v", got)
	}

	// The resend of the same range with the right digest lands.
	right, err := Sum(AlgoCRC32C, body)
	if err != nil {
		t.Fatalf("Sum: %v", err)
	}
	if _, perr := f.engine.PatchAt(ctx, f.root(t), s.ID, testUser, 0,
		bytes.NewReader(body), &Checksum{Algo: AlgoCRC32C, Digest: right}); perr != nil {
		t.Fatalf("the resend returned %v", perr)
	}
}

// The name-ordered path verifies a digest too, on both of its branches: the
// chunk that lands directly on the part file and the one that is spooled to
// wait for its predecessor.
//
// Without this the two spool modes disagreed about a corrupt chunk. The
// offset-addressed path refused it and a name-ordered upload of the same bytes
// accepted it, so a client sending checksums got them checked or ignored
// depending on a mode it chose for unrelated reasons.
func TestAFailedChecksumIsRefusedOnBothNamedBranches(t *testing.T) {
	ctx := context.Background()
	const chunk = limits.UploadChunkFloor

	wrong, err := Sum(AlgoCRC32C, []byte("not these bytes"))
	if err != nil {
		t.Fatalf("Sum: %v", err)
	}

	// Name 1 is what the assembly waits for, so it appends to the part file.
	// Name 2 arrives before its predecessor and is spooled.
	for _, c := range []struct {
		what string
		name uint32
	}{
		{"appended directly", 1},
		{"spooled", 2},
	} {
		t.Run(c.what, func(t *testing.T) {
			f := newFixture(t)
			s := f.create(t, "named-sum.bin", uint64(chunk*3), SessionSpec{Mode: SpoolNameOrdered})
			off := uint64(c.name-1) * chunk
			body := chunkOf(off, chunk)

			perr := f.engine.PutNamed(ctx, f.root(t), s.ID, testUser, c.name,
				bytes.NewReader(body), &Checksum{Algo: AlgoCRC32C, Digest: wrong})
			if !errors.Is(perr, ErrChecksum) {
				t.Fatalf("a wrong digest on the %s chunk returned %v", c.what, perr)
			}

			// Nothing is recorded, so the client resends this same name.
			names, lerr := f.engine.ListChunks(ctx, s.ID, testUser)
			if lerr != nil {
				t.Fatalf("ListChunks: %v", lerr)
			}
			if len(names) != 0 {
				t.Fatalf("a failed digest recorded the chunk list %v", names)
			}

			right, serr := Sum(AlgoCRC32C, body)
			if serr != nil {
				t.Fatalf("Sum: %v", serr)
			}
			if rerr := f.engine.PutNamed(ctx, f.root(t), s.ID, testUser, c.name,
				bytes.NewReader(body), &Checksum{Algo: AlgoCRC32C, Digest: right}); rerr != nil {
				t.Fatalf("the resend returned %v", rerr)
			}
			names, lerr = f.engine.ListChunks(ctx, s.ID, testUser)
			if lerr != nil {
				t.Fatalf("ListChunks after the resend: %v", lerr)
			}
			if len(names) != 1 {
				t.Fatalf("after the resend the chunk list is %v", names)
			}
		})
	}
}

// blockingReader stalls until it is released, so a test can hold one chunk
// open while another runs.
type blockingReader struct {
	body     []byte
	released chan struct{}
	started  chan struct{}
	once     sync.Once
	at       int
}

func (r *blockingReader) Read(p []byte) (int, error) {
	r.once.Do(func() { close(r.started) })
	<-r.released
	if r.at >= len(r.body) {
		return 0, io.EOF
	}
	n := copy(p, r.body[r.at:])
	r.at += n
	return n, nil
}

// The row lock covers the bookkeeping and never the body. Holding it across
// the body read serialized concurrent chunks, and over a multiplexed
// connection that deadlocks rather than queues: every upload stopped after
// its first chunk.
func TestConcurrentChunksDoNotSerialize(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	const chunk = limits.UploadChunkFloor
	s := f.create(t, "parallel.bin", uint64(chunk*2), SessionSpec{RandomAccess: true})

	stalled := &blockingReader{
		body:     chunkOf(0, chunk),
		released: make(chan struct{}),
		started:  make(chan struct{}),
	}
	firstDone := make(chan error, 1)
	task.Go(ctx, "upload: stalled chunk", func() {
		_, err := f.engine.PatchAt(ctx, f.root(t), s.ID, testUser, 0, stalled, nil)
		firstDone <- err
	})
	<-stalled.started

	// The second chunk has to complete while the first is stalled mid-body.
	secondDone := make(chan error, 1)
	task.Go(ctx, "upload: second chunk", func() {
		_, err := f.engine.PatchAt(ctx, f.root(t), s.ID, testUser, uint64(chunk),
			bytes.NewReader(chunkOf(uint64(chunk), chunk)), nil)
		secondDone <- err
	})

	select {
	case err := <-secondDone:
		if err != nil {
			t.Fatalf("the second chunk returned %v", err)
		}
	case <-time.After(10 * time.Second):
		close(stalled.released)
		t.Fatal("the second chunk waited for the first: the row lock is covering the body")
	}

	close(stalled.released)
	if err := <-firstDone; err != nil {
		t.Fatalf("the stalled chunk returned %v", err)
	}
}

func TestNamedChunksAssembleInNameOrder(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	const chunk = limits.UploadChunkFloor
	s := f.create(t, "named.bin", uint64(chunk*3), SessionSpec{Mode: SpoolNameOrdered})

	// Sent out of order: two and three wait for one.
	for _, name := range []uint32{2, 3, 1} {
		off := uint64(name-1) * chunk
		if err := f.engine.PutNamed(ctx, f.root(t), s.ID, testUser, name,
			bytes.NewReader(chunkOf(off, chunk)), nil); err != nil {
			t.Fatalf("PutNamed(%d): %v", name, err)
		}
	}
	names, err := f.engine.ListChunks(ctx, s.ID, testUser)
	if err != nil {
		t.Fatalf("ListChunks: %v", err)
	}
	if len(names) != 3 {
		t.Fatalf("the chunk list is %v", names)
	}

	entry, err := f.engine.Assemble(ctx, f.resolve(t, "named.bin"), s.ID, uint64(chunk*3), nil)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if entry.Size != uint64(chunk*3) {
		t.Fatalf("the published entry is %d bytes", entry.Size)
	}

	// The assembled bytes are the chunks in name order.
	want := append(append(chunkOf(0, chunk), chunkOf(uint64(chunk), chunk)...),
		chunkOf(uint64(chunk*2), chunk)...)
	got := readPublished(t, f, "named.bin", len(want))
	if !bytes.Equal(got, want) {
		t.Fatal("the assembled file is not the chunks in name order")
	}
}

// A gap at assembly is a refusal naming what is missing, because there is
// nothing left to wait for.
func TestAssemblyRefusesAGapAndNamesIt(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	const chunk = limits.UploadChunkFloor
	s := f.create(t, "gapped.bin", uint64(chunk*3), SessionSpec{Mode: SpoolNameOrdered})

	for _, name := range []uint32{1, 3} {
		off := uint64(name-1) * chunk
		if err := f.engine.PutNamed(ctx, f.root(t), s.ID, testUser, name,
			bytes.NewReader(chunkOf(off, chunk)), nil); err != nil {
			t.Fatalf("PutNamed(%d): %v", name, err)
		}
	}
	_, err := f.engine.Assemble(ctx, f.resolve(t, "gapped.bin"), s.ID, uint64(chunk*3), nil)
	if !errors.Is(err, ErrIncomplete) {
		t.Fatalf("an assembly over a gap returned %v", err)
	}
	if !bytes.Contains([]byte(err.Error()), []byte("2")) {
		t.Fatalf("the refusal does not name the missing chunk: %v", err)
	}
}

// A repeated name is a client retry after a lost response, carrying the same
// bytes, so it overwrites rather than being refused.
func TestARepeatedChunkNameIsARetry(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	const chunk = limits.UploadChunkFloor
	s := f.create(t, "retried.bin", uint64(chunk*2), SessionSpec{Mode: SpoolNameOrdered})

	// Chunk two arrives twice while chunk one is still missing.
	for i := 0; i < 2; i++ {
		if err := f.engine.PutNamed(ctx, f.root(t), s.ID, testUser, 2,
			bytes.NewReader(chunkOf(uint64(chunk), chunk)), nil); err != nil {
			t.Fatalf("PutNamed attempt %d: %v", i, err)
		}
	}
	names, err := f.engine.ListChunks(ctx, s.ID, testUser)
	if err != nil {
		t.Fatalf("ListChunks: %v", err)
	}
	if len(names) != 1 || names[0] != 2 {
		t.Fatalf("the repeated name is held %v", names)
	}
}

func TestTheTwoModesRefuseEachOthersWrites(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	offsets := f.create(t, "offsets.bin", 10, SessionSpec{})
	names := f.create(t, "names.bin", 10, SessionSpec{Mode: SpoolNameOrdered})

	if err := f.engine.PutNamed(ctx, f.root(t), offsets.ID, testUser, 1,
		bytes.NewReader([]byte("x")), nil); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("a named chunk against an offset-addressed session returned %v", err)
	}
	if _, err := f.engine.PatchAt(ctx, f.root(t), names.ID, testUser, 0,
		bytes.NewReader([]byte("x")), nil); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("an offset chunk against a name-ordered session returned %v", err)
	}
	// A chunk name starts at one: zero names nothing.
	if err := f.engine.PutNamed(ctx, f.root(t), names.ID, testUser, 0,
		bytes.NewReader([]byte("x")), nil); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("chunk name zero returned %v", err)
	}
}

// readPublished reads a published file back through the share root.
func readPublished(t *testing.T, f *fixture, name string, n int) []byte {
	t.Helper()
	p, err := vfs.ParseSafePath(name)
	if err != nil {
		t.Fatalf("parsing %q: %v", name, err)
	}
	file, err := f.root(t).OpenRead(p, vfs.IntentRead)
	if err != nil {
		t.Fatalf("opening %q: %v", name, err)
	}
	t.Cleanup(func() {
		if cerr := file.Close(); cerr != nil {
			t.Errorf("closing %q: %v", name, cerr)
		}
	})
	buf := make([]byte, n)
	if _, rerr := file.ReadAt(buf, 0); rerr != nil && !errors.Is(rerr, io.EOF) {
		t.Fatalf("reading %q: %v", name, rerr)
	}
	return buf
}
