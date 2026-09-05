package vault

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/secret"
)

// dataUnitBytes is the fixed 512-byte unit VeraCrypt's XTS tweak counts in,
// independent of the header's own sector size field.
//
// The tweak for a byte is its absolute position in the container file
// divided by 512, not its position within the data area: the first data
// sector of an ordinary container is unit 256, since the data area starts
// 128 KiB in. Getting this wrong reads every real container as garbage
// while a container this driver wrote itself round-trips perfectly, which
// is why it took a fixture made by VeraCrypt to catch.
const dataUnitBytes = 512

// volumeDevice is the decrypted, random-access byte space of one VeraCrypt
// container's data area. It implements Device, so the FAT driver in fat.go
// never has to know a container file exists underneath it.
type volumeDevice struct {
	f          *os.File
	dataOffset int64
	dataSize   int64
	cipher     *cascade
}

func openVolumeDevice(f *os.File, fields headerFields, alg encryptionAlgorithm, keys []byte) (*volumeDevice, error) {
	cipher, err := newCascade(alg, keys)
	if err != nil {
		return nil, fmt.Errorf("vault: build data area cipher: %w", err)
	}
	dataOffset, err := num.Narrow[int64](fields.dataAreaOffset)
	if err != nil {
		return nil, fmt.Errorf("%w: data area offset out of range: %v", ErrHeaderFieldsInvalid, err)
	}
	dataSize, err := num.Narrow[int64](fields.dataAreaSize)
	if err != nil {
		return nil, fmt.Errorf("%w: data area size out of range: %v", ErrHeaderFieldsInvalid, err)
	}
	return &volumeDevice{
		f:          f,
		dataOffset: dataOffset,
		dataSize:   dataSize,
		cipher:     cipher,
	}, nil
}

// unitRange returns the aligned byte range, in data-unit multiples, that
// covers [off, off+n).
func unitRange(off, n int64) (alignedOff, alignedLen int64) {
	firstUnit := off / dataUnitBytes
	lastUnit := (off + n - 1) / dataUnitBytes
	return firstUnit * dataUnitBytes, (lastUnit - firstUnit + 1) * dataUnitBytes
}

// tweakUnit is the XTS data unit number for a data-area offset, which is
// its absolute position in the container file over 512. dataOffset is a
// multiple of the unit size on every container VeraCrypt writes, so this
// stays exact.
func (v *volumeDevice) tweakUnit(alignedOff int64) uint64 {
	return mustNarrow[uint64]((v.dataOffset+alignedOff)/dataUnitBytes, "XTS tweak unit")
}

// ReadAt decrypts [off, off+len(p)) of the data area. A partial unit at
// either end is decrypted whole and trimmed, since XTS only decrypts a
// complete data unit.
func (v *volumeDevice) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, fmt.Errorf("vault: negative offset")
	}
	if off >= v.dataSize {
		return 0, io.EOF
	}
	n := int64(len(p))
	if off+n > v.dataSize {
		n = v.dataSize - off
	}
	if n == 0 {
		return 0, nil
	}
	alignedOff, alignedLen := unitRange(off, n)
	cipherBuf := make([]byte, alignedLen)
	if _, err := v.f.ReadAt(cipherBuf, v.dataOffset+alignedOff); err != nil {
		return 0, fmt.Errorf("vault: read container data area: %w", err)
	}
	plainBuf := make([]byte, alignedLen)
	units := alignedLen / dataUnitBytes
	firstUnit := v.tweakUnit(alignedOff)
	for u := int64(0); u < units; u++ {
		v.cipher.Decrypt(plainBuf[u*dataUnitBytes:(u+1)*dataUnitBytes], cipherBuf[u*dataUnitBytes:(u+1)*dataUnitBytes], firstUnit+uint64(u))
	}
	copy(p[:n], plainBuf[off-alignedOff:off-alignedOff+n])
	if n < int64(len(p)) {
		return int(n), io.EOF
	}
	return int(n), nil
}

// WriteAt encrypts data into [off, off+len(p)) of the data area. A unit only
// partially covered by p is read back and decrypted first, so the bytes p
// does not touch survive the read-modify-write.
func (v *volumeDevice) WriteAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, fmt.Errorf("vault: negative offset")
	}
	n := int64(len(p))
	if off+n > v.dataSize {
		return 0, ErrNoSpaceOnVolume
	}
	if n == 0 {
		return 0, nil
	}
	alignedOff, alignedLen := unitRange(off, n)
	units := alignedLen / dataUnitBytes
	firstUnit := v.tweakUnit(alignedOff)

	plainBuf := make([]byte, alignedLen)
	partial := off != alignedOff || off+n != alignedOff+alignedLen
	if partial {
		cipherBuf := make([]byte, alignedLen)
		if _, err := v.f.ReadAt(cipherBuf, v.dataOffset+alignedOff); err != nil {
			return 0, fmt.Errorf("vault: read container data area for read-modify-write: %w", err)
		}
		for u := int64(0); u < units; u++ {
			v.cipher.Decrypt(plainBuf[u*dataUnitBytes:(u+1)*dataUnitBytes], cipherBuf[u*dataUnitBytes:(u+1)*dataUnitBytes], firstUnit+uint64(u))
		}
	}
	copy(plainBuf[off-alignedOff:off-alignedOff+n], p)
	cipherOut := make([]byte, alignedLen)
	for u := int64(0); u < units; u++ {
		v.cipher.Encrypt(cipherOut[u*dataUnitBytes:(u+1)*dataUnitBytes], plainBuf[u*dataUnitBytes:(u+1)*dataUnitBytes], firstUnit+uint64(u))
	}
	if _, err := v.f.WriteAt(cipherOut, v.dataOffset+alignedOff); err != nil {
		return 0, fmt.Errorf("vault: write container data area: %w", err)
	}
	return int(n), nil
}

func (v *volumeDevice) Sync() error {
	return v.f.Sync()
}

// ErrContainerSize is the size refusal Create raises before writing
// anything, so a request outside the range this driver supports never
// touches disk.
var ErrContainerSize = errors.New("vault: container size must be between 16 MiB and 1 TiB")

// createContainer writes a genuine VeraCrypt container: a random salt and
// random master keys, a header encrypted with PBKDF2-HMAC-SHA-512 at 500000
// iterations, both CRC-32s valid, backup headers at the end, and the data
// area filled with random bytes so no boundary in the file distinguishes
// written content from empty space.
//
// The layout is VeraCrypt's own: a 128 KiB header group at each end, being
// a 64 KiB header region followed by a 64 KiB slot a hidden volume's header
// would occupy, and the data area between them. The hidden slot is random
// here and stays random, which is what makes a container holding no hidden
// volume indistinguishable from one that does.
//
// It does not format the data area: the caller runs the filesystem writer
// over the resulting container once it is open, so the two concerns (a
// valid encrypted container, and a valid filesystem inside it) are each
// testable on their own.
func createContainer(path string, sizeMiB uint64, password secret.Secret) error {
	if sizeMiB < minContainerDataMiB || sizeMiB > maxContainerDataMiB {
		return ErrContainerSize
	}
	dataSize := sizeMiB << 20
	fileSize := headerGroupSize + dataSize + headerGroupSize

	path = filepath.Clean(path)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("vault: create container file: %w", err)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil {
			slog.Default().Warn("vault: closing a container file after create", "path", path, "error", cerr)
		}
	}()

	success := false
	defer func() {
		if !success {
			if rerr := os.Remove(path); rerr != nil {
				slog.Default().Warn("vault: removing a partially created container after a failed create", "path", path, "error", rerr)
			}
		}
	}()

	salt1, err := randomBytes(headerSaltSize)
	if err != nil {
		return err
	}
	salt2, err := randomBytes(headerSaltSize)
	if err != nil {
		return err
	}
	var keys [layerKeyBytes]byte
	if _, rerr := rand.Read(keys[:]); rerr != nil {
		return fmt.Errorf("vault: generate master keys: %w", rerr)
	}
	var padding [headerKeyAreaSize - layerKeyBytes]byte
	if _, rerr := rand.Read(padding[:]); rerr != nil {
		return fmt.Errorf("vault: generate key area padding: %w", rerr)
	}

	fields := headerFields{
		version:           5,
		minProgramVersion: 0x0108,
		volumeSize:        dataSize,
		dataAreaOffset:    headerGroupSize,
		dataAreaSize:      dataSize,
		flags:             0,
		sectorSize:        512,
	}
	plain := buildHeaderPlaintext(fields, keys, padding)

	record1, err := encryptHeaderRecord(plain, password, salt1)
	if err != nil {
		return err
	}
	record2, err := encryptHeaderRecord(plain, password, salt2)
	if err != nil {
		return err
	}

	if err := f.Truncate(int64(fileSize)); err != nil {
		return fmt.Errorf("vault: size container file: %w", err)
	}
	if _, err := f.WriteAt(record1, 0); err != nil {
		return fmt.Errorf("vault: write primary header: %w", err)
	}
	// Everything outside the two 512-byte header records is random, the
	// hidden header slots included: a reader without the passphrase cannot
	// tell a slot holding a hidden volume from one that never did.
	if err := fillRandom(f, headerRecordSize, headerGroupSize-headerRecordSize); err != nil {
		return fmt.Errorf("vault: fill header group padding: %w", err)
	}
	if err := fillRandom(f, headerGroupSize, dataSize); err != nil {
		return fmt.Errorf("vault: fill data area: %w", err)
	}
	backupOff := headerGroupSize + dataSize
	if _, err := f.WriteAt(record2, mustNarrow[int64](backupOff, "backup header offset")); err != nil {
		return fmt.Errorf("vault: write backup header: %w", err)
	}
	if err := fillRandom(f, backupOff+headerRecordSize, headerGroupSize-headerRecordSize); err != nil {
		return fmt.Errorf("vault: fill backup header group padding: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("vault: sync container file: %w", err)
	}
	success = true
	return nil
}

func randomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return nil, fmt.Errorf("vault: read system randomness: %w", err)
	}
	return b, nil
}

// fillRandom writes n bytes of random data at off, in bounded chunks so a
// large container's creation never allocates the whole region in memory at
// once.
func fillRandom(f *os.File, off, n uint64) error {
	const chunk = 4 << 20
	buf := make([]byte, min(chunk, n))
	for n > 0 {
		size := uint64(len(buf))
		if n < size {
			size = n
		}
		if _, err := rand.Read(buf[:size]); err != nil {
			return fmt.Errorf("vault: read system randomness: %w", err)
		}
		if _, err := f.WriteAt(buf[:size], mustNarrow[int64](off, "random-fill write offset")); err != nil {
			return err
		}
		off += size
		n -= size
	}
	return nil
}

// openContainer opens path, decrypts its header with password, and returns
// the decrypted data area device plus the data area's own size, which is
// the coordinate space fat.Mount bounds every header field against: the
// Device this returns addresses only the data area, not the whole
// container file.
func openContainer(path string, password secret.Secret, pim uint32, hashToken string) (*volumeDevice, uint64, error) {
	path = filepath.Clean(path)
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return nil, 0, fmt.Errorf("vault: open container file: %w", err)
	}
	success := false
	defer func() {
		if !success {
			if cerr := f.Close(); cerr != nil {
				slog.Default().Warn("vault: closing a container file after a failed open", "path", path, "error", cerr)
			}
		}
	}()

	stat, err := f.Stat()
	if err != nil {
		return nil, 0, fmt.Errorf("vault: stat container file: %w", err)
	}
	// A dynamic container is a sparse file whose reported length is its
	// declared capacity, so this is the same number for both kinds and the
	// holes read back as zeroes the header bounds are checked against.
	size := mustNarrow[uint64](stat.Size(), "container file size")
	if size < headerGroupSize+minContainerDataMiB<<20+headerGroupSize {
		return nil, 0, fmt.Errorf("%w: container file too small to hold a header", ErrHeaderFieldsInvalid)
	}

	opened, err := openEitherHeader(f, password, pim, hashToken)
	if err != nil {
		return nil, 0, err
	}
	if verr := validateHeaderFields(opened.fields, size); verr != nil {
		return nil, 0, verr
	}
	dev, err := openVolumeDevice(f, opened.fields, opened.alg, opened.keys[:])
	if err != nil {
		return nil, 0, err
	}
	success = true
	return dev, opened.fields.dataAreaSize, nil
}

// hiddenHeaderOffset is where a hidden volume's header sits. When there is
// no hidden volume the same bytes are random, and are indistinguishable
// from one without the passphrase.
const hiddenHeaderOffset = headerRegionSize

// openEitherHeader reads the outer header and then the hidden one, and
// returns whichever the passphrase opens.
//
// Which volume a passphrase mounts is the passphrase's own answer, not a
// choice the caller makes: that is the whole point of a hidden volume, so
// both are tried and the operator gets the one their passphrase belongs
// to. The outer goes first because a container with no hidden volume is
// the common case and its 64 KiB of random bytes would otherwise cost a
// full set of derivations before the real answer.
//
// A hidden volume needs nothing computed: its own header carries the
// absolute offset of its data area, the same field the outer header uses,
// and validateHeaderFields bounds it against the file either way.
func openEitherHeader(f *os.File, password secret.Secret, pim uint32, hashToken string) (openedHeader, error) {
	record := make([]byte, headerRecordSize)
	if _, err := f.ReadAt(record, 0); err != nil {
		return openedHeader{}, fmt.Errorf("vault: read primary header: %w", err)
	}
	outer, outerErr := decryptHeaderRecord(record, password, pim, hashToken)
	if outerErr == nil {
		return outer, nil
	}
	if !errors.Is(outerErr, ErrWrongPassword) {
		return openedHeader{}, outerErr
	}
	if _, err := f.ReadAt(record, hiddenHeaderOffset); err != nil {
		return openedHeader{}, fmt.Errorf("vault: read hidden volume header: %w", err)
	}
	hidden, hiddenErr := decryptHeaderRecord(record, password, pim, hashToken)
	if hiddenErr != nil {
		// The outer refusal is the one to report: a container with no
		// hidden volume holds random bytes here, so this refusal says
		// nothing an operator can act on.
		return openedHeader{}, outerErr
	}
	return hidden, nil
}
