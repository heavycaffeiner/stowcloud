package vault

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/secret"
)

// Sizes and offsets from the VeraCrypt volume format. A container is a
// 128 KiB header group, the data area, then a second header group holding
// the backups. Each group is a 64 KiB header region followed by a 64 KiB
// region a hidden volume's header occupies, or that holds random bytes when
// there is no hidden volume.
//
// The first 512 bytes of a region are the only part this driver reads or
// writes; the rest, like the whole data area at creation time, is random so
// no boundary in the file leaks where real content starts.
const (
	headerRegionSize  = 64 << 10
	headerGroupSize   = 2 * headerRegionSize
	headerRecordSize  = 512
	headerSaltSize    = 64
	headerCipherSize  = 448 // the encrypted part of headerRecordSize
	headerKeyAreaSize = 256 // master key material: three cascade layers at most
	veraMagic         = "VERA"

	minContainerDataMiB = 16
	maxContainerDataMiB = 1 << 20 // 1 TiB
)

// ErrWrongPassword means no supported combination of key derivation and
// cipher produced the "VERA" magic: either the passphrase is wrong, or the
// operator named a PIM the container was not created with. The two are
// cryptographically indistinguishable without the passphrase.
var ErrWrongPassword = errors.New("vault: wrong passphrase, or the wrong PIM for this container")

// ErrHeaderCorrupt means a header decrypted (its magic matched, so the
// password and key derivation were both right) but one of its two CRC-32
// checks failed. This is deliberately a different error from
// ErrWrongPassword: a wrong password almost never produces a valid magic by
// chance, so reaching a CRC mismatch after a magic match means the header
// itself was damaged, not that the password was mistyped.
var ErrHeaderCorrupt = errors.New("vault: header decrypted but failed its integrity check, the container header is corrupt")

// openedHeader is one header that decrypted: its fields, the master key
// material for the data area, and which algorithm read it.
type openedHeader struct {
	fields headerFields
	keys   [headerKeyAreaSize]byte
	alg    encryptionAlgorithm
}

// decryptHeaderRecord finds the one combination of key derivation and
// cipher that opens record, the 512-byte salt-plus-encrypted-fields block.
//
// The cost is one derivation per KDF, not per combination: PBKDF2's output
// blocks are independent, so the full-width derivation a cascade needs has
// every shorter cipher's key material as its prefix, and Argon2id is asked
// for that same full width for the same reason. The cipher loop that
// follows each derivation is a 448-byte decrypt, which is free by
// comparison.
func decryptHeaderRecord(record []byte, password secret.Secret, pim uint32, hashToken string) (openedHeader, error) {
	if len(record) != headerRecordSize {
		return openedHeader{}, fmt.Errorf("vault: header record must be %d bytes, got %d", headerRecordSize, len(record))
	}
	kdfs, err := headerKDFsFor(hashToken)
	if err != nil {
		return openedHeader{}, err
	}
	salt := record[:headerSaltSize]
	ciphertext := record[headerSaltSize:]
	plain := make([]byte, headerCipherSize)

	for _, kdf := range kdfs {
		key, err := deriveHeaderKey(password, salt, kdf, pim)
		if err != nil {
			return openedHeader{}, fmt.Errorf("vault: %s: %w", kdf.name, err)
		}
		for _, alg := range supportedAlgorithms() {
			c, err := newCascade(alg, key)
			if err != nil {
				return openedHeader{}, err
			}
			c.Decrypt(plain, ciphertext, 0)
			if !bytes.Equal(plain[0:4], []byte(veraMagic)) {
				continue
			}
			return parseHeaderPlaintext(plain, alg)
		}
	}
	return openedHeader{}, ErrWrongPassword
}

// parseHeaderPlaintext decodes the 448-byte decrypted header body, whose
// magic the caller has already matched, validating both CRC-32s before
// trusting any other field: a header that decrypted under the right key but
// was damaged afterward must be refused, not read as if it were intact.
func parseHeaderPlaintext(plain []byte, alg encryptionAlgorithm) (openedHeader, error) {
	keysCRC := binary.BigEndian.Uint32(plain[8:12])
	if crc32.ChecksumIEEE(plain[192:448]) != keysCRC {
		return openedHeader{}, ErrHeaderCorrupt
	}
	headerCRC := binary.BigEndian.Uint32(plain[188:192])
	if crc32.ChecksumIEEE(plain[0:188]) != headerCRC {
		return openedHeader{}, ErrHeaderCorrupt
	}
	fields := headerFields{
		version:           binary.BigEndian.Uint16(plain[4:6]),
		minProgramVersion: binary.BigEndian.Uint16(plain[6:8]),
		hiddenVolumeSize:  binary.BigEndian.Uint64(plain[28:36]),
		volumeSize:        binary.BigEndian.Uint64(plain[36:44]),
		dataAreaOffset:    binary.BigEndian.Uint64(plain[44:52]),
		dataAreaSize:      binary.BigEndian.Uint64(plain[52:60]),
		flags:             binary.BigEndian.Uint32(plain[60:64]),
		sectorSize:        binary.BigEndian.Uint32(plain[64:68]),
	}
	if fields.dataAreaOffset == 0 {
		fields.dataAreaOffset = headerRecordSize
	}
	if fields.sectorSize == 0 {
		fields.sectorSize = headerRecordSize
	}
	out := openedHeader{fields: fields, alg: alg}
	copy(out.keys[:], plain[192:192+headerKeyAreaSize])
	return out, nil
}

// headerFields is a VeraCrypt volume header's decoded, non-secret content.
type headerFields struct {
	version           uint16
	minProgramVersion uint16
	hiddenVolumeSize  uint64
	volumeSize        uint64
	dataAreaOffset    uint64
	dataAreaSize      uint64
	flags             uint32
	sectorSize        uint32
}

// Flag bits a header may carry. Both describe a volume this driver has no
// business serving as a share: one belongs to a boot disk, the other to a
// conversion that is only partly done, and in each case the data area is
// not the plain encrypted filesystem the rest of this package assumes.
const (
	headerFlagSystemEncryption = 1 << 0
	headerFlagInPlace          = 1 << 1
)

// ErrUnsupportedVolume names a container this driver refuses on purpose
// rather than for want of an algorithm.
var ErrUnsupportedVolume = errors.New("vault: this volume is not a plain file container")

// ErrHeaderFieldsInvalid names a header that decrypted and passed both
// CRC-32 checks, but whose fields describe something this driver cannot
// safely open: every one of these values came from the container file, so
// none of them is trusted before this check runs.
var ErrHeaderFieldsInvalid = errors.New("vault: header fields describe an invalid volume")

// validateHeaderFields bounds every header field against the actual
// container file size before anything downstream computes an offset or an
// allocation from it.
func validateHeaderFields(f headerFields, containerSize uint64) error {
	if f.flags&headerFlagSystemEncryption != 0 {
		return fmt.Errorf("%w: this is a system-encrypted volume", ErrUnsupportedVolume)
	}
	if f.flags&headerFlagInPlace != 0 {
		return fmt.Errorf("%w: this volume is mid-conversion by in-place encryption", ErrUnsupportedVolume)
	}
	if f.sectorSize < 512 || f.sectorSize > 4096 || f.sectorSize%16 != 0 {
		return fmt.Errorf("%w: sector size %d", ErrHeaderFieldsInvalid, f.sectorSize)
	}
	if f.dataAreaOffset < headerRegionSize {
		return fmt.Errorf("%w: data area offset %d overlaps the header", ErrHeaderFieldsInvalid, f.dataAreaOffset)
	}
	if f.dataAreaSize == 0 {
		return fmt.Errorf("%w: zero-length data area", ErrHeaderFieldsInvalid)
	}
	end, overflow := addUint64(f.dataAreaOffset, f.dataAreaSize)
	if overflow || end > containerSize {
		return fmt.Errorf("%w: data area runs past the end of the container file", ErrHeaderFieldsInvalid)
	}
	return nil
}

func addUint64(a, b uint64) (sum uint64, overflow bool) {
	sum = a + b
	return sum, sum < a
}

// buildHeaderPlaintext lays out the 448-byte decrypted header body for a
// freshly created container, computing both CRC-32s over the content it just
// wrote.
func buildHeaderPlaintext(f headerFields, keys [layerKeyBytes]byte, keyAreaPadding [headerKeyAreaSize - layerKeyBytes]byte) []byte {
	plain := make([]byte, headerCipherSize)
	copy(plain[0:4], veraMagic)
	binary.BigEndian.PutUint16(plain[4:6], f.version)
	binary.BigEndian.PutUint16(plain[6:8], f.minProgramVersion)
	// plain[8:12] (keys CRC-32) and plain[12:28] (reserved) filled below or left zero.
	// plain[28:36] hidden volume size stays zero: this driver never creates one.
	binary.BigEndian.PutUint64(plain[36:44], f.volumeSize)
	binary.BigEndian.PutUint64(plain[44:52], f.dataAreaOffset)
	binary.BigEndian.PutUint64(plain[52:60], f.dataAreaSize)
	binary.BigEndian.PutUint32(plain[60:64], f.flags)
	binary.BigEndian.PutUint32(plain[64:68], f.sectorSize)
	// plain[68:188] reserved, stays zero.
	copy(plain[192:192+layerKeyBytes], keys[:])
	copy(plain[192+layerKeyBytes:448], keyAreaPadding[:])
	binary.BigEndian.PutUint32(plain[8:12], crc32.ChecksumIEEE(plain[192:448]))
	binary.BigEndian.PutUint32(plain[188:192], crc32.ChecksumIEEE(plain[0:188]))
	return plain
}

// encryptHeaderRecord derives a header key from password and salt using the
// creation KDF and encrypts plain under it, returning the full 512-byte
// on-disk record.
//
// Creation deliberately uses one combination rather than offering the
// choice: PBKDF2-HMAC-SHA-512 at the default iteration count with AES-XTS,
// which is the first entry in each table and what every VeraCrypt build
// can open.
func encryptHeaderRecord(plain []byte, password secret.Secret, salt []byte) ([]byte, error) {
	key, err := deriveHeaderKey(password, salt, supportedHeaderKDFs()[0], 0)
	if err != nil {
		return nil, fmt.Errorf("vault: derive header key: %w", err)
	}
	c, err := newCascade(supportedAlgorithms()[0], key)
	if err != nil {
		return nil, fmt.Errorf("vault: build header cipher: %w", err)
	}
	record := make([]byte, headerRecordSize)
	copy(record[:headerSaltSize], salt)
	c.Encrypt(record[headerSaltSize:], plain, 0)
	return record, nil
}
