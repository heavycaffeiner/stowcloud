package vault

import (
	"crypto/aes"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/crypto/xts"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/secret"
)

// dataUnitBytes is the fixed 512-byte unit VeraCrypt's data area XTS tweak
// counts in, independent of the header's own sector size field: the tweak
// for the byte at data-area offset n is always n/512, which is what makes
// this driver's data area interoperate with any real VeraCrypt build
// regardless of what sector size the volume declares.
const dataUnitBytes = 512

// volumeDevice is the decrypted, random-access byte space of one VeraCrypt
// container's data area. It implements Device, so the FAT driver in fat.go
// never has to know a container file exists underneath it.
type volumeDevice struct {
	f          *os.File
	dataOffset int64
	dataSize   int64
	cipher     *xts.Cipher
}

func openVolumeDevice(f *os.File, fields headerFields, keys [xtsKeySize]byte) (*volumeDevice, error) {
	cipher, err := xts.NewCipher(aes.NewCipher, keys[:])
	if err != nil {
		return nil, fmt.Errorf("vault: build data area cipher: %w", err)
	}
	return &volumeDevice{
		f:          f,
		dataOffset: int64(fields.dataAreaOffset),
		dataSize:   int64(fields.dataAreaSize),
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
	firstUnit := uint64(alignedOff / dataUnitBytes)
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
	firstUnit := uint64(alignedOff / dataUnitBytes)

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
// iterations, both CRC-32s valid, a backup header at the end, and the data
// area filled with random bytes so no boundary in the file distinguishes
// written content from empty space.
//
// It does not format the data area: the caller runs Format over the
// resulting container once it is open, so the two concerns (a valid
// encrypted container, and a valid filesystem inside it) are each testable
// on their own.
func createContainer(path string, sizeMiB uint64, password secret.Secret) error {
	if sizeMiB < minContainerDataMiB || sizeMiB > maxContainerDataMiB {
		return ErrContainerSize
	}
	dataSize := sizeMiB << 20
	fileSize := headerRegionSize + dataSize + headerRegionSize

	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("vault: create container file: %w", err)
	}
	defer func() { _ = f.Close() }()

	success := false
	defer func() {
		if !success {
			_ = os.Remove(path)
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
	var keys [xtsKeySize]byte
	if _, err := rand.Read(keys[:]); err != nil {
		return fmt.Errorf("vault: generate master keys: %w", err)
	}
	var padding [headerKeyAreaSize - xtsKeySize]byte
	if _, err := rand.Read(padding[:]); err != nil {
		return fmt.Errorf("vault: generate key area padding: %w", err)
	}

	fields := headerFields{
		version:           5,
		minProgramVersion: 0x0108,
		volumeSize:        dataSize,
		dataAreaOffset:    headerRegionSize,
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
	if err := fillRandom(f, headerRecordSize, headerRegionSize-headerRecordSize); err != nil {
		return fmt.Errorf("vault: fill header region padding: %w", err)
	}
	if err := fillRandom(f, headerRegionSize, dataSize); err != nil {
		return fmt.Errorf("vault: fill data area: %w", err)
	}
	if _, err := f.WriteAt(record2, int64(fileSize-headerRegionSize)); err != nil {
		return fmt.Errorf("vault: write backup header: %w", err)
	}
	if err := fillRandom(f, fileSize-headerRegionSize+headerRecordSize, headerRegionSize-headerRecordSize); err != nil {
		return fmt.Errorf("vault: fill backup header region padding: %w", err)
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
		if _, err := f.WriteAt(buf[:size], int64(off)); err != nil {
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
func openContainer(path string, password secret.Secret) (*volumeDevice, uint64, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return nil, 0, fmt.Errorf("vault: open container file: %w", err)
	}
	success := false
	defer func() {
		if !success {
			_ = f.Close()
		}
	}()

	stat, err := f.Stat()
	if err != nil {
		return nil, 0, fmt.Errorf("vault: stat container file: %w", err)
	}
	size := uint64(stat.Size())
	if size < headerRegionSize+minContainerDataMiB<<20+headerRegionSize {
		return nil, 0, fmt.Errorf("%w: container file too small to hold a header", ErrHeaderFieldsInvalid)
	}

	record := make([]byte, headerRecordSize)
	if _, err := f.ReadAt(record, 0); err != nil {
		return nil, 0, fmt.Errorf("vault: read primary header: %w", err)
	}
	fields, keys, err := decryptHeaderRecord(record, password)
	if err != nil {
		return nil, 0, err
	}
	if err := validateHeaderFields(fields, size); err != nil {
		return nil, 0, err
	}
	dev, err := openVolumeDevice(f, fields, keys)
	if err != nil {
		return nil, 0, err
	}
	success = true
	return dev, fields.dataAreaSize, nil
}
