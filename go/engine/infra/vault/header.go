package vault

import (
	"bytes"
	"crypto/aes"
	"crypto/pbkdf2"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"hash/crc32"

	"golang.org/x/crypto/xts"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/secret"
)

// Sizes and offsets straight from the VeraCrypt Volume Format Specification.
// A container is: a 64 KiB header region, a data area, and a 64 KiB backup
// header at the end. The header region's first 512 bytes are the only part
// this driver reads or writes; the rest of the region, like the whole data
// area at creation time, is filled with random bytes so no boundary in the
// file leaks where real content starts.
const (
	headerRegionSize  = 64 << 10
	headerRecordSize  = 512
	headerSaltSize    = 64
	headerCipherSize  = 448 // the encrypted part of headerRecordSize
	headerKeyAreaSize = 256 // reserved key material; this driver uses the first 64 bytes of it
	xtsKeySize        = 64  // primary (32) plus secondary (32) AES-256 XTS keys
	veraMagic         = "VERA"

	minContainerDataMiB = 16
	maxContainerDataMiB = 1 << 20 // 1 TiB
)

// ErrWrongPassword means no supported header key derivation produced the
// "VERA" magic: either the password is wrong, or the container uses a hash
// or cipher this build does not implement. The two are cryptographically
// indistinguishable without the password, so the message names exactly what
// this build tried, which is the actionable fact an operator can go check.
var ErrWrongPassword = errors.New("vault: wrong password, or a header algorithm this build does not support " +
	"(this build tries PBKDF2-HMAC-SHA-512/500000 and PBKDF2-HMAC-SHA-256/200000)")

// ErrHeaderCorrupt means a header decrypted (its magic matched, so the
// password and key derivation were both right) but one of its two CRC-32
// checks failed. This is deliberately a different error from
// ErrWrongPassword: a wrong password almost never produces a valid magic by
// chance, so reaching a CRC mismatch after a magic match means the header
// itself was damaged, not that the password was mistyped.
var ErrHeaderCorrupt = errors.New("vault: header decrypted but failed its integrity check, the container header is corrupt")

// headerKDF is one header key derivation this build implements.
type headerKDF struct {
	name       string
	newHash    func() hash.Hash
	iterations int
}

// supportedHeaderKDFs are tried in order against every header this driver
// opens. VeraCrypt itself offers more (Argon2id, Whirlpool, Streebog,
// BLAKE2s, and other iteration counts via PIM): a container using one of
// those never matches here, and comes back as ErrWrongPassword naming what
// this build does support instead.
//
// Held behind a function rather than a package variable, because a variable
// is state a caller could reassign; this table never changes at runtime.
func supportedHeaderKDFs() []headerKDF {
	return []headerKDF{
		{"PBKDF2-HMAC-SHA-512/500000", sha512.New, 500000},
		{"PBKDF2-HMAC-SHA-256/200000", sha256.New, 200000},
	}
}

// headerFields is a VeraCrypt volume header's decoded, non-secret content.
type headerFields struct {
	version           uint16
	minProgramVersion uint16
	volumeSize        uint64
	dataAreaOffset    uint64
	dataAreaSize      uint64
	flags             uint32
	sectorSize        uint32
}

// deriveHeaderKey runs PBKDF2 over password and salt with kdf's hash and
// iteration count, producing the XTS key material (two AES-256 keys back to
// back) VeraCrypt's header encryption uses.
//
// password.Reveal() aliases the Secret's own buffer; the copy pbkdf2.Key
// forces by requiring a string is the one copy secret.Secret's own contract
// already documents it cannot prevent.
func deriveHeaderKey(password secret.Secret, salt []byte, kdf headerKDF) ([]byte, error) {
	return pbkdf2.Key(kdf.newHash, string(password.Reveal()), salt, kdf.iterations, xtsKeySize)
}

// decryptHeaderRecord tries every supported key derivation against record
// (the 512-byte salt-plus-encrypted-fields block) and returns the fields and
// master keys of the first one whose decrypted magic and both CRC-32s check
// out.
func decryptHeaderRecord(record []byte, password secret.Secret) (headerFields, [xtsKeySize]byte, error) {
	if len(record) != headerRecordSize {
		return headerFields{}, [xtsKeySize]byte{}, fmt.Errorf("vault: header record must be %d bytes, got %d", headerRecordSize, len(record))
	}
	salt := record[:headerSaltSize]
	ciphertext := record[headerSaltSize:]

	for _, kdf := range supportedHeaderKDFs() {
		key, err := deriveHeaderKey(password, salt, kdf)
		if err != nil {
			continue
		}
		cipher, err := xts.NewCipher(aes.NewCipher, key)
		if err != nil {
			continue
		}
		plain := make([]byte, headerCipherSize)
		cipher.Decrypt(plain, ciphertext, 0)
		if !bytes.Equal(plain[0:4], []byte(veraMagic)) {
			continue
		}
		return parseHeaderPlaintext(plain)
	}
	return headerFields{}, [xtsKeySize]byte{}, ErrWrongPassword
}

// parseHeaderPlaintext decodes the 448-byte decrypted header body, whose
// magic the caller has already matched, validating both CRC-32s before
// trusting any other field: a header that decrypted under the right key but
// was damaged afterward must be refused, not read as if it were intact.
func parseHeaderPlaintext(plain []byte) (headerFields, [xtsKeySize]byte, error) {
	keysCRC := binary.BigEndian.Uint32(plain[8:12])
	if crc32.ChecksumIEEE(plain[192:448]) != keysCRC {
		return headerFields{}, [xtsKeySize]byte{}, ErrHeaderCorrupt
	}
	headerCRC := binary.BigEndian.Uint32(plain[188:192])
	if crc32.ChecksumIEEE(plain[0:188]) != headerCRC {
		return headerFields{}, [xtsKeySize]byte{}, ErrHeaderCorrupt
	}
	fields := headerFields{
		version:           binary.BigEndian.Uint16(plain[4:6]),
		minProgramVersion: binary.BigEndian.Uint16(plain[6:8]),
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
	var keys [xtsKeySize]byte
	copy(keys[:], plain[192:192+xtsKeySize])
	return fields, keys, nil
}

// ErrHeaderFieldsInvalid names a header that decrypted and passed both
// CRC-32 checks, but whose fields describe something this driver cannot
// safely open: every one of these values came from the container file, so
// none of them is trusted before this check runs.
var ErrHeaderFieldsInvalid = errors.New("vault: header fields describe an invalid volume")

// validateHeaderFields bounds every header field against the actual
// container file size before anything downstream computes an offset or an
// allocation from it.
func validateHeaderFields(f headerFields, containerSize uint64) error {
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
func buildHeaderPlaintext(f headerFields, keys [xtsKeySize]byte, keyAreaPadding [headerKeyAreaSize - xtsKeySize]byte) []byte {
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
	copy(plain[192:192+xtsKeySize], keys[:])
	copy(plain[192+xtsKeySize:448], keyAreaPadding[:])
	binary.BigEndian.PutUint32(plain[8:12], crc32.ChecksumIEEE(plain[192:448]))
	binary.BigEndian.PutUint32(plain[188:192], crc32.ChecksumIEEE(plain[0:188]))
	return plain
}

// encryptHeaderRecord derives a header key from password and salt using the
// creation KDF (the first and strongest entry in supportedHeaderKDFs) and
// encrypts plain under it, returning the full 512-byte on-disk record.
func encryptHeaderRecord(plain []byte, password secret.Secret, salt []byte) ([]byte, error) {
	key, err := deriveHeaderKey(password, salt, supportedHeaderKDFs()[0])
	if err != nil {
		return nil, fmt.Errorf("vault: derive header key: %w", err)
	}
	cipher, err := xts.NewCipher(aes.NewCipher, key)
	if err != nil {
		return nil, fmt.Errorf("vault: build header cipher: %w", err)
	}
	record := make([]byte, headerRecordSize)
	copy(record[:headerSaltSize], salt)
	cipher.Encrypt(record[headerSaltSize:], plain, 0)
	return record, nil
}
