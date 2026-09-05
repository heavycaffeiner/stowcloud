package vault

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/secret"
)

func TestCreateContainerRejectsBadSize(t *testing.T) {
	dir := t.TempDir()
	pw := secret.New([]byte("whatever"))
	if err := createContainer(filepath.Join(dir, "too-small.hc"), 8, pw); !errors.Is(err, ErrContainerSize) {
		t.Fatalf("size 8 MiB: got %v, want ErrContainerSize", err)
	}
	if err := createContainer(filepath.Join(dir, "too-big.hc"), (1<<20)+1, pw); !errors.Is(err, ErrContainerSize) {
		t.Fatalf("size 1 TiB+1: got %v, want ErrContainerSize", err)
	}
}

func TestHeaderRoundTripAndWrongPasswordVsCorruption(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vault.hc")
	correct := secret.New([]byte("correct horse battery staple"))
	wrong := secret.New([]byte("a different password entirely"))

	if err := createContainer(path, minContainerDataMiB, correct); err != nil {
		t.Fatalf("createContainer: %v", err)
	}

	dev, dataSize, err := openContainer(path, correct, 0, "")
	if err != nil {
		t.Fatalf("open with correct password: %v", err)
	}
	if dataSize != minContainerDataMiB<<20 {
		t.Fatalf("data size = %d, want %d", dataSize, minContainerDataMiB<<20)
	}
	if cerr := dev.f.Close(); cerr != nil {
		t.Fatalf("close: %v", cerr)
	}

	// Hinted at the KDF this container was written with. Unhinted, a wrong
	// passphrase exhausts every derivation, including two software hashes
	// at 500000 iterations and Argon2id at 416 MiB, which is the correct
	// behaviour and far too slow for a suite that also runs under the race
	// detector. What is under test here is the refusal, not the search.
	if _, _, werr := openContainer(path, wrong, 0, "sha512"); !errors.Is(werr, ErrWrongPassword) {
		t.Fatalf("open with wrong password: got %v, want ErrWrongPassword", werr)
	}

	// Flip a byte inside the reserved region (plaintext offset 96..111,
	// header-record offset 164), which corrupts exactly one 16-byte XTS
	// block. That block sits well after the block holding the "VERA" magic
	// (header-record offset 64..79), so a correct password still decrypts a
	// valid magic and only the header CRC-32 fails.
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open container: %v", err)
	}
	var flip [1]byte
	if _, rerr := f.ReadAt(flip[:], 164); rerr != nil {
		t.Fatalf("read byte 164: %v", rerr)
	}
	flip[0] ^= 0xFF
	if _, werr := f.WriteAt(flip[:], 164); werr != nil {
		t.Fatalf("corrupt byte 164: %v", werr)
	}
	if cerr := f.Close(); cerr != nil {
		t.Fatalf("close corrupted container: %v", cerr)
	}

	_, _, err = openContainer(path, correct, 0, "")
	if !errors.Is(err, ErrHeaderCorrupt) {
		t.Fatalf("open corrupted header with correct password: got %v, want ErrHeaderCorrupt", err)
	}
	if errors.Is(err, ErrWrongPassword) {
		t.Fatalf("ErrHeaderCorrupt must not also satisfy ErrWrongPassword")
	}
}

func TestXTSDataUnitNumbering(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vault.hc")
	pw := secret.New([]byte("xts tweak convention"))
	if err := createContainer(path, minContainerDataMiB, pw); err != nil {
		t.Fatalf("createContainer: %v", err)
	}

	dev, _, err := openContainer(path, pw, 0, "")
	if err != nil {
		t.Fatalf("openContainer: %v", err)
	}

	boundaryOff := int64(0)
	wantBoundary := []byte("boundary sector payload, byte 0")
	interiorOff := int64(1536 + 37) // sector 3 of the data area, 37 bytes in
	wantInterior := []byte("interior payload")

	if _, werr := dev.WriteAt(wantBoundary, boundaryOff); werr != nil {
		t.Fatalf("WriteAt boundary: %v", werr)
	}
	if _, werr := dev.WriteAt(wantInterior, interiorOff); werr != nil {
		t.Fatalf("WriteAt interior: %v", werr)
	}
	if serr := dev.Sync(); serr != nil {
		t.Fatalf("Sync: %v", serr)
	}
	if cerr := dev.f.Close(); cerr != nil {
		t.Fatalf("close: %v", cerr)
	}

	dev2, _, err := openContainer(path, pw, 0, "")
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() {
		if cerr := dev2.f.Close(); cerr != nil {
			t.Errorf("closing dev2: %v", cerr)
		}
	}()

	gotBoundary := make([]byte, len(wantBoundary))
	if _, berr := dev2.ReadAt(gotBoundary, boundaryOff); berr != nil {
		t.Fatalf("ReadAt boundary: %v", berr)
	}
	if !bytes.Equal(gotBoundary, wantBoundary) {
		t.Fatalf("boundary sector: got %q, want %q", gotBoundary, wantBoundary)
	}
	gotInterior := make([]byte, len(wantInterior))
	if _, ierr := dev2.ReadAt(gotInterior, interiorOff); ierr != nil {
		t.Fatalf("ReadAt interior: %v", ierr)
	}
	if !bytes.Equal(gotInterior, wantInterior) {
		t.Fatalf("interior bytes: got %q, want %q", gotInterior, wantInterior)
	}

	// Prove the tweak is the sector's absolute index in the container file,
	// not its index within the data area, by decrypting the raw container
	// bytes by hand with exactly that convention and checking it agrees
	// independently of the driver under test. Both conventions agree on a
	// container whose data area starts at offset zero, which no real
	// container does, so this assertion is what keeps the two apart.
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open raw file: %v", err)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil {
			t.Errorf("closing raw file: %v", cerr)
		}
	}()
	record := make([]byte, headerRecordSize)
	if _, rerr := f.ReadAt(record, 0); rerr != nil {
		t.Fatalf("read header record: %v", rerr)
	}
	opened, err := decryptHeaderRecord(record, pw, 0, "")
	if err != nil {
		t.Fatalf("decryptHeaderRecord: %v", err)
	}
	dataOffset := mustNarrow[int64](opened.fields.dataAreaOffset, "data area offset")
	if dataOffset == 0 {
		t.Fatal("a container whose data area starts at offset zero cannot tell the two tweak conventions apart")
	}
	cipher, err := newCascade(opened.alg, opened.keys[:])
	if err != nil {
		t.Fatalf("newCascade: %v", err)
	}
	fileOff := dataOffset + interiorOff
	sector := fileOff / dataUnitBytes
	within := fileOff % dataUnitBytes
	rawSector := make([]byte, dataUnitBytes)
	if _, err := f.ReadAt(rawSector, sector*dataUnitBytes); err != nil {
		t.Fatalf("read raw data sector: %v", err)
	}
	plainSector := make([]byte, dataUnitBytes)
	cipher.Decrypt(plainSector, rawSector, mustNarrow[uint64](sector, "XTS tweak unit"))
	got := plainSector[within : within+int64(len(wantInterior))]
	if !bytes.Equal(got, wantInterior) {
		t.Fatalf("manual file-absolute decrypt = %q, want %q (tweak convention mismatch)", got, wantInterior)
	}
}
